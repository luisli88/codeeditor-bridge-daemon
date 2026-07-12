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
	var installedLanguages []string
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, languageID string) error {
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

func TestLSPProxy_NoWorkspaceID_ReturnsError(t *testing.T) {
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, languageID string) error { return nil })
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
	var installedLanguages []string
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, languageID string) error {
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
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, languageID string) error {
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
