package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpod"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/entitlements"
)

func TestListProjectsHandler_ReturnsClonedAndUpProjects(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	provisioner := newProvisioner(gate, &provisionerFakes{})
	lister := devpod.NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return []string{"ws-1"}, nil },
		func(string) (string, error) { return "git@github.com:acme/repo.git", nil },
		fakeListRunner{ids: []string{"ws-1"}},
	)
	mux := http.NewServeMux()
	devpod.RegisterRoutes(mux, provisioner, lister)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/projects", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Projects []devpod.ProjectInfo `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Projects, 1)
	require.Equal(t, "ws-1", body.Projects[0].WorkspaceID)
	require.Equal(t, "git@github.com:acme/repo.git", body.Projects[0].RepositoryURL)
	require.Equal(t, devpod.StatusReady, body.Projects[0].Status)
}

func TestListProjectsHandler_NoClonedWorkspaces_ReturnsEmptyList(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	provisioner := newProvisioner(gate, &provisionerFakes{})
	lister := devpod.NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return nil, nil },
		func(string) (string, error) { return "", nil },
		fakeListRunner{},
	)
	mux := http.NewServeMux()
	devpod.RegisterRoutes(mux, provisioner, lister)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/projects", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Projects []devpod.ProjectInfo `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Empty(t, body.Projects)
}
