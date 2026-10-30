// Package hostport moves saved hosts between machines.
//
// Everything about a host list is portable except the secret, which lives in
// this machine's key ring and cannot leave it. So an export bundles the hosts
// together with their secrets re-encrypted under a passphrase the user chooses,
// in a single file that only GoTerm can read back: AES-256-GCM with a scrypt
// key, tagged with a format name so a random JSON file is refused with a
// sentence instead of a stack trace.
//
// The file is deliberately opaque. It contains passwords, it may travel through
// mail or a chat window, and a format someone can skim is a format someone can
// leak.
package hostport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/ykfq/goterm/internal/hoststore"
)

const (
	// Format tags every bundle and is checked on both ends.
	Format = "goterm.hosts"
	// Version is the bundle layout this build writes and understands.
	Version = 1

	// scrypt parameters: interactive-login cost, ~64 MiB of work at N=2^15.
	// They are written into the file so a future build can raise them without
	// orphaning older exports.
	kdfN   = 1 << 15
	kdfR   = 8
	kdfP   = 1
	keyLen = 32
)

// Sentinel errors the UI turns into sentences.
var (
	// ErrWrongPassphrase means the payload did not decrypt: a wrong passphrase,
	// or a file that was edited, truncated or corrupted.
	ErrWrongPassphrase = errors.New("wrong passphrase, or the file is damaged")
	// ErrNotABundle means the file is not a GoTerm host export at all.
	ErrNotABundle = errors.New("this is not a GoTerm host export")
	// ErrNewerBundle means the file was written by a newer GoTerm.
	ErrNewerBundle = errors.New("this export was written by a newer GoTerm")
)

// Bundle is the plaintext that gets sealed: the hosts, and whatever secrets the
// exporting machine could read out of its key ring.
type Bundle struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	ExportedAt time.Time         `json:"exportedAt"`
	Hosts      []hoststore.Host  `json:"hosts"`
	Secrets    map[string]string `json:"secrets,omitempty"`
	// ProxySecrets is keyed by the same host ids; a jump host's credential is a
	// separate item, exactly as it is on disk.
	ProxySecrets map[string]string `json:"proxySecrets,omitempty"`
}

// envelope is what actually lands in the file: a readable header (so the format
// can be recognised, and a wrong passphrase can be reported as such) and an
// encrypted payload.
type envelope struct {
	Format  string     `json:"format"`
	Version int        `json:"version"`
	KDF     kdfInfo    `json:"kdf"`
	Cipher  cipherInfo `json:"cipher"`
	Payload string     `json:"payload"`
}

type kdfInfo struct {
	Algo string `json:"algo"`
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt string `json:"salt"`
}

type cipherInfo struct {
	Algo  string `json:"algo"`
	Nonce string `json:"nonce"`
}

// Seal encrypts a bundle under passphrase and returns the file contents.
func Seal(bundle Bundle, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("an export passphrase is required — it is the only thing that unlocks the file")
	}
	bundle.Format = Format
	bundle.Version = Version
	if bundle.ExportedAt.IsZero() {
		bundle.ExportedAt = time.Now().UTC()
	}
	plain, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("encode hosts: %w", err)
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	key, err := scrypt.Key([]byte(passphrase), salt, kdfN, kdfR, kdfP, keyLen)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)

	env := envelope{
		Format:  Format,
		Version: Version,
		KDF:     kdfInfo{Algo: "scrypt", N: kdfN, R: kdfR, P: kdfP, Salt: b64(salt)},
		Cipher:  cipherInfo{Algo: "aes-256-gcm", Nonce: b64(nonce)},
		Payload: b64(sealed),
	}
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Unseal opens a bundle written by Seal.
func Unseal(raw []byte, passphrase string) (Bundle, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Bundle{}, ErrNotABundle
	}
	if env.Format != Format {
		return Bundle{}, ErrNotABundle
	}
	if env.Version > Version {
		return Bundle{}, ErrNewerBundle
	}
	if env.KDF.Algo != "scrypt" || env.Cipher.Algo != "aes-256-gcm" {
		return Bundle{}, fmt.Errorf("%w: unsupported encryption (%s/%s)", ErrNotABundle, env.KDF.Algo, env.Cipher.Algo)
	}
	// Only the parameters this build writes are accepted; a file claiming
	// N=2^30 would otherwise turn one import into a memory exhaustion.
	if env.KDF.N != kdfN || env.KDF.R != kdfR || env.KDF.P != kdfP {
		return Bundle{}, fmt.Errorf("%w: unsupported scrypt parameters", ErrNotABundle)
	}
	salt, err := unb64(env.KDF.Salt)
	if err != nil {
		return Bundle{}, ErrNotABundle
	}
	nonce, err := unb64(env.Cipher.Nonce)
	if err != nil {
		return Bundle{}, ErrNotABundle
	}
	sealed, err := unb64(env.Payload)
	if err != nil {
		return Bundle{}, ErrNotABundle
	}

	key, err := scrypt.Key([]byte(passphrase), salt, kdfN, kdfR, kdfP, keyLen)
	if err != nil {
		return Bundle{}, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Bundle{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Bundle{}, err
	}
	if len(nonce) != gcm.NonceSize() {
		return Bundle{}, ErrNotABundle
	}
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return Bundle{}, ErrWrongPassphrase
	}

	var bundle Bundle
	if err := json.Unmarshal(plain, &bundle); err != nil {
		return Bundle{}, ErrWrongPassphrase
	}
	if bundle.Format != Format {
		return Bundle{}, ErrNotABundle
	}
	return bundle, nil
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
