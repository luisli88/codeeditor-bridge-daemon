// Package pairing gates every HTTP/WebSocket request this daemon serves
// behind a single, long-lived bearer token — the seam that actually binds
// "runs on a machine reachable over SSH keys" (RemoteSetupService's own
// trust bootstrap) to "only the device that did that setup can drive the
// Bridge Daemon afterward". Before this package existed, `/handshake` and
// the main WebSocket upgrade had no auth check at all: any client that
// could complete the TLS handshake (trivial for self-hosted, since the app
// itself is configured to trust its self-signed certificate) got full
// shell/filesystem/git/run access, with no notion of "which device set
// this up" whatsoever.
package pairing

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// tokenByteLength is 32 random bytes (256 bits) hex-encoded to a 64-
// character token — comfortably beyond brute-force range, short enough to
// still be copy/paste-able for the manual (non-RemoteSetupView) pairing
// path.
const tokenByteLength = 32

// LoadOrCreateToken reads the pairing token at path, generating a new
// random one and persisting it (0600 — owner-readable only) if the file
// doesn't exist yet. Self-sufficient rather than requiring some other
// process to have written this file first: a `bridged` started by hand
// (bridge-daemon/README.md's manual self-hosted flow, not through
// RemoteSetupView) still ends up with a real token an operator can relay
// to the app, and a restart reuses the same one already-paired devices
// have rather than invalidating them.
func LoadOrCreateToken(path string) (string, error) {
	existing, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(existing))
		if token == "" {
			return "", fmt.Errorf("pairing: token file %q is empty", path)
		}
		return token, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("pairing: read token file %q: %w", path, err)
	}

	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("pairing: generate token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("pairing: create token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("pairing: write token file %q: %w", path, err)
	}
	return token, nil
}

func generateToken() (string, error) {
	raw := make([]byte, tokenByteLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// Middleware rejects any request whose `Authorization: Bearer <token>`
// header doesn't match token exactly, comparing in constant time
// (crypto/subtle) — a bearer secret shouldn't be checked with `==`, which
// leaks how many leading bytes matched through response timing. Applied
// to the whole mux in cmd/bridged/main.go (every route, `/handshake`
// included — there is no unauthenticated surface once a token exists),
// not per-route: a single enforcement point that can't be forgotten when
// a new route is added later.
func Middleware(token string, next http.Handler) http.Handler {
	const prefix = "Bearer "
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		presented := auth[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
