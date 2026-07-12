package tests

import (
	"context"
	"encoding/json"
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
