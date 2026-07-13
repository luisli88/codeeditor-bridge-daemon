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

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// ShellPayload is the `shell` channel's payload shape — raw PTY bytes,
// base64-encoded since JSON has no native binary type
// (contracts/websocket-protocol.md).
type ShellPayload struct {
	Data string `json:"data"`
}

// TmuxSessionName maps a Workspace to the name of its persistent tmux
// session (one per project, per plan.md's internal/session description).
type TmuxSessionName func(workspaceID string) string

// OutputWatcher observes every chunk of raw PTY output alongside the
// `shell` channel relay — used to detect the `claude` CLI's
// authentication prompt in the same tmux session (claude_auth.go)
// without that detector needing its own separate PTY attachment.
type OutputWatcher func(ctx context.Context, conn *ws.Conn, workspaceID string, data []byte)

// ShellSessions holds one attached PTY per Workspace, reused across every
// Envelope on the `shell` channel for that Workspace — attaching to tmux
// fresh on every keystroke would be wrong and slow.
type ShellSessions struct {
	mu       sync.Mutex
	attached map[string]*os.File
	name     TmuxSessionName
	watcher  OutputWatcher
}

// NewShellSessions builds an empty ShellSessions.
func NewShellSessions(name TmuxSessionName) *ShellSessions {
	return &ShellSessions{attached: make(map[string]*os.File), name: name}
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

		ptmx, isNew, err := s.attach(workspaceID)
		if err != nil {
			conn.SendError(env, "tmux-attach-failed", err.Error(), nil)
			return
		}
		if isNew {
			go s.pump(ctx, conn, workspaceID, ptmx)
		}

		if env.Payload == nil {
			return
		}
		var payload ShellPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
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

	cmd := exec.Command("tmux", "new-session", "-A", "-s", s.name(workspaceID))
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, false, fmt.Errorf("attach tmux session for workspace %s: %w", workspaceID, err)
	}
	s.attached[workspaceID] = ptmx
	return ptmx, true, nil
}

// pump streams PTY output back to the client as ShellPayload Envelopes
// until the session ends, then forgets the attachment so the next Envelope
// on this Workspace reattaches.
func (s *ShellSessions) pump(ctx context.Context, conn *ws.Conn, workspaceID string, ptmx *os.File) {
	defer func() {
		s.mu.Lock()
		delete(s.attached, workspaceID)
		s.mu.Unlock()
		_ = ptmx.Close()
	}()

	buf := make([]byte, 4096)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			payload := ShellPayload{Data: base64.StdEncoding.EncodeToString(buf[:n])}
			_ = conn.SendPayload(ctx, uuid.NewString(), ws.ChannelShell, &workspaceID, payload)

			s.mu.Lock()
			watcher := s.watcher
			s.mu.Unlock()
			if watcher != nil {
				watcher(ctx, conn, workspaceID, buf[:n])
			}
		}
		if err != nil {
			// io.EOF (or any other read error) means the PTY closed — tmux
			// detached or the process exited. Nothing further to relay.
			return
		}
	}
}
