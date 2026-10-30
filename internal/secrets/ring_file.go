package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ykfq/goterm/internal/atomicfile"
)

// fileRing seals the whole secret map into one AES-256-GCM file, with the key
// in a sibling 0600 file.
//
// Splitting key and ciphertext is not a security boundary against an attacker
// who already runs as you — nothing on a single-user desktop is — but it does
// mean hosts.json and any casual backup of it carry no passwords.
type fileRing struct {
	path    string
	keyPath string
	key     []byte
}

func newFileRing(path, keyPath string) (*fileRing, error) {
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	return &fileRing{path: path, keyPath: keyPath, key: key}, nil
}

func (r *fileRing) backend() Backend { return BackendSealedFile }

func loadOrCreateKey(keyPath string) ([]byte, error) {
	existing, err := os.ReadFile(keyPath)
	if err == nil {
		key, decErr := base64.StdEncoding.DecodeString(string(existing))
		if decErr == nil && len(key) == 32 {
			return key, nil
		}
		// A corrupt key would make every stored secret unreadable forever, so
		// keep it and start a new one rather than destroying evidence.
		quarantine := keyPath + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
		_ = os.Rename(keyPath, quarantine)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", keyPath, err)
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := atomicfile.Write(keyPath, []byte(encoded), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (r *fileRing) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(r.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (r *fileRing) load() (map[string]string, error) {
	sealed, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", r.path, err)
	}
	aead, err := r.aead()
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(string(sealed))
	if err != nil {
		return nil, r.quarantine(fmt.Errorf("%s is not a sealed file", r.path))
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, r.quarantine(fmt.Errorf("%s is truncated", r.path))
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		// Wrong key (restored backup, reinstalled system, replaced master.key).
		return nil, r.quarantine(fmt.Errorf("%s could not be opened with %s", r.path, filepath.Base(r.keyPath)))
	}
	items := map[string]string{}
	if err := json.Unmarshal(plain, &items); err != nil {
		return nil, r.quarantine(fmt.Errorf("%s is corrupt: %w", r.path, err))
	}
	return items, nil
}

// quarantine keeps an unreadable vault on disk under a new name instead of
// silently starting empty: the user can still recover it by restoring the old
// master.key.
func (r *fileRing) quarantine(problem error) error {
	stamped := r.path + ".unreadable-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(r.path, stamped); err != nil {
		return fmt.Errorf("%w (and it could not be set aside: %v)", problem, err)
	}
	return fmt.Errorf("%w — the old file was kept as %s", problem, filepath.Base(stamped))
}

func (r *fileRing) save(items map[string]string) error {
	plain, err := json.Marshal(items)
	if err != nil {
		return err
	}
	aead, err := r.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, plain, nil)
	encoded := base64.StdEncoding.EncodeToString(sealed)
	return atomicfile.Write(r.path, []byte(encoded), 0o600)
}

func (r *fileRing) Get(key string) (string, error) {
	items, err := r.load()
	if err != nil {
		return "", err
	}
	value, ok := items[key]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (r *fileRing) Put(key, value string) error {
	items, err := r.load()
	if err != nil {
		return err
	}
	items[key] = value
	return r.save(items)
}

func (r *fileRing) Delete(key string) error {
	items, err := r.load()
	if err != nil {
		return err
	}
	if _, ok := items[key]; !ok {
		return nil
	}
	delete(items, key)
	return r.save(items)
}

func (r *fileRing) Clear() error {
	err := os.Remove(r.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
