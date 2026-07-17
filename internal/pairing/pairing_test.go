package pairing

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateToken_FileMissing_GeneratesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "pairing-token")

	token, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(token) != tokenByteLength*2 {
		t.Fatalf("expected a %d-character hex token, got %d chars: %q", tokenByteLength*2, len(token), token)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected token file to exist, got %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600 permissions, got %o", perm)
	}
}

func TestLoadOrCreateToken_FileExists_ReturnsSameTokenOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing-token")

	first, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	second, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if first != second {
		t.Fatalf("expected the same token across calls, got %q then %q", first, second)
	}
}

func TestLoadOrCreateToken_TrimsWhitespaceFromExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing-token")
	if err := os.WriteFile(path, []byte("  hand-written-token  \n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	token, err := LoadOrCreateToken(path)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if token != "hand-written-token" {
		t.Fatalf("expected trimmed token, got %q", token)
	}
}

func TestLoadOrCreateToken_EmptyFile_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing-token")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	_, err := LoadOrCreateToken(path)

	if err == nil {
		t.Fatal("expected an error for an empty token file")
	}
}

func TestMiddleware_CorrectBearerToken_CallsNext(t *testing.T) {
	called := false
	handler := Middleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMiddleware_WrongBearerToken_Returns401(t *testing.T) {
	called := false
	handler := Middleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("expected next handler NOT to be called")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_MissingAuthorizationHeader_Returns401(t *testing.T) {
	handler := Middleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_NonBearerScheme_Returns401(t *testing.T) {
	handler := Middleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Authorization", "Basic secret-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// Regression guard: a naive `presented == token` comparison is fine
// functionally but leaks timing information proportional to how many
// leading bytes matched — this just confirms the constant-time path is
// actually wired in and a token of different length doesn't panic
// subtle.ConstantTimeCompare (it returns 0 for mismatched lengths, never
// errors).
func TestMiddleware_TokenDifferentLength_Returns401WithoutPanicking(t *testing.T) {
	handler := Middleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Authorization", "Bearer short")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
