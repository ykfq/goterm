// The client half of the trzsz protocol, written for these tests only.
//
// It exists so the server side in trzsz.go is checked against the exact frames
// the JavaScript client sends and expects — banner, ACT, CFG, NUM, NAME, SIZE,
// DATA, MD5, EXIT — with base64(zlib(...)) payloads and plain integer frames.
package sshtest

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder collects everything the shell writes, so a write can never block on
// a test that is busy doing something else.
type recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *recorder) takeLine() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := r.buf.Bytes()
	if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
		line := string(data[:newline])
		r.buf.Next(newline + 1)
		return strings.TrimRight(line, "\r"), true
	}
	return "", false
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

type trzszClient struct {
	t      *testing.T
	out    io.Writer
	screen *recorder
	finish func(wantErr bool)
}

// startServer runs one transfer direction against the shell in a goroutine and
// hands back the client end of the pipe.
func startServer(t *testing.T, run func(rd *bufio.Reader, out io.Writer) error) *trzszClient {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	screen := &recorder{}
	done := make(chan error, 1)
	go func() {
		defer serverIn.Close()
		done <- run(bufio.NewReader(serverIn), screen)
	}()

	client := &trzszClient{t: t, out: clientOut, screen: screen}
	client.finish = func(wantErr bool) {
		_ = clientOut.Close()
		select {
		case err := <-done:
			if wantErr && err == nil {
				t.Fatal("server side finished without the expected error")
			}
			if !wantErr && err != nil {
				t.Fatalf("server side failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server side did not finish")
		}
	}
	return client
}

func (c *trzszClient) readLine() string {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if line, ok := c.screen.takeLine(); ok {
			return line
		}
		time.Sleep(time.Millisecond)
	}
	c.t.Fatalf("timed out waiting for server output; saw %q", c.screen.text())
	return ""
}

func (c *trzszClient) send(id, payload string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.out, "#%s:%s\n", id, payload); err != nil {
		c.t.Fatalf("writing #%s: %v", id, err)
	}
}

func (c *trzszClient) expect(id string) string {
	c.t.Helper()
	line := c.readLine()
	want := "#" + id + ":"
	if !strings.HasPrefix(line, want) {
		c.t.Fatalf("expected %s..., got %q", want, line)
	}
	return line[len(want):]
}

// handshake answers a banner the way the JS client does.
func (c *trzszClient) handshake(direction string) {
	c.t.Helper()
	line := c.readLine()
	if !strings.Contains(line, "::TRZSZ:TRANSFER:"+direction+":"+TrzszVersion+":") {
		c.t.Fatalf("unexpected banner %q", line)
	}
	c.send("ACT", encodeText(`{"lang":"js","confirm":true,"version":"1.1.6","support_dir":true}`))
	cfg := c.expect("CFG")
	text, err := decodeText(cfg)
	if err != nil {
		c.t.Fatalf("decoding CFG: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		c.t.Fatalf("CFG is not JSON: %q", text)
	}
	if parsed["lang"] != "js" {
		c.t.Fatalf("CFG lang = %v, want js", parsed["lang"])
	}
}

func TestTrzszShellReceive(t *testing.T) {
	dir := t.TempDir()
	payload := make([]byte, 100_000)
	for i := range payload {
		payload[i] = byte((i*13 + 3) & 0xff)
	}

	shell := TrzszShell{Dir: dir}
	client := startServer(t, func(rd *bufio.Reader, out io.Writer) error {
		return shell.receive(rd, out, dir)
	})

	client.handshake("R")
	client.send("NUM", "1")
	if got := client.expect("SUCC"); got != "1" {
		t.Fatalf("NUM ack = %q", got)
	}

	client.send("NAME", encodeText("upload.txt"))
	if got := client.expect("SUCC"); got != encodeText("upload.txt") {
		t.Fatalf("NAME ack = %q", got)
	}

	client.send("SIZE", fmt.Sprintf("%d", len(payload)))
	if got := client.expect("SUCC"); got != fmt.Sprintf("%d", len(payload)) {
		t.Fatalf("SIZE ack = %q", got)
	}

	digest := md5.Sum(payload)
	const chunkSize = 32768
	for offset := 0; offset < len(payload); offset += chunkSize {
		end := offset + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[offset:end]
		client.send("DATA", encodeBinary(chunk))
		// The server acknowledges the exact number of bytes it decoded.
		if got := client.expect("SUCC"); got != fmt.Sprintf("%d", len(chunk)) {
			t.Fatalf("DATA ack = %q, want %d", got, len(chunk))
		}
	}

	client.send("MD5", base64.StdEncoding.EncodeToString(digest[:]))
	if got := client.expect("SUCC"); got != base64.StdEncoding.EncodeToString(digest[:]) {
		t.Fatalf("MD5 ack = %q", got)
	}

	client.send("EXIT", encodeText("Saved 1 file/directory to /tmp"))
	client.finish(false)

	written, err := os.ReadFile(filepath.Join(dir, "upload.txt"))
	if err != nil {
		t.Fatalf("reading received file: %v", err)
	}
	if !bytes.Equal(written, payload) {
		t.Fatalf("received %d bytes, want %d identical bytes", len(written), len(payload))
	}
}

func TestTrzszShellSend(t *testing.T) {
	dir := t.TempDir()
	shell := TrzszShell{Dir: dir}

	client := startServer(t, func(rd *bufio.Reader, out io.Writer) error {
		return shell.send(rd, out, dir, "demo.bin")
	})

	client.handshake("S")

	if got := client.expect("NUM"); got != "1" {
		t.Fatalf("NUM = %q", got)
	}
	client.send("SUCC", "1")

	name, err := decodeText(client.expect("NAME"))
	if err != nil {
		t.Fatalf("NAME: %v", err)
	}
	if name != "demo.bin" {
		t.Fatalf("NAME = %q, want demo.bin", name)
	}
	client.send("SUCC", encodeText(name))

	var size int64
	if _, err := fmt.Sscanf(client.expect("SIZE"), "%d", &size); err != nil {
		t.Fatalf("SIZE: %v", err)
	}
	client.send("SUCC", fmt.Sprintf("%d", size))

	var received bytes.Buffer
	digest := md5.New()
	for int64(received.Len()) < size {
		chunk, err := decodePayload(client.expect("DATA"))
		if err != nil {
			t.Fatalf("DATA: %v", err)
		}
		received.Write(chunk)
		digest.Write(chunk)
		client.send("SUCC", fmt.Sprintf("%d", len(chunk)))
	}

	remote := client.expect("MD5")
	if remote != base64.StdEncoding.EncodeToString(digest.Sum(nil)) {
		t.Fatalf("MD5 frame %q does not match the data sent", remote)
	}
	client.send("SUCC", remote)
	client.send("EXIT", encodeText("Saved 1 file/directory"))
	client.finish(false)

	served, err := os.ReadFile(filepath.Join(dir, "demo.bin"))
	if err != nil {
		t.Fatalf("reading served file: %v", err)
	}
	if !bytes.Equal(served, received.Bytes()) {
		t.Fatalf("client received %d bytes, server offered %d", received.Len(), len(served))
	}
	if len(served) == 0 {
		t.Fatal("server offered no data")
	}
}

func TestTrzszShellHonoursDeclinedTransfer(t *testing.T) {
	dir := t.TempDir()
	shell := TrzszShell{Dir: dir}

	client := startServer(t, func(rd *bufio.Reader, out io.Writer) error {
		return shell.receive(rd, out, dir)
	})

	if line := client.readLine(); !strings.Contains(line, "::TRZSZ:TRANSFER:R:") {
		t.Fatalf("unexpected banner %q", line)
	}
	client.send("ACT", encodeText(`{"lang":"js","confirm":false,"version":"1.1.6"}`))
	// Declining is a failure the shell reports, not a crash.
	client.finish(true)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("declined transfer still wrote %d entries", len(entries))
	}
}
