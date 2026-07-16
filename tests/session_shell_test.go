package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/session"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

func newTmuxSessionName(t *testing.T) session.TmuxSessionName {
	t.Helper()
	suffix := t.Name()
	return func(workspaceID string) string {
		return fmt.Sprintf("codeeditor-test-%s-%s", suffix, workspaceID)
	}
}

// The Terminal channel round-trips real bytes through a real `tmux`
// session — attach, write a command, read the echoed output back.
func TestShellSessions_AttachWriteRead_RoundTripsThroughRealTmux(t *testing.T) {
	fakeDevpodSSHScript(t)
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	command := "echo CODEEDITOR_MARKER\n"
	payload, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(command))})
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelShell, WorkspaceID: &workspaceID, Payload: payload,
	}))

	require.Eventually(t, func() bool {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer readCancel()
		var env ws.Envelope
		if err := wsjson.Read(readCtx, c, &env); err != nil {
			return false
		}
		var shellPayload struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(env.Payload, &shellPayload); err != nil {
			return false
		}
		raw, err := base64.StdEncoding.DecodeString(shellPayload.Data)
		if err != nil {
			return false
		}
		return strings.Contains(string(raw), "CODEEDITOR_MARKER")
	}, 8*time.Second, 100*time.Millisecond, "expected to see the echoed marker in the PTY output")
}

// The claude-auth output watcher taps the same PTY stream without
// disrupting the shell channel's own relay.
func TestShellSessions_OutputWatcher_SeesSameBytesAsShellChannel(t *testing.T) {
	fakeDevpodSSHScript(t)
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	detector := session.NewAuthDetector()
	sessions.SetOutputWatcher(detector.Watch())

	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	srv.Handle(ws.ChannelClaude, func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	command := "echo 'Visit https://claude.ai/device and enter code ABCD-1234'\n"
	payload, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(command))})
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelShell, WorkspaceID: &workspaceID, Payload: payload,
	}))

	var sawAuthRequired bool
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && !sawAuthRequired {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		var env ws.Envelope
		err := wsjson.Read(readCtx, c, &env)
		readCancel()
		if err != nil {
			continue
		}
		if env.Channel == ws.ChannelClaude {
			sawAuthRequired = true
		}
	}
	require.True(t, sawAuthRequired, "expected the claude channel to signal auth-required from the shared PTY output")
}

// A second Envelope on the same Workspace reuses the already-attached PTY
// rather than reattaching to tmux — the same `git`/`shell` distinction
// other channels make between a first, session-opening request and every
// following one.
func TestShellSessions_SecondEnvelopeSameWorkspace_ReusesAttachedPTY(t *testing.T) {
	fakeDevpodSSHScript(t)
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	send := func(command string) {
		payload, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(command))})
		require.NoError(t, err)
		require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
			ID: "req", Channel: ws.ChannelShell, WorkspaceID: &workspaceID, Payload: payload,
		}))
	}
	send("echo FIRST_MARKER\n")
	send("echo SECOND_MARKER\n")

	var sawFirst, sawSecond bool
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && (!sawFirst || !sawSecond) {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		var env ws.Envelope
		err := wsjson.Read(readCtx, c, &env)
		readCancel()
		if err != nil {
			continue
		}
		var shellPayload struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(env.Payload, &shellPayload) != nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(shellPayload.Data)
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), "FIRST_MARKER") {
			sawFirst = true
		}
		if strings.Contains(string(raw), "SECOND_MARKER") {
			sawSecond = true
		}
	}
	require.True(t, sawFirst && sawSecond, "expected both commands to run in the same reused PTY session")
}

func TestShellSessions_NoWorkspaceID_ReturnsError(t *testing.T) {
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelShell}))

	var env ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &env))
	require.NotNil(t, env.Error)
	require.Equal(t, "workspace-required", env.Error.Code)
}

// A first Envelope with no payload just opens the session — no PTY write,
// no crash.
func TestShellSessions_NilPayload_JustAttaches(t *testing.T) {
	fakeDevpodSSHScript(t)
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-1", Channel: ws.ChannelShell, WorkspaceID: &workspaceID}))

	// No response is expected for a payload-less attach — just confirm no
	// error arrives within a short window.
	readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer readCancel()
	var env ws.Envelope
	err = wsjson.Read(readCtx, c, &env)
	if err == nil {
		require.Nil(t, env.Error)
	}
}

func TestShellSessions_MalformedPayload_IsIgnored(t *testing.T) {
	fakeDevpodSSHScript(t)
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	workspaceID := "ws-1"
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-1", Channel: ws.ChannelShell, WorkspaceID: &workspaceID, Payload: json.RawMessage(`{"not":"shell payload shape"}`),
	}))
	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID: "req-2", Channel: ws.ChannelShell, WorkspaceID: &workspaceID,
		Payload: json.RawMessage(`{"data":"not valid base64!!"}`),
	}))

	// Neither malformed message should crash the handler or the session —
	// a well-formed follow-up still works.
	send := func(command string) {
		payload, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(command))})
		require.NoError(t, err)
		require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
			ID: "req-3", Channel: ws.ChannelShell, WorkspaceID: &workspaceID, Payload: payload,
		}))
	}
	send("echo STILL_ALIVE\n")

	var sawMarker bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !sawMarker {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		var env ws.Envelope
		err := wsjson.Read(readCtx, c, &env)
		readCancel()
		if err != nil {
			continue
		}
		var shellPayload struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(env.Payload, &shellPayload) != nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(shellPayload.Data)
		if err == nil && strings.Contains(string(raw), "STILL_ALIVE") {
			sawMarker = true
		}
	}
	require.True(t, sawMarker, "session should keep working after malformed payloads")
}
