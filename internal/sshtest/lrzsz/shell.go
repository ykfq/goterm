// Package lrzsz is a remote peer for the app's ZMODEM support: an `rz` and an
// `sz` that speak the protocol over the SSH shell, so a test can drive an
// upload or a download the way a real server would.
package lrzsz

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
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
