package gitmanager

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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

func TestProviderValidator_ValidateSSHKey_UnresolvableHost_Fails(t *testing.T) {
	validator := NewProviderValidator()
	err := validator.Validate(
		context.Background(), "this-host-does-not-exist.invalid", CredentialKindSSHKey, testSSHPrivateKeyPEM(t),
	)

	if err == nil {
		t.Fatal("expected an error for an unresolvable host")
	}
}

func TestProviderValidator_ValidateSSHKey_MalformedKey_Fails(t *testing.T) {
	validator := NewProviderValidator()
	err := validator.Validate(context.Background(), "github.com", CredentialKindSSHKey, "not a real key")

	if err == nil {
		t.Fatal("expected an error for a malformed private key")
	}
}

// The provider's SSH server always completes the transport handshake
// regardless of which key (if any) is offered — a bare reachability check
// passes for *any* key against *any* running SSH server, which is exactly
// the bug this test guards against: only a key the fake server's
// PublicKeyCallback actually authorizes should validate successfully.
func TestProviderValidator_ValidateSSHKey_AuthorizedKey_Succeeds(t *testing.T) {
	authorizedSigner, authorizedPEM := testGenerateSSHKeyPair(t)
	addr := startFakeSSHServer(t, authorizedSigner.PublicKey())

	validator := NewProviderValidator()
	validator.SSHPort = portOf(t, addr)
	err := validator.Validate(context.Background(), hostOf(t, addr), CredentialKindSSHKey, authorizedPEM)

	if err != nil {
		t.Fatalf("expected the authorized key to validate, got %v", err)
	}
}

func TestProviderValidator_ValidateSSHKey_UnauthorizedKey_Fails(t *testing.T) {
	authorizedSigner, _ := testGenerateSSHKeyPair(t)
	_, unauthorizedPEM := testGenerateSSHKeyPair(t)
	addr := startFakeSSHServer(t, authorizedSigner.PublicKey())

	validator := NewProviderValidator()
	validator.SSHPort = portOf(t, addr)
	err := validator.Validate(context.Background(), hostOf(t, addr), CredentialKindSSHKey, unauthorizedPEM)

	if err == nil {
		t.Fatal("expected an error for a key the fake provider never authorized")
	}
}

// startFakeSSHServer stands in for a real git provider's SSH endpoint —
// it accepts a connection only from authorizedKey (mirroring "this key is
// registered on the account"), rejecting every other key, and never
// offers any channel (real providers don't give git@ connections a shell
// either).
func startFakeSSHServer(t *testing.T, authorizedKey ssh.PublicKey) string {
	t.Helper()

	hostSigner, _ := testGenerateSSHKeyPair(t)
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorizedKey.Marshal()) {
				return nil, nil
			}
			return nil, errUnauthorizedTestKey
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return // auth rejected — the client sees that as the Dial error
				}
				defer sshConn.Close() //nolint:errcheck
				go ssh.DiscardRequests(reqs)
				for newChannel := range chans {
					_ = newChannel.Reject(ssh.Prohibited, "no channels in this fake")
				}
			}()
		}
	}()

	return listener.Addr().String()
}

var errUnauthorizedTestKey = errors.New("unauthorized key")

func testGenerateSSHKeyPair(t *testing.T) (signer ssh.Signer, privateKeyPEM string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	signer, err = ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer from key: %v", err)
	}
	pemBytes, err := marshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return signer, pemBytes
}

func testSSHPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	_, pem := testGenerateSSHKeyPair(t)
	return pem
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port %q: %v", addr, err)
	}
	return port
}

func hostOf(t *testing.T, addr string) string {
	t.Helper()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port %q: %v", addr, err)
	}
	return host
}

func TestProviderValidator_Validate_UnknownKind_ReturnsError(t *testing.T) {
	validator := NewProviderValidator()
	err := validator.Validate(context.Background(), "github.com", CredentialKind("unknown"), "secret")

	if err == nil {
		t.Fatal("expected an error for an unknown credential kind")
	}
}
