//go:build windows

package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

const (
	agentDialTimeout = 3 * time.Second
	// opensshAgentPipe is the named pipe published by the "OpenSSH
	// Authentication Agent" service on Windows.
	opensshAgentPipe = `\\.\pipe\openssh-ssh-agent`
)

// dialAgent connects to a Windows ssh-agent. On this platform the agent is a
// named pipe, not a socket, so SSH_AUTH_SOCK is only usable when it already
// names a pipe; otherwise fall back to the stock OpenSSH pipe.
func dialAgent() (net.Conn, string, error) {
	candidates := []string{opensshAgentPipe}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" && strings.Contains(sock, `\pipe\`) {
		candidates = append([]string{sock}, candidates...)
	}

	timeout := agentDialTimeout
	var lastErr error
	for _, pipe := range candidates {
		conn, err := winio.DialPipe(pipe, &timeout)
		if err == nil {
			return conn, pipe, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no agent pipe configured")
	}
	return nil, "", fmt.Errorf("ssh-agent: %w (start the \"OpenSSH Authentication Agent\" service, or use key-file auth)", lastErr)
}
