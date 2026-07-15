package tests

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/filetree"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

func newFileTreeTestServer(t *testing.T, browser *filetree.Browser) (*websocket.Conn, func()) {
	t.Helper()
	srv := ws.NewServer()
	srv.Handle(ws.ChannelFS, browser.Handler())
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

func sendFileTreeRequest(t *testing.T, c *websocket.Conn, workspaceID string, req filetree.Request) ws.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, err := json.Marshal(req)
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelFS, WorkspaceID: &workspaceID, Payload: payload,
	}))

	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	return env
}

func TestFileTreeHandler_List_ReturnsRealEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "hello\n")
	browser := filetree.NewBrowser(func(workspaceID string) string { return dir })
	c, cleanup := newFileTreeTestServer(t, browser)
	defer cleanup()

	env := sendFileTreeRequest(t, c, "ws-1", filetree.Request{Action: "list"})

	require.Nil(t, env.Error, "unexpected error: %+v", env.Error)
	var resp filetree.Response
	require.NoError(t, json.Unmarshal(env.Payload, &resp))
	require.Equal(t, "list", resp.Action)
	require.Len(t, resp.Entries, 1)
	require.Equal(t, "README.md", resp.Entries[0].Name)
}

func TestFileTreeHandler_Read_ReturnsRealContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "hello\n")
	browser := filetree.NewBrowser(func(workspaceID string) string { return dir })
	c, cleanup := newFileTreeTestServer(t, browser)
	defer cleanup()

	env := sendFileTreeRequest(t, c, "ws-1", filetree.Request{Action: "read", Path: "README.md"})

	require.Nil(t, env.Error, "unexpected error: %+v", env.Error)
	var resp filetree.Response
	require.NoError(t, json.Unmarshal(env.Payload, &resp))
	require.Equal(t, "read", resp.Action)
	require.Equal(t, "hello\n", resp.Content)
}

func TestFileTreeHandler_NoWorkspaceID_ReturnsError(t *testing.T) {
	browser := filetree.NewBrowser(func(workspaceID string) string { return t.TempDir() })
	srv := ws.NewServer()
	srv.Handle(ws.ChannelFS, browser.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelFS}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
	require.Equal(t, "workspace-required", env.Error.Code)
}

func TestFileTreeHandler_MalformedPayload_ReturnsError(t *testing.T) {
	browser := filetree.NewBrowser(func(workspaceID string) string { return t.TempDir() })
	c, cleanup := newFileTreeTestServer(t, browser)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workspaceID := "ws-1"
	// Valid JSON syntax (so it survives wsjson.Write's own marshal of the
	// outer envelope, which validates any embedded json.RawMessage) but the
	// wrong shape for filetree.Request{Action, Path} — an array instead of
	// an object — so the handler's own json.Unmarshal fails server-side.
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelFS, WorkspaceID: &workspaceID, Payload: json.RawMessage(`[1,2,3]`),
	}))
	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
	require.Equal(t, "invalid-payload", env.Error.Code)
}

func TestFileTreeHandler_UnknownAction_ReturnsError(t *testing.T) {
	browser := filetree.NewBrowser(func(workspaceID string) string { return t.TempDir() })
	c, cleanup := newFileTreeTestServer(t, browser)
	defer cleanup()

	env := sendFileTreeRequest(t, c, "ws-1", filetree.Request{Action: "delete"})

	require.NotNil(t, env.Error)
	require.Equal(t, "unknown-action", env.Error.Code)
}

func TestFileTreeHandler_PathEscapesWorkspace_ReturnsError(t *testing.T) {
	browser := filetree.NewBrowser(func(workspaceID string) string { return t.TempDir() })
	c, cleanup := newFileTreeTestServer(t, browser)
	defer cleanup()

	env := sendFileTreeRequest(t, c, "ws-1", filetree.Request{Action: "read", Path: "../../etc/passwd"})

	require.NotNil(t, env.Error)
	require.Equal(t, "fs-invalid-path", env.Error.Code)
}
