// Command sshtest runs GoTerm's bundled SSH server so the app can be driven
// end-to-end without a remote host:
//
//	go run ./cmd/sshtest -addr 127.0.0.1:2222
//
// then connect in GoTerm to tester@127.0.0.1:2222 with the printed password.
//
// It serves a deterministic PTY emulator, not a shell — see internal/sshtest.
// It accepts any password matching the flag and trusts no host keys, so bind it
// to loopback unless you know why you would not.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"

	"github.com/ykfq/goterm/internal/sshtest"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2222", "listen address (loopback strongly advised)")
	user := flag.String("user", "tester", "accepted username")
	password := flag.String("password", "goterm-dev", "accepted password")
	banner := flag.String("banner", "GoTerm development SSH server — not a real host", "pre-auth banner")
	hostKeyPath := flag.String("hostkey", "", "PEM file for a stable host key (created if missing); empty means a fresh key each run")
	shellKind := flag.String("shell", "repeater", "interactive shell: repeater (deterministic PTY) or trzsz (serves tsz/trz transfers)")
	transferDir := flag.String("transfer-dir", "", "directory trzsz uploads land in (default: a temp directory)")
	twoFactor := flag.Bool("2fa", false, "password auth also needs a trailing one-time code (password<digits>), like a TOTP bastion")
	allowForwarding := flag.Bool("allow-forwarding", false, "accept direct-tcpip channels, so this server can act as a jump host")
	verbose := flag.Bool("v", false, "log every request")
	flag.Parse()

	var shell sshtest.ShellFunc
	switch *shellKind {
	case "repeater":
		shell = sshtest.Repeater
	case "trzsz":
		trzsz := sshtest.TrzszShell{Dir: *transferDir}
		shell = trzsz.Run
	default:
		fmt.Fprintf(os.Stderr, "sshtest: unknown shell %q (want repeater or trzsz)\n", *shellKind)
		os.Exit(1)
	}

	hostKey, err := loadOrCreateHostKey(*hostKeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshtest: host key: %v\n", err)
		os.Exit(1)
	}

	logger := log.New(os.Stderr, "sshtest ", log.Ltime|log.Lmicroseconds)
	if !*verbose {
		logger.SetOutput(io.Discard)
	}

	server, err := sshtest.NewOn(*addr, sshtest.Options{
		User:            *user,
		Password:        *password,
		Banner:          *banner,
		HostKey:         hostKey,
		Shell:           shell,
		TwoFactor:       *twoFactor,
		AllowForwarding: *allowForwarding,
		Logger:          logger,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshtest: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf(`sshtest listening on %s
  username   %s
  password   %s
  host key   %s
  terminal   try: echo hi, bytes, resize, spam 2000, b64 <base64>, exit
  shell      %s
  stop       Ctrl-C
`, server.Addr(), *user, *password, server.HostKeyFingerprint(), *shellKind)
	if *hostKeyPath != "" {
		fmt.Printf("  key file   %s\n", *hostKeyPath)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	fmt.Println("sshtest: shutting down")
	_ = server.Close()
}

// loadOrCreateHostKey reads a PEM private key, creating one on first run.
//
// A stable key matters for the client, not the server: GoTerm pins the
// fingerprint on first use, so an ephemeral key makes every restart of this
// development server look like a man-in-the-middle.
func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if path == "" {
		return nil, nil // internal/sshtest generates a throwaway key
	}
	data, err := os.ReadFile(path)
	if err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return signer, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("signer: %w", err)
	}
	return signer, nil
}
