// Package session owns the persistent `tmux` session per project — the
// same session both the raw Terminal (mosh primary, this file's `shell`
// channel as fallback — contracts/mosh-terminal.md, FR-053) and the
// interactive `claude` CLI run inside (research.md §7).
package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"github.com/google/uuid"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpodexec"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// ShellPayload is the `shell` channel's payload shape — either raw PTY
// bytes, base64-encoded since JSON has no native binary type, or a
// terminal resize request (`Cols`/`Rows`, no `Data`) carrying the
// client's actual on-screen size, which the PTY otherwise never learns
// (it defaults to whatever `pty.Start` gave it in `attach`, below).
// Without ever receiving one of these, the remote shell/tmux wraps
// output at the wrong column count, visually corrupting anything near
// that (wrong) boundary — confirmed live (contracts/websocket-protocol.md).
type ShellPayload struct {
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// TmuxSessionName maps a Workspace to the name of its persistent tmux
// session (one per project, per plan.md's internal/session description).
type TmuxSessionName func(workspaceID string) string

// OutputWatcher observes every chunk of raw PTY output alongside the
// `shell` channel relay — used to detect the `claude` CLI's
// authentication prompt in the same tmux session (claude_auth.go)
// without that detector needing its own separate PTY attachment.
type OutputWatcher func(ctx context.Context, conn *ws.Conn, workspaceID string, data []byte)

// activeConnection is whichever (ctx, conn) pair most recently sent an
// Envelope for a Workspace's `shell` channel — see `pump`'s doc comment
// for why this needs to be looked up fresh on every relay instead of
// captured once.
type activeConnection struct {
	ctx  context.Context
	conn *ws.Conn
}

// ShellSessions holds one attached PTY per Workspace, reused across every
// Envelope on the `shell` channel for that Workspace — attaching to tmux
// fresh on every keystroke would be wrong and slow.
type ShellSessions struct {
	mu       sync.Mutex
	attached map[string]*os.File
	active   map[string]activeConnection
	name     TmuxSessionName
	watcher  OutputWatcher
}

// NewShellSessions builds an empty ShellSessions.
func NewShellSessions(name TmuxSessionName) *ShellSessions {
	return &ShellSessions{
		attached: make(map[string]*os.File),
		active:   make(map[string]activeConnection),
		name:     name,
	}
}

// SetOutputWatcher registers watcher to receive every chunk of PTY
// output going forward — nil-safe, call before Handler() sees its first
// Envelope to avoid missing an early auth prompt.
func (s *ShellSessions) SetOutputWatcher(watcher OutputWatcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watcher = watcher
}

// Handler returns the ws.Handler to register for ws.ChannelShell.
func (s *ShellSessions) Handler() ws.Handler {
	return func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {
		if env.WorkspaceID == nil {
			conn.SendError(env, "workspace-required", "el canal shell requiere workspaceId", nil)
			return
		}
		workspaceID := *env.WorkspaceID

		// Recorded on every Envelope, not just the one that creates the
		// PTY — a reconnect (app relaunch, dropped WS, ...) sends a new
		// `conn`/`ctx` pair for a Workspace whose PTY is already
		// attached (`isNew` false below), and `pump`'s goroutine — still
		// alive, still reading the same tmux session — needs to notice.
		s.mu.Lock()
		previous, hadActive := s.active[workspaceID]
		isNewViewer := !hadActive || previous.conn != conn
		s.active[workspaceID] = activeConnection{ctx: ctx, conn: conn}
		s.mu.Unlock()

		ptmx, isNew, err := s.attach(workspaceID)
		if err != nil {
			conn.SendError(env, "tmux-attach-failed", err.Error(), nil)
			return
		}
		if isNew {
			go s.pump(workspaceID, ptmx)
		} else if isNewViewer {
			// This Workspace's tmux session is already attached — reused
			// as-is (this whole type's own point: attaching fresh on
			// every keystroke would be wrong and slow) — but a *different*
			// WebSocket connection just started relaying for it (app
			// relaunch, a dropped/reconnected socket, a second device).
			// tmux only ever repaints its screen for a client it thinks
			// just attached; it has no idea the other end of this
			// redirection just changed, so it silently kept streaming
			// only *new* output (confirmed live: the status bar's own
			// once-a-second clock tick reached a fresh viewer just fine,
			// but the pane content already on screen — the prompt,
			// anything printed before this viewer connected — never did,
			// indefinitely, until the Desarrollador typed something
			// blind and *that* counted as new output). `refresh-client`
			// forces tmux to repaint for the one real OS-level client
			// this type keeps attached, which is exactly the redirection
			// target that just changed.
			go refreshTmuxClient(workspaceID, s.name(workspaceID))
		}

		if env.Payload == nil {
			return
		}
		var payload ShellPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return
		}
		if payload.Cols > 0 && payload.Rows > 0 {
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(payload.Cols), Rows: uint16(payload.Rows)})
			return
		}
		raw, err := base64.StdEncoding.DecodeString(payload.Data)
		if err != nil {
			return
		}
		_, _ = ptmx.Write(raw)
	}
}

func (s *ShellSessions) attach(workspaceID string) (*os.File, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if f, ok := s.attached[workspaceID]; ok {
		return f, false, nil
	}

	// Runs inside workspaceID's own devpod Workspace container via `devpod
	// ssh`, not on bridged's own host — same mechanism `devpodexec` already
	// uses for the LSP/Run channels (see its package doc comment). Without
	// this, the Terminal landed in bridged's own container instead of the
	// devcontainer.json-provisioned one, so the workspace's own toolchain
	// (node/npm for a Node.js project, etc.) was never on PATH — confirmed
	// live: `node`/`npm` genuinely weren't installed anywhere bridged
	// itself could see them.
	if err := ensureTmuxInstalled(workspaceID); err != nil {
		return nil, false, fmt.Errorf("ensure tmux installed for workspace %s: %w", workspaceID, err)
	}
	tmuxCmd := devpodexec.ShellJoin([]string{"tmux", "new-session", "-A", "-s", s.name(workspaceID)})
	cmd := exec.Command("devpod", "ssh", workspaceID, "--command", tmuxCmd)
	// bridged runs as a daemon (systemd/container entrypoint), not from an
	// interactive shell — it inherits no TERM, and tmux refuses to
	// initialize the screen without one ("terminal does not support
	// clear", surfaced verbatim to the client's real terminal emulator).
	// The client (SwiftTerm) understands xterm-256color.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, false, fmt.Errorf("attach tmux session for workspace %s: %w", workspaceID, err)
	}
	s.attached[workspaceID] = ptmx
	return ptmx, true, nil
}

// refreshTmuxClient forces a full-screen repaint for workspaceID's already-
// attached OS-level tmux client (`ShellSessions.attach`'s cached ptmx) — see
// the call site in `Handler` for why this is needed at all. Best-effort:
// nothing meaningful to do with an error here (no Envelope/conn to report
// one on — this runs detached, after `Handler` already returned), and a
// failed refresh just leaves the Desarrollador looking at a stale screen
// until real new output happens to arrive, same as before this existed.
func refreshTmuxClient(workspaceID, sessionName string) {
	cmd := devpodexec.ShellJoin([]string{"tmux", "refresh-client", "-t", sessionName})
	_ = exec.Command("devpod", "ssh", workspaceID, "--command", cmd).Run()
}

// ensureTmuxInstalled installs tmux inside workspaceID's devpod Workspace
// if it isn't already there. devcontainer.json base images generally
// don't ship it — confirmed by hand against
// mcr.microsoft.com/devcontainers/typescript-node, the first real image
// this shell channel was tested against after routing through `devpod
// ssh` (this file's `attach`): "tmux: command not found", surfacing as a
// tunnel failure (exit 127) on every attach attempt. Every devcontainers
// base image observed so far is Debian/Ubuntu with passwordless sudo for
// its default non-root user, so `apt-get` is safe to assume without a
// package-manager-detection layer this project doesn't have anywhere
// else either.
func ensureTmuxInstalled(workspaceID string) error {
	check := devpodexec.ShellJoin([]string{"sh", "-c", "command -v tmux"})
	if exec.Command("devpod", "ssh", workspaceID, "--command", check).Run() == nil {
		return nil
	}
	install := devpodexec.ShellJoin([]string{"sh", "-c", "sudo apt-get update -qq && sudo apt-get install -y -qq tmux"})
	return exec.Command("devpod", "ssh", workspaceID, "--command", install).Run()
}

// pump streams PTY output back to the client as ShellPayload Envelopes
// until the session ends, then forgets the attachment so the next Envelope
// on this Workspace reattaches.
//
// Reads `s.active[workspaceID]` fresh on every chunk instead of a `conn`/
// `ctx` captured once at goroutine start — this goroutine is started only
// the *first* time a Workspace's PTY is created (`isNew` in `Handler`)
// and then lives for as long as tmux does, but the WebSocket connection
// that happened to trigger that first attach is under no obligation to
// live that long too (an app relaunch, a dropped connection, ...). With a
// captured `conn` this goroutine kept writing to that first, now-closed
// connection forever, and `ctx` being that connection's own
// (`Server.ServeHTTP`'s `r.Context()`, cancelled the moment it closes)
// meant every `SendPayload` failed silently — the PTY itself stayed
// perfectly healthy (confirmed live via `tmux capture-pane` showing real,
// correct output) while every *new* connection's Terminal sat forever on
// a blank screen with a live cursor, never receiving a single byte.
func (s *ShellSessions) pump(workspaceID string, ptmx *os.File) {
	defer func() {
		s.mu.Lock()
		delete(s.attached, workspaceID)
		delete(s.active, workspaceID)
		s.mu.Unlock()
		_ = ptmx.Close()
	}()

	buf := make([]byte, 4096)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			active, ok := s.active[workspaceID]
			watcher := s.watcher
			s.mu.Unlock()
			if ok {
				payload := ShellPayload{Data: base64.StdEncoding.EncodeToString(buf[:n])}
				_ = active.conn.SendPayload(active.ctx, uuid.NewString(), ws.ChannelShell, &workspaceID, payload)
				if watcher != nil {
					watcher(active.ctx, active.conn, workspaceID, buf[:n])
				}
			}
		}
		if err != nil {
			// io.EOF (or any other read error) means the PTY closed — tmux
			// detached or the process exited. Nothing further to relay.
			return
		}
	}
}
