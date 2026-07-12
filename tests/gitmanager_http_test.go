package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
