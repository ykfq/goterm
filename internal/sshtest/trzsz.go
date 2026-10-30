// Server half of the trzsz protocol, so `trz` and `tsz` can be driven end to
// end against this development server instead of a remote host.
//
// It deliberately behaves like the third-party server GoTerm has to cope with:
// the banner carries a version but no unique id, arguments travel as
// base64(zlib(...)) frames, integer frames are plain digits, and file data
// rides in `#DATA:` frames in non-binary mode. If GoTerm can move a file
// against this, it can move one against a real trz remote.
package sshtest

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TrzszBanner is what the client sniffs. No trailing id, on purpose.
const TrzszBanner = "::TRZSZ:TRANSFER:%s:%s:"

// TrzszVersion is reported to the client; older or newer clients negotiate
// against it.
const TrzszVersion = "1.2.0"

// TrzszShell is an interactive shell that runs the repeater's commands and
// speaks the server side of trzsz for `trz` and `tsz`.
type TrzszShell struct {
	// Dir is where uploads land and where downloads are served from.
	Dir string
}

// Run implements ShellFunc.
func (t TrzszShell) Run(sh *Shell) {
	out := sh.Out()
	dir := t.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "goterm-sshtest")
	}
	_ = os.MkdirAll(dir, 0o755)

	writeString(out, "\x1b[0;32msshtest trzsz\x1b[0m ready — try: \x1b[1mtsz demo.bin\x1b[0m or \x1b[1mtrz\x1b[0m\r\n")
	writeString(out, "\x1b[2mtransfers land in "+dir+"\x1b[0m\r\n")

	rd := bufio.NewReader(sh.In())
	for {
		prompt(out)
		line, err := readEchoedLine(rd, out)
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "trz", "rz":
			if err := t.receive(rd, out, dir); err != nil {
				writeString(out, "\x1b[31mtrz: "+err.Error()+"\x1b[0m\r\n")
			}
		case "tsz":
			name := "demo.bin"
			if len(fields) > 1 {
				name = filepath.Base(fields[1])
			}
			if err := t.send(rd, out, dir, name); err != nil {
				writeString(out, "\x1b[31mtsz: "+err.Error()+"\x1b[0m\r\n")
			}
		default:
			// Same trivial commands the repeater offers, minus the transfer ones.
			writeString(out, "sshtest: "+line+"\r\n")
		}
	}
}

// readEchoedLine reads one line, echoing it the way a PTY would. The terminal
// has no local echo, so without this the user types into silence.
func readEchoedLine(rd *bufio.Reader, out io.Writer) (string, error) {
	var line []byte
	for {
		b, err := rd.ReadByte()
		if err != nil {
			return string(line), err
		}
		switch b {
		case '\r', '\n':
			writeString(out, "\r\n")
			return string(line), nil
		case 0x7f, 0x08:
			if len(line) > 0 {
				line = line[:len(line)-1]
				writeString(out, "\b \b")
			}
		case 0x03: // Ctrl-C
			writeString(out, "^C\r\n")
			line = line[:0]
		case 0x04: // Ctrl-D
			return "", io.EOF
		default:
			line = append(line, b)
			if _, err := out.Write([]byte{b}); err != nil {
				return "", err
			}
		}
	}
}

// --- protocol primitives ---------------------------------------------------

func writeFrame(out io.Writer, id, payload string) {
	writeString(out, "#"+id+":"+payload+"\n")
}

func writeInteger(out io.Writer, id string, value int64) {
	writeFrame(out, id, fmt.Sprintf("%d", value))
}

func writeStringFrame(out io.Writer, id, text string) {
	writeFrame(out, id, encodeText(text))
}

func encodeText(text string) string {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write([]byte(text))
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func encodeBinary(data []byte) string {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func decodePayload(payload string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("zlib: %w", err)
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

func decodeText(payload string) (string, error) {
	data, err := decodePayload(payload)
	return string(data), err
}

// readFrame reads one `#ID:payload` line and splits it.
func readFrame(rd *bufio.Reader) (id, payload string, err error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "#") {
		return "", "", fmt.Errorf("unexpected frame %q", line)
	}
	line = line[1:]
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return line, "", nil
	}
	return line[:colon], line[colon+1:], nil
}

// expectFrame reads the next frame and fails unless it carries the wanted id.
func expectFrame(rd *bufio.Reader, want string) (string, error) {
	id, payload, err := readFrame(rd)
	if err != nil {
		return "", err
	}
	if id != want {
		return "", fmt.Errorf("expected #%s, got #%s", want, id)
	}
	return payload, nil
}

// handshake sends the banner and completes ACT/CFG with the client.
func handshake(rd *bufio.Reader, out io.Writer, direction string) (map[string]any, error) {
	writeString(out, fmt.Sprintf(TrzszBanner, direction, TrzszVersion)+"\r\n")

	payload, err := expectFrame(rd, "ACT")
	if err != nil {
		return nil, err
	}
	text, err := decodeText(payload)
	if err != nil {
		return nil, err
	}
	var action map[string]any
	if err := json.Unmarshal([]byte(text), &action); err != nil {
		return nil, fmt.Errorf("ACT payload: %w", err)
	}
	if confirm, _ := action["confirm"].(bool); !confirm {
		return nil, fmt.Errorf("client declined the transfer")
	}

	config, _ := json.Marshal(map[string]any{"lang": "js", "bufsize": 32768, "timeout": 100})
	writeStringFrame(out, "CFG", string(config))
	return action, nil
}

// --- upload: the client sends, this server receives (`trz`) ----------------

func (t TrzszShell) receive(rd *bufio.Reader, out io.Writer, dir string) error {
	if _, err := handshake(rd, out, "R"); err != nil {
		return err
	}

	count, err := readInteger(rd, "NUM")
	if err != nil {
		return err
	}
	writeInteger(out, "SUCC", count)

	for i := int64(0); i < count; i++ {
		namePayload, err := expectFrame(rd, "NAME")
		if err != nil {
			return err
		}
		name, err := decodeText(namePayload)
		if err != nil {
			return err
		}
		name = filepath.Base(strings.TrimSpace(name))
		writeStringFrame(out, "SUCC", name)

		size, err := readInteger(rd, "SIZE")
		if err != nil {
			return err
		}
		writeInteger(out, "SUCC", size)

		path := filepath.Join(dir, name)
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		digest := md5.New()
		received := int64(0)
		for received < size {
			payload, err := expectFrame(rd, "DATA")
			if err != nil {
				_ = file.Close()
				return err
			}
			chunk, err := decodePayload(payload)
			if err != nil {
				_ = file.Close()
				return err
			}
			if _, err := file.Write(chunk); err != nil {
				_ = file.Close()
				return err
			}
			digest.Write(chunk)
			received += int64(len(chunk))
			writeInteger(out, "SUCC", int64(len(chunk)))
		}
		if err := file.Close(); err != nil {
			return err
		}

		remote, err := expectFrame(rd, "MD5")
		if err != nil {
			return err
		}
		expected, err := base64.StdEncoding.DecodeString(remote)
		if err != nil {
			return err
		}
		if !bytes.Equal(digest.Sum(nil), expected) {
			return fmt.Errorf("md5 mismatch for %s", name)
		}
		writeFrame(out, "SUCC", remote)

		writeString(out, fmt.Sprintf("\r\n\x1b[32m%s: received %s (%d bytes)\x1b[0m\r\n",
			name, humanBytes(size), size))
	}

	// The client says goodbye; the summary it sends is the one to display.
	if payload, err := expectFrame(rd, "EXIT"); err == nil {
		if text, err := decodeText(payload); err == nil && text != "" {
			writeString(out, "\x1b[2m"+text+"\x1b[0m\r\n")
		}
	}
	return nil
}

// --- download: this server sends, the client receives (`tsz`) -------------

func (t TrzszShell) send(rd *bufio.Reader, out io.Writer, dir, name string) error {
	data, err := t.downloadPayload(dir, name)
	if err != nil {
		return err
	}
	if _, err := handshake(rd, out, "S"); err != nil {
		return err
	}

	writeInteger(out, "NUM", 1)
	if _, err := readInteger(rd, "SUCC"); err != nil {
		return err
	}

	writeStringFrame(out, "NAME", name)
	if _, err := readString(rd, "SUCC"); err != nil {
		return err
	}

	writeInteger(out, "SIZE", int64(len(data)))
	if _, err := readInteger(rd, "SUCC"); err != nil {
		return err
	}

	const chunkSize = 32768
	digest := md5.New()
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		digest.Write(chunk)
		writeFrame(out, "DATA", encodeBinary(chunk))
		if _, err := readInteger(rd, "SUCC"); err != nil {
			return err
		}
	}

	writeFrame(out, "MD5", base64.StdEncoding.EncodeToString(digest.Sum(nil)))
	if _, err := expectFrame(rd, "SUCC"); err != nil {
		return err
	}

	writeString(out, fmt.Sprintf("\r\n\x1b[32m%s: sent %s (%d bytes)\x1b[0m\r\n",
		name, humanBytes(int64(len(data))), len(data)))
	if payload, err := expectFrame(rd, "EXIT"); err == nil {
		if text, err := decodeText(payload); err == nil && text != "" {
			writeString(out, "\x1b[2m"+text+"\x1b[0m\r\n")
		}
	}
	return nil
}

// downloadPayload serves the file if it exists, or generates a deterministic
// one so `tsz demo.bin` always has something to send.
func (t TrzszShell) downloadPayload(dir, name string) ([]byte, error) {
	path := filepath.Join(dir, filepath.Base(name))
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}
	const size = 262144
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*7 + 11) & 0xff)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	return data, nil
}

func readInteger(rd *bufio.Reader, id string) (int64, error) {
	payload, err := expectFrame(rd, id)
	if err != nil {
		return 0, err
	}
	var value int64
	if _, err := fmt.Sscanf(payload, "%d", &value); err != nil {
		return 0, fmt.Errorf("#%s: %w", id, err)
	}
	return value, nil
}

func readString(rd *bufio.Reader, id string) (string, error) {
	payload, err := expectFrame(rd, id)
	if err != nil {
		return "", err
	}
	return decodeText(payload)
}

func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(n)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}
