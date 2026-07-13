package gitmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPatValidationRequest_KnownProviders(t *testing.T) {
	cases := []struct {
		domain       string
		wantURL      string
		wantAuthHead string
	}{
		{"github.com", "https://api.github.com/user", "Bearer tok"},
		{"bitbucket.org", "https://api.bitbucket.org/2.0/user", "Bearer tok"},
		{"gitlab.example.com", "https://gitlab.example.com/api/v4/user", "Bearer tok"},
	}
	for _, tc := range cases {
		url, authHeader := patValidationRequest(tc.domain, "tok")
		if url != tc.wantURL {
			t.Errorf("%s: got url %q, want %q", tc.domain, url, tc.wantURL)
		}
		if authHeader != tc.wantAuthHead {
			t.Errorf("%s: got authHeader %q, want %q", tc.domain, authHeader, tc.wantAuthHead)
		}
	}
}

// patValidationRequest always builds an https:// URL, so these tests use
// a TLS test server (with its self-signed cert trusted via
// server.Client()) rather than httptest.NewServer's plain HTTP.

func TestProviderValidator_ValidatePAT_Success(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	validator := &ProviderValidator{HTTPClient: server.Client()}
	domain := strings.TrimPrefix(server.URL, "https://")
	err := validator.Validate(context.Background(), domain, CredentialKindPAT, "good-token")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestProviderValidator_ValidatePAT_Rejected(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	validator := &ProviderValidator{HTTPClient: server.Client()}
	domain := strings.TrimPrefix(server.URL, "https://")
	err := validator.Validate(context.Background(), domain, CredentialKindPAT, "bad-token")

	if err == nil {
		t.Fatal("expected an error for a rejected token")
	}
}

// GitHub OAuth-derived tokens validate through the same PAT path (both
// are Bearer tokens against /user).
func TestProviderValidator_ValidateGitHubOAuthDerived_UsesPATPath(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	validator := &ProviderValidator{HTTPClient: server.Client()}
	domain := strings.TrimPrefix(server.URL, "https://")
	err := validator.Validate(context.Background(), domain, CredentialKindGitHubOAuthDerived, "gho_token")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// Documents the validator's own known limitation (see the comment on
// validateSSHReachability): `ssh` exits non-zero even for a DNS
// resolution failure, and any *exec.ExitError reads as "the host
// responded" — so this genuinely returns no error here, not because the
// host is reachable, but because the heuristic can't tell the difference.
func TestProviderValidator_ValidateSSHReachability_UnresolvableHost_TreatedAsReachable(t *testing.T) {
	validator := NewProviderValidator()
	err := validator.Validate(context.Background(), "this-host-does-not-exist.invalid", CredentialKindSSHKey, "")

	if err != nil {
		t.Fatalf("expected no error (known heuristic limitation), got %v", err)
	}
}

func TestProviderValidator_Validate_UnknownKind_ReturnsError(t *testing.T) {
	validator := NewProviderValidator()
	err := validator.Validate(context.Background(), "github.com", CredentialKind("unknown"), "secret")

	if err == nil {
		t.Fatal("expected an error for an unknown credential kind")
	}
}
