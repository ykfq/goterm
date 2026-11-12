// Package lrzsz is a remote peer for the app's ZMODEM support: an `rz` and an
// `sz` that speak the protocol over the SSH shell, so a test can drive an
// upload or a download the way a real server would.
package lrzsz

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/zmodem"
)

// Shell is the remote end of a session: it answers `rz` and `sz` and ignores
// anything else, like a shell that only has those two binaries.
type Shell struct {
	// Dir is where uploads land and where downloads are served from.
	Dir string
}

// Run implements sshtest.ShellFunc.
func (s Shell) Run(sh *sshtest.Shell) {
	dir := s.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "goterm-lrzsz")
	}
	_ = os.MkdirAll(dir, 0o755)
	in := bufio.NewReader(sh.In())
	out := sh.Out()
	io.WriteString(out, "lrzsz ready\r\n")
	for {
		io.WriteString(out, "$ ")
		line, err := readLine(in, out)
		if err != nil {
			return
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "rzstrict":
			io.WriteString(out, "\r\n")
			names, err := strictRZ(in, out, dir)
			if err != nil {
				io.WriteString(out, "rz: "+err.Error()+"\r\n")
				continue
			}
			io.WriteString(out, fmt.Sprintf("rz strict received %d file(s)\r\n", len(names)))
		case "rz":
			io.WriteString(out, "\r\n")
			saved, err := (&zmodem.Receiver{In: in, Out: out, Dir: dir}).Receive()
			if err != nil && len(saved) == 0 {
				io.WriteString(out, "rz: "+err.Error()+"\r\n")
				continue
			}
			io.WriteString(out, "rz done\r\n")
		case "sz":
			io.WriteString(out, "\r\n")
			files, err := zmodem.ReadPaths(join(dir, fields[1:]))
			if err != nil || len(files) == 0 {
				io.WriteString(out, "sz: no such file\r\n")
				continue
			}
			serr := (&zmodem.Sender{In: in, Out: out}).Send(files)
			if serr != nil {
				io.WriteString(out, "sz: "+serr.Error()+"\r\n")
			}
			io.WriteString(out, "sz done\r\n")
		default:
			io.WriteString(out, "unknown command\r\n")
		}
	}
}

// readLine reads one CR-terminated line, echoing it like a PTY would.
func readLine(in *bufio.Reader, out io.Writer) (string, error) {
	var line []byte
	for {
		b, err := in.ReadByte()
		if err != nil {
			return string(line), err
		}
		switch b {
		case '\r', '\n':
			io.WriteString(out, "\r\n")
			return string(line), nil
		case 0x7f, 0x08:
			if len(line) > 0 {
				line = line[:len(line)-1]
				io.WriteString(out, "\b \b")
			}
		default:
			line = append(line, b)
			out.Write([]byte{b})
		}
	}
}

func join(dir string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if filepath.IsAbs(n) {
			out = append(out, n)
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// --- a peer that insists on what lrzsz insists on --------------------------
//
// The rest of this fake is a mirror of GoTerm's own ZMODEM code, which proves
// the two halves agree with each other and nothing about the program people
// actually run. `rzstrict` is written the other way round: it is a receiver
// that stops moving unless the sender behaves like sz does. It refuses a file
// whose descriptor claims nothing, refuses data that arrives at an offset it
// did not ask for, and waits for the ZCRCR that acknowledges a window before it
// reads another one.

// strictRZ receives one or more files the way lrzsz's rz does and returns the
// names it saved.
func strictRZ(in *bufio.Reader, out io.Writer, dir string) ([]string, error) {
	r := zmodem.NewReader(in)
	// A plain rz announces itself with ZRINIT and waits; only `rz -R` sends a
	// ZRQINIT first.
	out.Write(hexHdr(zmodem.ZRINIT, []byte{0, 0, 0, 0, 0, 1}))
	var saved []string
	for {
		f, err := r.Next()
		if err != nil {
			return saved, err
		}
		switch f.Type {
		case zmodem.ZRQINIT:
			// An rz answers a request to send with a fresh invitation; without
			// this the sender waits for a ZRINIT it has already been given once.
			out.Write(hexHdr(zmodem.ZRINIT, []byte{0, 0, 0, 0, 0, 1}))
		case zmodem.ZFIN:
			out.Write(hexHdr(zmodem.ZFIN, nil))
			return saved, nil
		case zmodem.ZFILE:
			name, size := strictDescriptor(f.Data)
			if name == "" {
				return saved, fmt.Errorf("strict rz: no name in the descriptor")
			}
			if size <= 0 {
				return saved, fmt.Errorf("strict rz: %s declares %d bytes", name, size)
			}
			out.Write(hexHdr(zmodem.ZRPOS, strictOffset(0)))
			data, err := strictData(r, out, size)
			if err != nil {
				return saved, err
			}
			target := filepath.Join(dir, filepath.Base(name))
			if err := os.WriteFile(target, data, 0o644); err != nil {
				return saved, err
			}
			saved = append(saved, filepath.Base(name))
			out.Write(hexHdr(zmodem.ZRINIT, []byte{0, 0, 0, 0, 0, 1}))
		}
	}
}

// strictData reads one file's windows. A window that asked to be acknowledged
// is not answered with silence: that is the exchange that used to stop both
// sides waiting for the other.
func strictData(r *zmodem.Reader, out io.Writer, size int64) ([]byte, error) {
	var data []byte
	for {
		f, err := r.Next()
		if err != nil {
			return nil, err
		}
		switch f.Type {
		case zmodem.ZEOF:
			if f.Offset != uint32(size) {
				return nil, fmt.Errorf("strict rz: ZEOF says %d, descriptor said %d", f.Offset, size)
			}
			if int64(len(data)) != size {
				return nil, fmt.Errorf("strict rz: got %d of %d bytes", len(data), size)
			}
			return data, nil
		case zmodem.ZDATA:
			if int(f.Offset) != len(data) {
				return nil, fmt.Errorf("strict rz: data at %d, we have %d", f.Offset, len(data))
			}
			data = append(data, f.Data...)
			if f.Kind == zmodem.ZCRCQ || f.Kind == zmodem.ZCRCW {
				out.Write(hexHdr(zmodem.ZCRC, strictOffset(uint32(zmodem.ZCRCR))))
			}
		case zmodem.ZABORT:
			return nil, fmt.Errorf("strict rz: the sender aborted")
		}
	}
}

// strictDescriptor is lrzsz's ZFILE text: name, NUL, then mode, size, count
// and times.
func strictDescriptor(args []byte) (string, int64) {
	i := bytes.IndexByte(args, 0)
	if i <= 0 {
		return "", 0
	}
	name := string(args[:i])
	fields := strings.Fields(string(args[i+1:]))
	if len(fields) < 2 {
		return name, 0
	}
	size, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return name, 0
	}
	return name, size
}

// hexHdr writes the header form lrzsz always uses: PAD PAD ZDLE B, then the
// type, a four-byte header word, and CRC16 — twelve hex digits. Short headers
// are what our own code used to write; peers pad to the full word.
func hexHdr(typ byte, word []byte) []byte {
	if len(word) < 4 {
		word = append(append(make([]byte, 0, 4), word...), make([]byte, 4-len(word))...)
	}
	args := word
	body := append([]byte{typ}, args...)
	crc := zmodem.CRC16(body)
	out := []byte{zmodem.ZPAD, zmodem.ZPAD, zmodem.ZDLE, zmodem.ZHEX}
	for _, b := range append(body, byte(crc>>8), byte(crc)) {
		out = append(out, "0123456789abcdef"[b>>4], "0123456789abcdef"[b&0x0f])
	}
	return append(out, '\r', '\n')
}

func strictOffset(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}
