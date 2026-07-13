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

	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpod"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

func dialRunChannel(t *testing.T, manager *devpod.RunManager) (*websocket.Conn, func()) {
	t.Helper()
	srv := ws.NewServer()
	srv.Handle(ws.ChannelRun, manager.Handler())
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

// FR-039: stdout streams live for a console run.
func TestRunManager_StartConsole_StreamsStdout(t *testing.T) {
	manager := devpod.NewRunManager(func(workspaceID string) string { return "." }, nil)
	c, cleanup := dialRunChannel(t, manager)
	defer cleanup()

	payload, err := json.Marshal(devpod.RunPayload{
		Action: "start", Kind: devpod.RunKindConsole, Command: "echo hello",
	})
	require.NoError(t, err)
	workspaceID := "ws-1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelRun, WorkspaceID: &workspaceID, Payload: payload}))

	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	require.Nil(t, resp.Error)
	var runPayload devpod.RunPayload
	require.NoError(t, json.Unmarshal(resp.Payload, &runPayload))
	require.Equal(t, "hello", runPayload.Stdout)
}

// FR-040: a web run reports a preview URL.
func TestRunManager_StartWeb_ReportsPreviewURL(t *testing.T) {
	manager := devpod.NewRunManager(
		func(workspaceID string) string { return "." },
		func(workspaceID string) string { return "https://" + workspaceID + ".preview.codeeditor.dev" },
	)
	c, cleanup := dialRunChannel(t, manager)
	defer cleanup()

	payload, err := json.Marshal(devpod.RunPayload{Action: "start", Kind: devpod.RunKindWeb, Command: "sleep 5"})
	require.NoError(t, err)
	workspaceID := "ws-1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelRun, WorkspaceID: &workspaceID, Payload: payload}))

	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	var runPayload devpod.RunPayload
	require.NoError(t, json.Unmarshal(resp.Payload, &runPayload))
	require.Equal(t, "https://ws-1.preview.codeeditor.dev", runPayload.PreviewURL)
}

// FR-045: stop terminates the run.
func TestRunManager_Stop_AcknowledgesStop(t *testing.T) {
	manager := devpod.NewRunManager(func(workspaceID string) string { return "." }, nil)
	c, cleanup := dialRunChannel(t, manager)
	defer cleanup()

	workspaceID := "ws-1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startPayload, err := json.Marshal(devpod.RunPayload{Action: "start", Kind: devpod.RunKindConsole, Command: "sleep 5"})
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelRun, WorkspaceID: &workspaceID, Payload: startPayload}))

	stopPayload, err := json.Marshal(devpod.RunPayload{Action: "stop"})
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-2", Channel: ws.ChannelRun, WorkspaceID: &workspaceID, Payload: stopPayload}))

	var resp ws.Envelope
	for {
		require.NoError(t, wsjson.Read(ctx, c, &resp))
		if resp.ID == "req-2" {
			break
		}
	}
	var runPayload devpod.RunPayload
	require.NoError(t, json.Unmarshal(resp.Payload, &runPayload))
	require.Equal(t, "stop", runPayload.Action)
}
