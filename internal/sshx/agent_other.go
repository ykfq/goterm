//go:build !windows

package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

const agentDialTimeout = 3 * time.Second

// dialAgent connects to the ssh-agent named by SSH_AUTH_SOCK. The caller owns
// the returned conn and must keep it open for as long as its signers are in
// use, then close it.
func dialAgent() (net.Conn, string, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, "", errors.New("ssh-agent: SSH_AUTH_SOCK is not set and no agent is reachable")
	}
	conn, err := net.DialTimeout("unix", sock, agentDialTimeout)
	if err != nil {
		return nil, sock, fmt.Errorf("ssh-agent socket %s: %w", sock, err)
	}
	return conn, sock, nil
}
