package sshx

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

func TestAttachedTerminalGetsTheBytesAndTheLogToo(t *testing.T) {
	logDir := t.TempDir()
	h := newHarness(t, sshtest.Options{})
	if _, err := h.mgr.ConfigureSessionLogging(true, logDir); err != nil {
		t.Fatal(err)
	}
	sess := h.connect(h.config())
	out, err := sess.AttachOutput()
	if err != nil {
		t.Fatal(err)
	}

	// The terminal reader owns the output now: events must stay silent, or the
	// login banner would be delivered twice in two different ways.
	var mu sync.Mutex
	var got []byte
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := out.Read(buf)
			if n > 0 {
				mu.Lock()
				got = append(got, buf[:n]...)
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			seen := strings.Contains(string(got), want)
			mu.Unlock()
			if seen {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("the attached reader never saw %q", want)
	}
	waitFor("ready")

	if n := h.rec.countDataEvents(); n != 0 {
		t.Fatalf("%d ssh:data events were emitted while a terminal was attached", n)
	}

	if err := sess.Write([]byte("attach-probe\n")); err != nil {
		t.Fatal(err)
	}
	waitFor("attach-probe")

	info := sess.Info()
	if info.LogPath == "" {
		t.Fatal("session logging was on but the session reports no log file")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(info.LogPath)
		if err == nil && bytes.Contains(data, []byte("attach-probe")) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(info.LogPath)
	t.Fatalf("log %s does not hold the session's bytes: %q", info.LogPath, data)
}

func TestOutputQueueBlocksTheWriterAtTheLimit(t *testing.T) {
	q := newOutQueue(64)
	// Fill exactly to the limit; the next write must wait.
	q.write(bytes.Repeat([]byte("a"), 64))
	blocked := make(chan struct{})
	unblocked := make(chan struct{})
	go func() {
		close(blocked)
		q.write([]byte("b"))
		close(unblocked)
	}()
	<-blocked
	select {
	case <-unblocked:
		t.Fatal("a full queue let the writer through: the SSH reader would never apply backpressure")
	case <-time.After(50 * time.Millisecond):
	}

	buf := make([]byte, 8)
	if _, err := q.Read(buf); err != nil {
		t.Fatal(err)
	}
	select {
	case <-unblocked:
	case <-time.After(2 * time.Second):
		t.Fatal("reading one chunk did not release the blocked writer")
	}
}

func TestOutputQueueKeepsBytesInOrderAndEndsAfterClose(t *testing.T) {
	q := newOutQueue(1024)
	go func() {
		q.write([]byte("first"))
		q.write([]byte("second"))
		_ = q.Close()
	}()

	got, err := io.ReadAll(q)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "firstsecond" {
		t.Fatalf("got %q, want firstsecond", got)
	}
	if _, err := q.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("after Close and drain Read = %v, want EOF", err)
	}
}

func TestOutputQueueStaysBoundedUnderAWriterThatOutrunsTheReader(t *testing.T) {
	const limit = 8 << 10
	q := newOutQueue(limit)
	const total = 1 << 20

	var wrote atomic.Int64
	go func() {
		chunk := bytes.Repeat([]byte("x"), 4096)
		for wrote.Load() < total {
			q.write(chunk)
			wrote.Add(int64(len(chunk)))
		}
		_ = q.Close()
	}()

	// Read slowly and watch the queue never exceed its limit by more than one
	// write's worth. This is the property that keeps `cat huge.log` from
	// growing the process by 1 GB.
	var peak int
	buf := make([]byte, 1024)
	read := 0
	for read < total {
		n, err := q.Read(buf)
		read += n
		if d := q.queueDepth(); d > peak {
			peak = d
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if peak > limit+4096 {
		t.Fatalf("queue peaked at %d bytes, limit is %d (+ one write): backpressure is not holding", peak, limit)
	}
	if read != total {
		t.Fatalf("read %d of %d bytes", read, total)
	}
}

func TestSessionDeliversToTheTerminalInsteadOfEventsOnceAttached(t *testing.T) {
	m := NewManager(NewHostKeyStore(t.TempDir() + "/known.json"))
	var events atomic.Int64
	m.SetEmitter(func(string, any) { events.Add(1) })
	s := &Session{id: "s", mgr: m, info: SessionInfo{ID: "s"}}

	q, err := s.AttachOutput()
	if err != nil {
		t.Fatal(err)
	}
	s.deliver([]byte("hello"))
	if events.Load() != 0 {
		t.Fatal("output was emitted as an event while a terminal was attached")
	}
	buf := make([]byte, 8)
	n, _ := q.Read(buf)
	if string(buf[:n]) != "hello" {
		t.Fatalf("terminal read %q", buf[:n])
	}

	if _, err := s.AttachOutput(); err != ErrOutputAttached {
		t.Fatalf("second AttachOutput = %v, want ErrOutputAttached", err)
	}
}

func TestCloseOutputUnblocksAWriterWhenTheTerminalGoesAway(t *testing.T) {
	s := &Session{id: "s", mgr: NewManager(NewHostKeyStore(t.TempDir() + "/known.json"))}
	if _, err := s.AttachOutput(); err != nil {
		t.Fatal(err)
	}
	// Fill the queue so the next deliver blocks.
	q := s.attachedOutput()
	q.write(make([]byte, DefaultOutputLimit))

	done := make(chan struct{})
	go func() {
		s.deliver([]byte("late"))
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("deliver did not block on a full queue")
	case <-time.After(50 * time.Millisecond):
	}
	s.closeOutput()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the output left a writer blocked forever")
	}
}

func TestConcurrentAttachIsRefused(t *testing.T) {
	s := &Session{id: "s", mgr: NewManager(NewHostKeyStore(t.TempDir() + "/known.json"))}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AttachOutput()
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		} else if err != ErrOutputAttached {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d attachments succeeded, want exactly 1", ok)
	}
}
