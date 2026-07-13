package tests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/gitmanager"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

func newGitOperationTestServer(t *testing.T, ops *gitmanager.Operations) (*websocket.Conn, func()) {
	t.Helper()
	srv := ws.NewServer()
	srv.Handle(ws.ChannelGit, ops.Handler(func(ctx context.Context, secretRef string) (string, error) {
		return "resolved-secret", nil
	}))
	ts := httptest.NewServer(srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)

	return c, func() {
		cancel()
		_ = c.CloseNow()
		ts.Close()
	}
}

func sendGitRequest(t *testing.T, c *websocket.Conn, workspaceID string, req gitmanager.GitOperationRequest) gitmanager.GitOperationResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, err := json.Marshal(req)
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload,
	}))

	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.Nil(t, env.Error, "unexpected error: %+v", env.Error)

	var resp gitmanager.GitOperationResponse
	require.NoError(t, json.Unmarshal(env.Payload, &resp))
	return resp
}

func TestGitOperationHandler_Status_ReflectsRealRepo(t *testing.T) {
	_, dir := newTestRepo(t)
	writeFile(t, dir, "new.txt", "content\n")
	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	resp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{Phase: "status"})

	require.Equal(t, "status", resp.Phase)
	require.Len(t, resp.Files, 1)
	require.Equal(t, "untracked", resp.Files[0].Status)
}

func TestGitOperationHandler_StageAndCommit(t *testing.T) {
	_, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "content\n")
	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	stageResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{Phase: "stage", Paths: []string{"file.txt"}})
	require.Equal(t, "stage", stageResp.Phase)

	commitResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{Phase: "commit", Message: "add file"})
	require.Equal(t, "commit", commitResp.Phase)
	require.Len(t, commitResp.Hash, 40)
}

func TestGitOperationHandler_NoWorkspaceID_ReturnsError(t *testing.T) {
	_, dir := newTestRepo(t)
	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })
	srv := ws.NewServer()
	srv.Handle(ws.ChannelGit, ops.Handler(func(ctx context.Context, secretRef string) (string, error) { return "", nil }))
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelGit}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
	require.Equal(t, "workspace-required", env.Error.Code)
}

// FR-035: a `pull` that leaves conflicts reports `phase: "merge-conflict"`
// with the conflicted files, then resolving + staging clears them.
// A pull failure with no remote configured at all (not a merge conflict)
// surfaces as a plain error, not phase "merge-conflict".
func TestGitOperationHandler_Pull_FailsWithoutConflict_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(gitmanager.GitOperationRequest{Phase: "pull"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
}

func TestGitOperationHandler_PullConflict_ResolveThroughWebSocket(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "a.txt", "base\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "a.txt", "feature\n")
	runGit(t, dir, "commit", "-q", "-am", "feature change")
	runGit(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "main\n")
	runGit(t, dir, "commit", "-q", "-am", "main change")
	mergeCmd := exec.Command("git", "merge", "feature")
	mergeCmd.Dir = dir
	_ = mergeCmd.Run()

	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	listResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{Phase: "merge-conflict", ConflictAction: "list"})
	require.Equal(t, "merge-conflict", listResp.Phase)
	require.Len(t, listResp.ConflictedFiles, 1)

	resolveResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{
		Phase: "merge-conflict", ConflictAction: "resolve", Path: "a.txt",
		ConflictBlockIndex: 0, ConflictResolution: "mine",
	})
	require.Equal(t, "merge-conflict", resolveResp.Phase)

	stageResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{
		Phase: "merge-conflict", ConflictAction: "stage", Path: "a.txt",
	})
	require.Empty(t, stageResp.ConflictedFiles)
}

// FR-033: push over the WebSocket, authenticated via a resolved credential.
func TestGitOperationHandler_Push_ResolvesCredentialAndPublishesCommit(t *testing.T) {
	central := t.TempDir()
	runGit(t, central, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	runGit(t, dir, "clone", "-q", central, ".")
	runGit(t, dir, "config", "user.email", "dev@example.com")
	runGit(t, dir, "config", "user.name", "Dev")
	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })
	writeFile(t, dir, "file.txt", "content\n")
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	var resolvedRefs []string
	srv := ws.NewServer()
	srv.Handle(ws.ChannelGit, ops.Handler(func(ctx context.Context, secretRef string) (string, error) {
		resolvedRefs = append(resolvedRefs, secretRef)
		return "unused-for-local-remote", nil
	}))
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	resp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{
		Phase: "push", CredentialSecretRef: "ref/1", CredentialKind: "personal-access-token",
	})

	require.Equal(t, "push", resp.Phase)
	require.Equal(t, []string{"ref/1"}, resolvedRefs)
	logOut := runGit(t, central, "log", "--oneline")
	require.Contains(t, logOut, "initial")
}

// FR-032: a clean pull (no conflicts) reports phase "pull".
func TestGitOperationHandler_Pull_NoConflict_ReportsPullPhase(t *testing.T) {
	central := t.TempDir()
	runGit(t, central, "init", "-q", "--bare", "-b", "main")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", "-q", central, ".")
	runGit(t, seedDir, "config", "user.email", "dev@example.com")
	runGit(t, seedDir, "config", "user.name", "Dev")
	writeFile(t, seedDir, "shared.txt", "v1\n")
	runGit(t, seedDir, "add", ".")
	runGit(t, seedDir, "commit", "-q", "-m", "shared v1")
	runGit(t, seedDir, "push", "-q", "origin", "main")

	dir := t.TempDir()
	runGit(t, dir, "clone", "-q", central, ".")
	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })

	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	resp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{Phase: "pull"})

	require.Equal(t, "pull", resp.Phase)
}

// FR-032: listing, creating, and switching branches over the WebSocket.
func TestGitOperationHandler_Branch_ListCreateSwitch(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	createResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{
		Phase: "branch", BranchAction: "create", BranchName: "feature",
	})
	require.Equal(t, "branch", createResp.Phase)
	names := branchNames(createResp.Branches)
	require.Contains(t, names, "feature")

	switchResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{
		Phase: "branch", BranchAction: "switch", BranchName: "main",
	})
	var current string
	for _, b := range switchResp.Branches {
		if b.Current {
			current = b.Name
		}
	}
	require.Equal(t, "main", current)

	listResp := sendGitRequest(t, c, "ws-1", gitmanager.GitOperationRequest{Phase: "branch", BranchAction: "list"})
	require.ElementsMatch(t, []string{"main", "feature"}, branchNames(listResp.Branches))
}

func TestGitOperationHandler_Branch_UnknownAction_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(gitmanager.GitOperationRequest{Phase: "branch", BranchAction: "rename"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
}

func TestGitOperationHandler_Branch_SwitchToNonexistentBranch_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload2, err := json.Marshal(gitmanager.GitOperationRequest{
		Phase: "branch", BranchAction: "switch", BranchName: "does-not-exist",
	})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload2,
	}))
	var env2 ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env2))
	require.NotNil(t, env2.Error)
}

func TestGitOperationHandler_MergeConflict_ResolveNonexistentBlock_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload3, err := json.Marshal(gitmanager.GitOperationRequest{
		Phase: "merge-conflict", ConflictAction: "resolve", Path: "file.txt",
		ConflictBlockIndex: 0, ConflictResolution: "mine",
	})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload3,
	}))
	var env3 ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env3))
	require.NotNil(t, env3.Error)
}

func TestGitOperationHandler_UnknownPhase_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload4, err := json.Marshal(gitmanager.GitOperationRequest{Phase: "rebase"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload4,
	}))
	var env4 ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env4))
	require.NotNil(t, env4.Error)
}

func branchNames(branches []gitmanager.Branch) []string {
	names := make([]string, len(branches))
	for i, b := range branches {
		names[i] = b.Name
	}
	return names
}

// A push with no remote configured (not a credential problem) surfaces
// git's own failure as a plain error.
func TestGitOperationHandler_Push_NoRemoteConfigured_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(gitmanager.GitOperationRequest{Phase: "push"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
}

func TestGitOperationHandler_Push_CredentialResolutionFails_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	srv := ws.NewServer()
	srv.Handle(ws.ChannelGit, ops.Handler(func(ctx context.Context, secretRef string) (string, error) {
		return "", errors.New("secret not found")
	}))
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	payload, err := json.Marshal(gitmanager.GitOperationRequest{Phase: "push", CredentialSecretRef: "ref/missing"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
}

func TestGitOperationHandler_Pull_CredentialResolutionFails_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	srv := ws.NewServer()
	srv.Handle(ws.ChannelGit, ops.Handler(func(ctx context.Context, secretRef string) (string, error) {
		return "", errors.New("secret not found")
	}))
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	payload, err := json.Marshal(gitmanager.GitOperationRequest{Phase: "pull", CredentialSecretRef: "ref/missing"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
}

func TestGitOperationHandler_MergeConflict_UnknownAction_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	c, cleanup := newGitOperationTestServer(t, ops)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(gitmanager.GitOperationRequest{Phase: "merge-conflict", ConflictAction: "delete"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelGit, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
}
