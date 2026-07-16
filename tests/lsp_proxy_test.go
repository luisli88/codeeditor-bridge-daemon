package tests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
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

// `true` exits immediately, closing its stdout right away — stands in
// for a Language Server process that dies almost as soon as it starts
// (a real `vtsls` doing exactly this under memory pressure, `SIGKILL`,
// is what actually exposed the bug this file's own eviction test is for).
func trueCommand(languageID string) (string, []string, error) {
	return "true", nil, nil
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

// A spec-compliant LSP client always sends `initialize` before anything
// else — including the first `textDocument/didOpen` this proxy otherwise
// relies on to learn which language to route to. Regression test for the
// bug found via a real end-to-end WS client against a live daemon: the
// proxy rejected that very first `initialize` with `lsp-no-active-session`
// (no language known yet), which a real client can't recover from since it
// blocks on `initialize`'s response before sending anything else — a
// deadlock, not just a dropped message, and the reason LSP never worked at
// all regardless of every other fix in this file's history.
func TestLSPProxy_Initialize_BeforeAnyDidOpen_SucceedsSynthetically(t *testing.T) {
	fakeDevpodSSHScript(t)
	proxy := ws.NewLSPProxy(catCommand, func(ctx context.Context, workspaceID, languageID string) error { return nil })
	srv := ws.NewServer()
	srv.Handle(ws.ChannelLSP, proxy.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"

	initialize, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"processId": nil, "rootUri": nil, "capabilities": map[string]any{}},
	})
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: initialize,
	}))

	var initResp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &initResp))
	require.Nil(t, initResp.Error, "initialize must not be rejected before any didOpen")
	var decoded struct {
		ID     int `json:"id"`
		Result struct {
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(initResp.Payload, &decoded))
	require.Equal(t, 1, decoded.ID)

	initialized, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-2", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: initialized,
	}))

	// The handshake must not have spawned or blocked anything — a real
	// didOpen still routes and echoes normally right after.
	didOpenPayload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"languageId": "go", "uri": "file:///main.go"}},
	})
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-3", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: didOpenPayload,
	}))

	// `cat` (this test's stand-in Language Server, see `catCommand`) echoes
	// literally everything written to its stdin, including the replayed
	// `initialize`/`initialized` `replayHandshake` sends it once it spawns
	// — a real Language Server wouldn't echo those back on its own (they
	// have no output of their own, or their `initialize` response is
	// swallowed by `pump` before reaching here), so tolerate and skip that
	// echo noise instead of asserting on it; the one message that matters
	// is the didOpen echo actually arriving, proving the handshake replay
	// didn't wedge routing for the message that triggered it.
	var didOpenResp ws.Envelope
	for range 5 {
		require.NoError(t, wsjson.Read(ctx, c, &didOpenResp))
		require.Nil(t, didOpenResp.Error)
		if string(didOpenResp.Payload) == string(didOpenPayload) {
			return
		}
	}
	t.Fatalf("didOpen echo never arrived; last message: %s", didOpenResp.Payload)
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

// Regression test for a bug found live: a real vtsls process can die
// (SIGKILL, confirmed under memory pressure from a long test session)
// entirely independent of anything the client does. Before
// LSPProxy.evictDeadServer, the dead entry stayed in `servers` forever —
// every later request for the same (workspaceID, languageID) kept being
// routed to a handle that could never respond again, silently, until the
// whole daemon restarted. This is exactly "no aparece nada" after typing
// a trigger character: the request went out, but nothing was ever going
// to answer it.
func TestLSPProxy_ServerProcessDies_NextRequestRespawnsInsteadOfReusingDeadHandle(t *testing.T) {
	fakeDevpodSSHScript(t)
	var mu sync.Mutex
	spawnCount := 0
	proxy := ws.NewLSPProxy(trueCommand, func(ctx context.Context, workspaceID, languageID string) error {
		mu.Lock()
		spawnCount++
		mu.Unlock()
		return nil
	})
	srv := ws.NewServer()
	srv.Handle(ws.ChannelLSP, proxy.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	didOpen, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"languageId": "go", "uri": "file:///main.go"}},
	})
	require.NoError(t, err)

	// Spawns a server that dies almost immediately (`true` exits right
	// away) — nothing ever answers this first didOpen, which is expected.
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: didOpen,
	}))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return spawnCount == 1
	}, 5*time.Second, 20*time.Millisecond, "first didOpen should have spawned a server")

	// pump noticing the dead process and evicting it is itself async and
	// races with this test — retrying the send (a fresh envelope each
	// time, since a WS message can't be replayed) until it lands *after*
	// eviction, rather than sending once and hoping the timing lines up,
	// is what actually waits out that race. Without evictDeadServer, this
	// would just keep reusing the same dead handle forever and spawnCount
	// would never reach 2, correctly failing the test on timeout.
	require.Eventually(t, func() bool {
		mu.Lock()
		count := spawnCount
		mu.Unlock()
		if count >= 2 {
			return true
		}
		_ = wsjson.Write(ctx, c, ws.Envelope{
			ID: "req-2", Channel: ws.ChannelLSP, WorkspaceID: &workspaceID, Payload: didOpen,
		})
		return false
	}, 5*time.Second, 50*time.Millisecond, "second didOpen should eventually respawn instead of reusing the dead server")
}
