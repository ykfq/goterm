//go:build !windows

package secrets

import "errors"

// newOSRing has no dependency-free implementation on this platform: reaching
// Secret Service (Linux) or the Keychain (macOS) needs D-Bus or a subprocess,
// and a half-implemented key ring is worse than an honest file. Open() therefore
// falls back to the sealed file and the UI says so.
func newOSRing(string) (ring, error) {
	return nil, errors.New("no OS key ring integration on this platform")
}
