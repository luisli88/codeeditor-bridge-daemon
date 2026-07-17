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

// A second connection for the same Workspace (an app relaunch, a dropped
// WebSocket, ...) must start receiving PTY output too — not just accept
// writes while the original pump goroutine, still bound to the first,
// now-closed connection, silently discards everything it reads. This is
// a regression test for exactly that bug: found live via `tmux
// capture-pane` showing real, correct output while the Terminal sat on a
// permanently blank screen after any reconnect.
func TestShellSessions_SecondConnectionAfterFirstCloses_StillReceivesOutput(t *testing.T) {
	fakeDevpodSSHScript(t)
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	workspaceID := "ws-1"

	send := func(c *websocket.Conn, command string) {
		payload, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(command))})
		require.NoError(t, err)
		require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
			ID: "req", Channel: ws.ChannelShell, WorkspaceID: &workspaceID, Payload: payload,
		}))
	}

	// First connection: attaches (creates the PTY, starts the pump), then
	// disconnects — same as an app relaunch or a dropped WebSocket.
	first, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	send(first, "echo FIRST_MARKER\n")
	require.Eventually(t, func() bool {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer readCancel()
		var env ws.Envelope
		return wsjson.Read(readCtx, first, &env) == nil
	}, 8*time.Second, 100*time.Millisecond, "expected the first connection to see at least one reply before disconnecting")
	require.NoError(t, first.CloseNow())

	// Second connection, same Workspace: the PTY/tmux session already
	// exists (`isNew` is false), so no new pump starts — the existing one
	// must notice this connection and start writing to it instead.
	second, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer second.CloseNow() //nolint:errcheck
	send(second, "echo SECOND_MARKER\n")

	require.Eventually(t, func() bool {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer readCancel()
		var env ws.Envelope
		if err := wsjson.Read(readCtx, second, &env); err != nil {
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
		return strings.Contains(string(raw), "SECOND_MARKER")
	}, 8*time.Second, 100*time.Millisecond, "expected the second connection to receive PTY output after the first one closed")
}

// A new viewer taking over an already-attached tmux session (app
// relaunch, a dropped/reconnected socket, a second device) must see the
// screen that's already there, not just whatever new output happens next
// — see `refreshTmuxClient`'s doc comment for the real symptom this was
// found live: a Terminal that looked permanently blank/stuck after any
// reconnect, until the Desarrollador typed something blind. This connects
// only with a payload-less "attach" Envelope (never anything that would
// itself produce output) and still expects to see content the *first*
// connection already printed before it disconnected.
func TestShellSessions_SecondViewer_GetsFullRepaintWithoutTypingAnything(t *testing.T) {
	fakeDevpodSSHScript(t)
	sessions := session.NewShellSessions(newTmuxSessionName(t))
	srv := ws.NewServer()
	srv.Handle(ws.ChannelShell, sessions.Handler())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	workspaceID := "ws-1"

	send := func(c *websocket.Conn, command string) {
		payload, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(command))})
		require.NoError(t, err)
		require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
			ID: "req", Channel: ws.ChannelShell, WorkspaceID: &workspaceID, Payload: payload,
		}))
	}

	first, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	send(first, "echo ALREADY_ON_SCREEN\n")
	require.Eventually(t, func() bool {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer readCancel()
		var env ws.Envelope
		return wsjson.Read(readCtx, first, &env) == nil
	}, 8*time.Second, 100*time.Millisecond, "expected the first connection to see at least one reply before disconnecting")
	require.NoError(t, first.CloseNow())

	second, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer second.CloseNow() //nolint:errcheck
	require.NoError(t, wsjson.Write(ctx, second, ws.Envelope{
		ID: "attach", Channel: ws.ChannelShell, WorkspaceID: &workspaceID,
	}))

	var sawAlreadyOnScreen bool
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && !sawAlreadyOnScreen {
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		var env ws.Envelope
		err := wsjson.Read(readCtx, second, &env)
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
		if err == nil && strings.Contains(string(raw), "ALREADY_ON_SCREEN") {
			sawAlreadyOnScreen = true
		}
	}
	require.True(t, sawAlreadyOnScreen, "a new viewer attaching to an already-running tmux session must get a full repaint, not just future output")
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
