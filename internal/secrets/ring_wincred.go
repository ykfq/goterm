//go:build windows

package secrets

import (
	"errors"
	"fmt"
	"strings"

	"github.com/danieljoos/wincred"
)

// wincredRing keeps secrets in the Windows Credential Manager as generic
// credentials named "github.com/ykfq/goterm/<key>".
//
// This is the same store git-for-windows and VS Code use. It is reached
// through advapi32 rather than cmdkey.exe on purpose: shelling out pops a
// console window on every read, which looks broken in a desktop app.
type wincredRing struct{}

func newOSRing(service string) (ring, error) {
	r := &wincredRing{}
	// Probe before committing: group policy can disable Credential Manager, and
	// a failure discovered on the first saved password is a bad time to learn
	// it. The probe writes, reads back, and deletes a throwaway entry.
	probe := service + "/__probe__"
	if err := r.writeRaw(probe, "probe"); err != nil {
		return nil, fmt.Errorf("credential manager unavailable: %w", err)
	}
	got, err := r.get(probe)
	if err != nil || got != "probe" {
		_ = r.Delete(probe)
		return nil, fmt.Errorf("credential manager round-trip failed: %v", err)
	}
	if err := r.Delete(probe); err != nil {
		return nil, fmt.Errorf("credential manager cleanup failed: %w", err)
	}
	return r, nil
}

func (r *wincredRing) backend() Backend { return BackendCredentialManager }

func targetName(key string) string {
	return serviceName + "/" + strings.ReplaceAll(key, string([]byte{0}), " ")
}

func (r *wincredRing) writeRaw(target, value string) error {
	cred := wincred.NewGenericCredential(target)
	cred.UserName = serviceName
	cred.CredentialBlob = []byte(value)
	cred.Comment = "GoTerm SSH secret"
	cred.Persist = wincred.PersistLocalMachine
	if err := cred.Write(); err != nil {
		return err
	}
	return nil
}

func (r *wincredRing) Get(key string) (string, error) {
	return r.get(targetName(key))
}

func (r *wincredRing) get(target string) (string, error) {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	if cred == nil {
		return "", ErrNotFound
	}
	return string(cred.CredentialBlob), nil
}

func (r *wincredRing) Put(key, value string) error {
	return r.writeRaw(targetName(key), value)
}

func (r *wincredRing) Delete(key string) error {
	target := targetName(key)
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil
		}
		return err
	}
	if cred == nil {
		return nil
	}
	return cred.Delete()
}

// Clear removes every credential GoTerm owns, and only those.
func (r *wincredRing) Clear() error {
	creds, err := wincred.FilteredList(serviceName + "/")
	if err != nil {
		return err
	}
	var firstErr error
	for _, c := range creds {
		if !strings.HasPrefix(c.TargetName, serviceName+"/") {
			continue
		}
		cred, err := wincred.GetGenericCredential(c.TargetName)
		if err != nil || cred == nil {
			continue
		}
		if err := cred.Delete(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
