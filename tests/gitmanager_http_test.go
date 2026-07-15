package tests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/gitmanager"
)

func newTestMux() (*http.ServeMux, *fakeSecretStore) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: false})
	mux := http.NewServeMux()
	gitmanager.RegisterRoutes(mux, manager)
	return mux, store
}

type failingSecretStore struct{}

func (failingSecretStore) Put(ctx context.Context, ref, value string) error {
	return errors.New("secret store unavailable")
}

func (failingSecretStore) Get(ctx context.Context, ref string) (string, error) {
	return "", errors.New("secret store unavailable")
}

func newFailingStoreTestMux() *http.ServeMux {
	manager := gitmanager.NewCredentialManager(failingSecretStore{}, &fakeValidator{shouldFail: false})
	mux := http.NewServeMux()
	gitmanager.RegisterRoutes(mux, manager)
	return mux
}

func postJSON(t *testing.T, mux *http.ServeMux, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRegisterPATHandler_ReturnsVerifiedCredential(t *testing.T) {
	mux, _ := newTestMux()

	rec := postJSON(t, mux, "/git-credentials/pat", map[string]string{
		"ownerUserId": "user-1",
		"alias":       "GitHub Token",
		"domain":      "github.com",
		"token":       "ghp_token",
	})

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Credential struct {
			Status string `json:"status"`
			Kind   string `json:"kind"`
		} `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "verified", resp.Credential.Status)
	require.Equal(t, "personal-access-token", resp.Credential.Kind)
}

func TestGenerateSSHKeyHandler_ReturnsPublicKey(t *testing.T) {
	mux, _ := newTestMux()

	rec := postJSON(t, mux, "/git-credentials/ssh-key/generate", map[string]string{
		"ownerUserId": "user-1",
		"alias":       "My Key",
		"domain":      "github.com",
	})

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		PublicKey string `json:"publicKey"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp.PublicKey, "ssh-ed25519")
}

func TestRegisterGitHubDerivedHandler_ReturnsFixedAliasAndDomain(t *testing.T) {
	mux, _ := newTestMux()

	rec := postJSON(t, mux, "/git-credentials/github-derived", map[string]string{
		"ownerUserId": "user-1",
		"accessToken": "gho_token",
	})

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Credential gitmanager.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "GitHub", resp.Credential.Alias)
	require.Equal(t, "github.com", resp.Credential.Domain)
	require.Equal(t, gitmanager.CredentialKindGitHubOAuthDerived, resp.Credential.Kind)
	require.Equal(t, gitmanager.CredentialStatusVerified, resp.Credential.Status)
}

func TestReauthenticateHandler_RestoresVerifiedStatus(t *testing.T) {
	mux, store := newTestMux()

	registerRec := postJSON(t, mux, "/git-credentials/pat", map[string]string{
		"ownerUserId": "user-1",
		"alias":       "My Token",
		"domain":      "github.com",
		"token":       "old-token",
	})
	var registerResp struct {
		Credential gitmanager.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(registerRec.Body.Bytes(), &registerResp))
	gitmanager.Revoke(&registerResp.Credential)

	rec := postJSON(t, mux, "/git-credentials/reauthenticate", map[string]any{
		"credential": registerResp.Credential,
		"newSecret":  "new-token",
	})

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Credential gitmanager.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, gitmanager.CredentialStatusVerified, resp.Credential.Status)
	storedValue, err := store.Get(context.Background(), resp.Credential.SecretRef)
	require.NoError(t, err)
	require.Equal(t, "new-token", storedValue)
}

func TestRegisterPATHandler_InvalidBody_ReturnsBadRequest(t *testing.T) {
	mux, _ := newTestMux()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, "/git-credentials/pat", strings.NewReader("not json"),
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestImportSSHKeyHandler_ValidKey_ReturnsCredential(t *testing.T) {
	mux, _ := newTestMux()
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	keygenCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath, "-q")
	require.NoError(t, keygenCmd.Run())
	privateKeyPEM, err := os.ReadFile(keyPath)
	require.NoError(t, err)

	rec := postJSON(t, mux, "/git-credentials/ssh-key/import", map[string]string{
		"ownerUserId": "user-1", "alias": "Imported Key", "domain": "github.com",
		"privateKeyPem": string(privateKeyPEM),
	})

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Credential gitmanager.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, gitmanager.CredentialKindSSHKey, resp.Credential.Kind)
}

// Exercises writeError (via importSSHKeyHandler rejecting an invalid key).
func TestImportSSHKeyHandler_InvalidKey_ReturnsUnprocessableEntity(t *testing.T) {
	mux, _ := newTestMux()

	rec := postJSON(t, mux, "/git-credentials/ssh-key/import", map[string]string{
		"ownerUserId": "user-1", "alias": "Bad Key", "domain": "github.com",
		"privateKeyPem": "not a real key",
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestRegisterPATHandler_SecretStoreFails_ReturnsUnprocessableEntity(t *testing.T) {
	mux := newFailingStoreTestMux()

	rec := postJSON(t, mux, "/git-credentials/pat", map[string]string{
		"ownerUserId": "user-1", "alias": "GitHub Token", "domain": "github.com", "token": "ghp_token",
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestGenerateSSHKeyHandler_SecretStoreFails_ReturnsUnprocessableEntity(t *testing.T) {
	mux := newFailingStoreTestMux()

	rec := postJSON(t, mux, "/git-credentials/ssh-key/generate", map[string]string{
		"ownerUserId": "user-1", "alias": "My Key", "domain": "github.com",
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestRegisterGitHubDerivedHandler_SecretStoreFails_ReturnsUnprocessableEntity(t *testing.T) {
	mux := newFailingStoreTestMux()

	rec := postJSON(t, mux, "/git-credentials/github-derived", map[string]string{
		"ownerUserId": "user-1", "accessToken": "gho_token",
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// FR-020: reauthenticating with a credential that still fails validation
// reports an error rather than silently marking it verified.
func TestReauthenticateHandler_StillInvalid_ReturnsUnprocessableEntity(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: true})
	mux := http.NewServeMux()
	gitmanager.RegisterRoutes(mux, manager)

	rec := postJSON(t, mux, "/git-credentials/reauthenticate", map[string]any{
		"credential": gitmanager.Credential{
			ID: "cred-1", OwnerUserID: "user-1", Alias: "Token", Domain: "github.com",
			Kind: gitmanager.CredentialKindPAT, Status: gitmanager.CredentialStatusRevoked, SecretRef: "ref/1",
		},
		"newSecret": "still-bad-token",
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// The second step of the generate-then-verify flow: GenerateSSHKey never
// validates (there's nothing to check yet), so the credential comes back
// `unverified` — Verify is what the Desarrollador triggers after actually
// adding the public key to their provider.
func TestVerifyHandler_Succeeds_MarksVerified(t *testing.T) {
	mux, _ := newTestMux()

	generateRec := postJSON(t, mux, "/git-credentials/ssh-key/generate", map[string]string{
		"ownerUserId": "user-1", "alias": "My Key", "domain": "github.com",
	})
	var generateResp struct {
		Credential gitmanager.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(generateRec.Body.Bytes(), &generateResp))
	require.Equal(t, gitmanager.CredentialStatusUnverified, generateResp.Credential.Status)

	rec := postJSON(t, mux, "/git-credentials/verify", map[string]any{
		"credential": generateResp.Credential,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Credential gitmanager.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, gitmanager.CredentialStatusVerified, resp.Credential.Status)
}

func TestVerifyHandler_StillRejected_ReturnsUnprocessableEntity(t *testing.T) {
	store := newFakeSecretStore()
	ref := "codeeditor/git-credentials/user-1/cred-1"
	store.values[ref] = "some-private-key-pem"
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: true})
	mux := http.NewServeMux()
	gitmanager.RegisterRoutes(mux, manager)

	rec := postJSON(t, mux, "/git-credentials/verify", map[string]any{
		"credential": gitmanager.Credential{
			ID: "cred-1", OwnerUserID: "user-1", Alias: "My Key", Domain: "github.com",
			Kind: gitmanager.CredentialKindSSHKey, Status: gitmanager.CredentialStatusUnverified, SecretRef: ref,
		},
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestVerifyHandler_InvalidBody_ReturnsBadRequest(t *testing.T) {
	mux, _ := newTestMux()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, "/git-credentials/verify", strings.NewReader("not json"),
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestReauthenticateHandler_InvalidBody_ReturnsBadRequest(t *testing.T) {
	mux, _ := newTestMux()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, "/git-credentials/reauthenticate", strings.NewReader("not json"),
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
