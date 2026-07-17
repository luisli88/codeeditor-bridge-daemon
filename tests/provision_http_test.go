package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpod"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/entitlements"
)

func newProvisionTestMux(fakes *provisionerFakes) *http.ServeMux {
	gate := entitlements.NewGate(allowAllStore{})
	provisioner := newProvisioner(gate, fakes)
	mux := http.NewServeMux()
	devpod.RegisterRoutes(mux, provisioner)
	return mux
}

func TestProvisionHandler_AllStepsSucceed_ReturnsReady(t *testing.T) {
	mux := newProvisionTestMux(&provisionerFakes{})

	body, err := json.Marshal(devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/projects/provision", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var result devpod.Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Equal(t, devpod.StatusReady, result.Status)
}

func TestRetryStepHandler_RetriesOnlyRequestedStep(t *testing.T) {
	fakes := &provisionerFakes{devPodUpShouldFail: true}
	mux := newProvisionTestMux(fakes)

	provisionBody, err := json.Marshal(devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})
	require.NoError(t, err)
	provisionReq := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, "/projects/provision", strings.NewReader(string(provisionBody)),
	)
	provisionRec := httptest.NewRecorder()
	mux.ServeHTTP(provisionRec, provisionReq)
	var result devpod.Result
	require.NoError(t, json.Unmarshal(provisionRec.Body.Bytes(), &result))
	require.Equal(t, devpod.StatusError, result.Status)
	require.Equal(t, 1, fakes.cloneCalls)

	fakes.devPodUpShouldFail = false
	retryBody, err := json.Marshal(map[string]any{
		"request": devpod.Request{
			WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
		},
		"result": result,
		"step":   devpod.StepDevPodUp,
	})
	require.NoError(t, err)
	retryReq := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, "/projects/retry-step", strings.NewReader(string(retryBody)),
	)
	retryRec := httptest.NewRecorder()
	mux.ServeHTTP(retryRec, retryReq)

	require.Equal(t, http.StatusOK, retryRec.Code)
	var retried devpod.Result
	require.NoError(t, json.Unmarshal(retryRec.Body.Bytes(), &retried))
	require.Equal(t, devpod.StatusReady, retried.Status)
	require.Equal(t, 1, fakes.cloneCalls, "retry must not re-clone")
	require.Equal(t, 2, fakes.devPodUpCalls)
}

func TestDeprovisionHandler_Succeeds_ReturnsOK(t *testing.T) {
	fakes := &provisionerFakes{}
	mux := newProvisionTestMux(fakes)

	body, err := json.Marshal(map[string]string{"workspaceId": "ws-1"})
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/projects/deprovision", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, fakes.devPodDeleteCalls)
	require.Equal(t, "ws-1", fakes.devPodDeleteWorkspace)
	require.Equal(t, 1, fakes.deleteCloneCalls)
}

func TestDeprovisionHandler_TeardownFails_ReturnsServerError(t *testing.T) {
	fakes := &provisionerFakes{devPodDeleteShouldFail: true, deleteCloneShouldFail: true}
	mux := newProvisionTestMux(fakes)

	body, err := json.Marshal(map[string]string{"workspaceId": "ws-1"})
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/projects/deprovision", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
