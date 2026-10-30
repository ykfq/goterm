package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ErrPassphraseRequired reports that a private key is passphrase-protected and
// either no passphrase was supplied, or the one supplied did not decrypt it.
//
// Wails hands errors to JavaScript as plain text, so the frontend recognises
// this case by its leading words rather than by type. Keep the prefix.
var ErrPassphraseRequired = errors.New("passphrase required")

// credential is one endpoint's authentication material, with no idea whether it
// belongs to the target or to the jump host.
type credential struct {
	AuthType   string
	Password   string
	KeyPath    string
	Passphrase string
}

func credsFor(c Config) credential {
	return credential{AuthType: c.AuthType, Password: c.Password, KeyPath: c.KeyPath, Passphrase: c.Passphrase}
}

func credsForProxy(p ProxyConfig) credential {
	return credential{AuthType: p.AuthType, Password: p.Password, KeyPath: p.KeyPath, Passphrase: p.Passphrase}
}

// authMethods builds the SSH auth methods for cfg.
//
// It also reports which kinds it offered (so a denied login can be explained)
// and returns a closer for anything that must outlive authentication — an
// ssh-agent connection, whose signers stop working once it is closed. The
// caller owns the closer and must close it when the session ends.
func authMethods(cfg Config) ([]ssh.AuthMethod, []string, io.Closer, error) {
	return authMethodsFor(credsFor(cfg), "")
}

// authMethodsFor is authMethods with an explicit credential and a label used to
// make errors say which endpoint failed ("proxy ...").
func authMethodsFor(c credential, label string) ([]ssh.AuthMethod, []string, io.Closer, error) {
	var (
		methods []ssh.AuthMethod
		offered []string
		closer  io.Closer
	)

	switch c.AuthType {
	case AuthPassword:
		methods = append(methods, ssh.Password(c.Password))
		// Plenty of sshds advertise keyboard-interactive only. Answering every
		// prompt with the configured password keeps those servers working.
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = c.Password
			}
			return answers, nil
		}))
		offered = append(offered, label+"password", label+"keyboard-interactive")
		return methods, offered, nil, nil

	case AuthKey:
		signer, err := signerForKey(c.KeyPath, c.Passphrase, label)
		if err != nil {
			return nil, nil, nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
		offered = append(offered, label+"publickey:"+shortName(c.KeyPath))
		return methods, offered, nil, nil

	case AuthAgent:
		conn, where, err := dialAgent()
		if err != nil {
			return nil, nil, nil, err
		}
		closer = conn
		signers, err := agent.NewClient(conn).Signers()
		if err != nil {
			_ = conn.Close()
			return nil, nil, nil, fmt.Errorf("ssh-agent (%s) signers: %w", where, err)
		}
		if len(signers) == 0 {
			_ = conn.Close()
			return nil, nil, nil, fmt.Errorf("ssh-agent (%s) holds no keys", where)
		}
		methods = append(methods, ssh.PublicKeys(signers...))
		offered = append(offered, fmt.Sprintf("%spublickey via agent:%s (%d keys)", label, where, len(signers)))
		return methods, offered, closer, nil
	}

	return nil, nil, nil, errors.New("no authentication methods available")
}

// signerForKey loads a PEM or OpenSSH private key, with an optional passphrase.
//
// An encrypted key without a usable passphrase is not a dead end: it returns an
// error wrapping ErrPassphraseRequired, which the frontend turns into a prompt
// and a retry.
func signerForKey(path, passphrase, label string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%sprivate key %s does not exist", label, path)
		}
		return nil, fmt.Errorf("read %sprivate key %s: %w", label, path, err)
	}
	if isPuttyKey(path, data) {
		// The commonest way a Windows key "silently fails to authenticate": a PuTTY
		// .ppk is not a format x/crypto can read, so there is nothing to unlock and
		// no passphrase is ever asked for. Say what to do instead.
		return nil, fmt.Errorf("%sprivate key %s is a PuTTY .ppk file, which GoTerm cannot read — open it in PuTTYgen and use Conversions → Export OpenSSH key, then point GoTerm at the exported file", label, path)
	}
	if strings.EqualFold(filepath.Ext(path), ".pub") {
		// id_rsa and id_rsa.pub sit side by side in the file picker, and picking the
		// public half otherwise surfaces as a baffling parse error or a refused login.
		return nil, fmt.Errorf("%s%s is a public key file — select the private key next to it (usually the same name without .pub)", label, filepath.Base(path))
	}

	var raw any
	if passphrase != "" {
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase(data, []byte(passphrase))
	} else {
		raw, err = ssh.ParseRawPrivateKey(data)
	}
	if err != nil {
		if isPassphraseError(err) {
			if passphrase == "" {
				return nil, fmt.Errorf("%w%sprivate key %s is encrypted", ErrPassphraseRequired, promptScope(label), path)
			}
			return nil, fmt.Errorf("%w%swrong passphrase for private key %s", ErrPassphraseRequired, promptScope(label), path)
		}
		return nil, fmt.Errorf("parse %sprivate key %s: %w", label, path, err)
	}

	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%sprivate key %s unusable: %w", label, path, err)
	}
	return signer, nil
}

// isPassphraseError recognises "this key needs a passphrase".
//
// x/crypto returns a typed error for the common formats, but the message
// heuristic stays because PEM, PKCS#8 and OpenSSH keys surface it differently
// and none of those errors are exported.
func isPassphraseError(err error) bool {
	if err == nil {
		return false
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return true
	}
	msg := strings.ToLower(err.Error())
	// Wrong passphrases surface as x509/"decryption password incorrect", OpenSSH
	// "failed to decrypt", or PKCS#8 padding errors — none of them typed.
	return strings.Contains(msg, "password") || strings.Contains(msg, "passphrase") ||
		strings.Contains(msg, "encrypted") || strings.Contains(msg, "decrypt") ||
		strings.Contains(msg, "incorrect") || strings.Contains(msg, "padding")
}

func shortName(path string) string { return filepath.Base(path) }

// promptScope marks a passphrase request as belonging to the jump host.
//
// It is a marker for the frontend, not prose: "passphrase required (proxy): ..."
// is the only thing that says *which* key to ask about. The key's path is no
// proof — keys get named "proxy.pem" plenty, and that word used to be the hint,
// which sent the target's passphrase to the jump host and back again.
func promptScope(label string) string {
	if strings.TrimSpace(label) == "" {
		return ": "
	}
	return " (" + strings.TrimSpace(label) + "): "
}

// isPuttyKey reports whether this is a PuTTY/WinSCP/Xshell key file.
func isPuttyKey(path string, data []byte) bool {
	if strings.EqualFold(filepath.Ext(path), ".ppk") {
		return true
	}
	return bytes.HasPrefix(bytes.TrimSpace(data), []byte("P ssh2-privtree-file"))
}
