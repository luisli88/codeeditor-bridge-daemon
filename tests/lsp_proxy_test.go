package tests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// `cat` echoes stdin to stdout byte-for-byte, so wrapping LSPProxy's
// Content-Length framing around it round-trips exactly — a real
// subprocess exercising the real framing code, without needing an actual
// Language Server installed.
func catCommand(languageID string) (string, []string, error) {
	return "cat", nil, nil
}

func TestLSPProxy_DidOpen_RoutesAndEchoesThroughRealFraming(t *testing.T) {
	fakeDevpodSSHScript(t)
	var installedLanguages []string
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, workspaceID, languageID string) error {
		installedLanguages = append(installedLanguages, languageID)
		return nil
	})

	srv := ws.NewServer()
	srv.Handle(ws.ChannelLSP, proxy.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	didOpen := map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"languageId": "go", "uri": "file:///main.go"}},
	}
	payload, err := json.Marshal(didOpen)
	require.NoError(t, err)

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: payload,
	}))

	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	require.Nil(t, resp.Error)
	require.JSONEq(t, string(payload), string(resp.Payload))
	require.Equal(t, []string{"go"}, installedLanguages)
}

// A second connection for the same Workspace (an app relaunch, revisiting
// a Project, ...) must start receiving the Language Server's replies too
// — not just accept writes while the original pump goroutine, still
// bound to the first, now-closed connection, silently discards
// everything it reads. Regression test for the same bug/fix as
// `session_shell_test.go`'s analogous test — see `LSPProxy.pump`'s doc
// comment.
func TestLSPProxy_SecondConnectionAfterFirstCloses_StillReceivesOutput(t *testing.T) {
	fakeDevpodSSHScript(t)
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, workspaceID, languageID string) error { return nil })
	srv := ws.NewServer()
	srv.Handle(ws.ChannelLSP, proxy.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	workspaceID := "ws-1"

	didOpen := func(uri string) json.RawMessage {
		payload, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"languageId": "go", "uri": uri}},
		})
		require.NoError(t, err)
		return payload
	}

	// First connection: spawns the (fake, `cat`) Language Server and
	// starts its pump, then disconnects — same as an app relaunch.
	first, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	firstPayload := didOpen("file:///first.go")
	require.NoError(t, wsjson.Write(ctx, first, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: firstPayload,
	}))
	var firstResp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, first, &firstResp))
	require.JSONEq(t, string(firstPayload), string(firstResp.Payload))
	require.NoError(t, first.CloseNow())

	// Second connection, same Workspace: the server process already
	// exists (`isNew` is false), so no new pump starts — the existing
	// one must notice this connection and start writing to it instead.
	second, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer second.CloseNow() //nolint:errcheck
	secondPayload := didOpen("file:///second.go")
	require.NoError(t, wsjson.Write(ctx, second, ws.Envelope{
		ID: "req-2", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: secondPayload,
	}))

	readCtx, readCancel := context.WithTimeout(ctx, 8*time.Second)
	defer readCancel()
	var secondResp ws.Envelope
	require.NoError(t, wsjson.Read(readCtx, second, &secondResp))
	require.Nil(t, secondResp.Error)
	require.JSONEq(t, string(secondPayload), string(secondResp.Payload))
}

func TestLSPProxy_NoWorkspaceID_ReturnsError(t *testing.T) {
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, workspaceID, languageID string) error { return nil })
	srv := ws.NewServer()
	srv.Handle(ws.ChannelLSP, proxy.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelLSP}))

	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	require.NotNil(t, resp.Error)
	require.Equal(t, "workspace-required", resp.Error.Code)
}

// FR-036: a language nobody detected up front still gets its Language
// Server installed, on demand, the moment a file of that language opens.
func TestLSPProxy_UndetectedLanguage_InstallsOnDemand(t *testing.T) {
	fakeDevpodSSHScript(t)
	var installedLanguages []string
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, workspaceID, languageID string) error {
		installedLanguages = append(installedLanguages, languageID)
		return nil
	})
	srv := ws.NewServer()
	srv.Handle(ws.ChannelLSP, proxy.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	didOpen := map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"languageId": "ruby", "uri": "file:///main.rb"}},
	}
	payload, err := json.Marshal(didOpen)
	require.NoError(t, err)

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))

	require.Equal(t, []string{"ruby"}, installedLanguages)
}

func TestLSPProxy_InstallFailure_ReturnsError(t *testing.T) {
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, workspaceID, languageID string) error {
		return errors.New("install failed")
	})
	srv := ws.NewServer()
	srv.Handle(ws.ChannelLSP, proxy.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	didOpen := map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"languageId": "go", "uri": "file:///main.go"}},
	}
	payload, err := json.Marshal(didOpen)
	require.NoError(t, err)

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: payload,
	}))
	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	require.NotNil(t, resp.Error)
	require.Equal(t, "lsp-launch-failed", resp.Error.Code)
}
