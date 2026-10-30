/*
 * A host that accepts the socket and then says nothing.
 *
 * x/crypto/ssh puts `ClientConfig.Timeout` on the TCP connect only: reading the
 * version banner and running the key exchange have no deadline at all, and the
 * channel-backed conn a jump-host tunnel hands over answers "deadline not
 * supported" even if someone did try. Before `dial` grew a watchdog, a peer like
 * this — a dead port-forward, a bastion aimed at nothing, a load balancer with no
 * backend — left `Connect` blocked forever and the window sitting on
 * "connecting…" until the app was killed.
 *
 * These tests are the reason the watchdog exists, and they fail (by hanging until
 * the go test timeout) if it is removed.
 */
package sshx

import (
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/testutil"
)

// silentServer accepts TCP connections and never writes a byte.
type silentServer struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newSilentServer(t *testing.T) *silentServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &silentServer{ln: ln}
	t.Cleanup(s.close)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Held open on purpose: closing it would look like a refused
			// connection rather than a peer that is alive and mute.
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *silentServer) close() {
	_ = s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.conns = nil
}

func newTestManager(t *testing.T, timeout time.Duration) *Manager {
	t.Helper()
	dir := testutil.TempDir(t)
	mgr := NewManager(NewHostKeyStore(filepath.Join(dir, "known_hosts.json")))
	mgr.dialTimeout = timeout
	t.Cleanup(mgr.CloseAll)
	return mgr
}

func TestConnectingToASilentHostGivesUp(t *testing.T) {
	silent := newSilentServer(t)
	host, port, err := netHostPort(silent.ln.Addr().String())
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	mgr := newTestManager(t, 300*time.Millisecond)

	start := time.Now()
	_, err = mgr.Connect(Config{Host: host, Port: port, User: "tester", Password: "x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("connect to a silent host reported success")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("connect took %s; the handshake is not bounded", elapsed)
	}
	// The same sentence a slow connect already produces, so the user cannot tell
	// (and does not need to) which of the two ways it stalled.
	if !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("error = %q, want the \"no answer from … within …\" wording", err)
	}
}

func TestConnectingThroughAJumpHostToASilentTargetGivesUp(t *testing.T) {
	// The common shape of the bug: the bastion happily opens the channel, and the
	// far end of it never speaks. Nothing on the tunnel can be given a deadline,
	// so only the watchdog can end this.
	silent := newSilentServer(t)
	targetHost, targetPort, err := netHostPort(silent.ln.Addr().String())
	if err != nil {
		t.Fatalf("address: %v", err)
	}

	jump, err := sshtest.New(sshtest.Options{Password: "jump-pw", AllowForwarding: true})
	if err != nil {
		t.Fatalf("start jump host: %v", err)
	}
	t.Cleanup(func() { _ = jump.Close() })
	proxyHost, proxyPort, err := netHostPort(jump.Addr())
	if err != nil {
		t.Fatalf("jump address: %v", err)
	}

	mgr := newTestManager(t, 300*time.Millisecond)

	start := time.Now()
	_, err = mgr.Connect(Config{
		Host:     targetHost,
		Port:     targetPort,
		User:     "tester",
		Password: "x",
		Proxy:    &ProxyConfig{Host: proxyHost, Port: proxyPort, User: "tester", Password: "jump-pw"},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("connect through a jump host to a silent target reported success")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("connect took %s; the tunnelled handshake is not bounded", elapsed)
	}
	if !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("error = %q, want the \"no answer from … within …\" wording", err)
	}
}
