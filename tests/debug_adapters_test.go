package tests

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/debug"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// `cat` echoes stdin to stdout byte-for-byte, exercising the real
// Content-Length framing code without needing a real debug adapter
// installed (same trick as lsp_proxy_test.go).
func catAdapterCommand(language string) (string, []string, bool) {
	switch language {
	case "c", "cpp":
		return "", nil, false
	default:
		return "cat", nil, true
	}
}

func dialDebugChannel(t *testing.T, proxy *debug.Proxy) (*websocket.Conn, func()) {
	t.Helper()
	srv := ws.NewServer()
	srv.Handle(ws.ChannelDebug, proxy.Handler())
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

func sendDebugMessage(t *testing.T, c *websocket.Conn, workspaceID string, msg map[string]any) ws.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, err := json.Marshal(msg)
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelDebug, WorkspaceID: &workspaceID, Payload: payload,
	}))

	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	return resp
}

// FR-041: breakpoint → stop → variables/stack round-trips for a
// supported language, via the same DAP base-protocol framing every real
// adapter speaks.
func TestDebugProxy_LaunchAndSetBreakpoints_RoundTripsThroughRealFraming(t *testing.T) {
	fakeDevpodSSHScript(t)
	proxy := debug.NewProxy(catAdapterCommand)
	c, cleanup := dialDebugChannel(t, proxy)
	defer cleanup()

	launch := map[string]any{
		"command": "launch", "arguments": map[string]any{"language": "go", "program": "/ws/main.go"},
	}
	launchResp := sendDebugMessage(t, c, "ws-1", launch)
	require.Nil(t, launchResp.Error)
	var echoedLaunch map[string]any
	require.NoError(t, json.Unmarshal(launchResp.Payload, &echoedLaunch))
	require.Equal(t, "launch", echoedLaunch["command"])

	setBreakpoints := map[string]any{
		"command": "setBreakpoints",
		"arguments": map[string]any{
			"source": map[string]any{"path": "/ws/main.go"}, "breakpoints": []map[string]any{{"line": 10}},
		},
	}
	bpResp := sendDebugMessage(t, c, "ws-1", setBreakpoints)
	require.Nil(t, bpResp.Error)
	var echoedBP map[string]any
	require.NoError(t, json.Unmarshal(bpResp.Payload, &echoedBP))
	require.Equal(t, "setBreakpoints", echoedBP["command"])
}

// FR-042: C/C++ reports unsupported explicitly rather than failing silently.
func TestDebugProxy_UnsupportedLanguage_ReturnsExplicitError(t *testing.T) {
	proxy := debug.NewProxy(catAdapterCommand)
	c, cleanup := dialDebugChannel(t, proxy)
	defer cleanup()

	launch := map[string]any{
		"command": "launch", "arguments": map[string]any{"language": "cpp", "program": "/ws/main.cpp"},
	}
	resp := sendDebugMessage(t, c, "ws-1", launch)

	require.NotNil(t, resp.Error)
	require.Equal(t, "debug-launch-failed", resp.Error.Code)
}

// FR-043: stopOnEntry only applies to JS/TS, Python, Swift.
func TestDebugProxy_StopOnEntry_RejectedForUnsupportedLanguage(t *testing.T) {
	fakeDevpodSSHScript(t)
	proxy := debug.NewProxy(catAdapterCommand)
	c, cleanup := dialDebugChannel(t, proxy)
	defer cleanup()

	launchNoStopOnEntry := map[string]any{
		"command": "launch", "arguments": map[string]any{"language": "go", "program": "/ws/main.go"},
	}
	launchResp := sendDebugMessage(t, c, "ws-1", launchNoStopOnEntry)
	require.Nil(t, launchResp.Error)

	launchWithStopOnEntry := map[string]any{
		"command": "launch", "arguments": map[string]any{"language": "go", "stopOnEntry": true},
	}
	resp := sendDebugMessage(t, c, "ws-1", launchWithStopOnEntry)

	require.NotNil(t, resp.Error)
	require.Equal(t, "stop-on-entry-unsupported", resp.Error.Code)
}

func TestDebugProxy_StopOnEntry_AllowedForSupportedLanguage(t *testing.T) {
	fakeDevpodSSHScript(t)
	proxy := debug.NewProxy(catAdapterCommand)
	c, cleanup := dialDebugChannel(t, proxy)
	defer cleanup()

	launch := map[string]any{
		"command": "launch", "arguments": map[string]any{"language": "python", "program": "/ws/main.py"},
	}
	launchResp := sendDebugMessage(t, c, "ws-1", launch)
	require.Nil(t, launchResp.Error)

	launchWithStopOnEntry := map[string]any{
		"command": "launch", "arguments": map[string]any{"language": "python", "stopOnEntry": true},
	}
	resp := sendDebugMessage(t, c, "ws-1", launchWithStopOnEntry)

	require.Nil(t, resp.Error)
}

// Regression test mirroring lsp_proxy_test.go's own — same
// evictDeadServer fix, same underlying bug: an adapter process that dies
// (crash, `SIGKILL`, ...) independent of anything the client does used
// to leave a permanently-dead entry in `Proxy.servers`, silently routing
// every later request for the Workspace to a handle that could never
// respond again.
func TestDebugProxy_AdapterProcessDies_NextRequestRespawnsInsteadOfReusingDeadHandle(t *testing.T) {
	fakeDevpodSSHScript(t)
	var mu sync.Mutex
	spawnCount := 0
	command := func(language string) (string, []string, bool) {
		mu.Lock()
		spawnCount++
		mu.Unlock()
		return "true", nil, true
	}
	proxy := debug.NewProxy(command)
	c, cleanup := dialDebugChannel(t, proxy)
	defer cleanup()

	launch := map[string]any{
		"command": "launch", "arguments": map[string]any{"language": "go", "program": "/ws/main.go"},
	}
	payload, err := json.Marshal(launch)
	require.NoError(t, err)
	workspaceID := "ws-1"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Spawns an adapter that dies almost immediately (`true` exits right
	// away) — nothing ever answers this first launch, which is expected.
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelDebug, WorkspaceID: &workspaceID, Payload: payload,
	}))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return spawnCount == 1
	}, 5*time.Second, 20*time.Millisecond, "first launch should have spawned an adapter")

	// pump noticing the dead process and evicting it is async — retrying
	// the send (a fresh envelope each time) until it lands after eviction
	// waits out that race instead of hoping the timing lines up.
	require.Eventually(t, func() bool {
		mu.Lock()
		count := spawnCount
		mu.Unlock()
		if count >= 2 {
			return true
		}
		_ = wsjson.Write(ctx, c, ws.Envelope{
			ID: "req-2", Channel: ws.ChannelDebug, WorkspaceID: &workspaceID, Payload: payload,
		})
		return false
	}, 5*time.Second, 50*time.Millisecond, "second launch should eventually respawn instead of reusing the dead adapter")
}
