package devpod

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/google/uuid"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// RunKind mirrors data-model.md → Proyecto.runConfig.kind. There is no
// "gui" case anywhere in this model by design — a Project can only ever
// be configured to run as console or web, which is what makes FR-038
// ("MUST NOT intentar ejecutar apps con interfaz gráfica nativa") true
// structurally rather than by a runtime check.
type RunKind string

const (
	RunKindConsole RunKind = "console"
	RunKindWeb     RunKind = "web"
)

// RunPayload mirrors the `run` channel's payload
// (contracts/websocket-protocol.md → Canal `run`).
type RunPayload struct {
	Action     string  `json:"action"` // "start" | "stop"
	Kind       RunKind `json:"kind,omitempty"`
	Command    string  `json:"command,omitempty"` // only meaningful on start — Proyecto.runConfig.command
	Stdout     string  `json:"stdout,omitempty"`
	Stderr     string  `json:"stderr,omitempty"`
	PreviewURL string  `json:"previewUrl,omitempty"`
}

// PreviewURLFunc builds a Workspace's preview URL from its assigned
// subdomain (02_arquitectura_solucion.md §3.15: assigned automatically,
// not editable). The actual DNS/reverse-proxy routing that makes this URL
// resolve to the running container is infrastructure this package
// doesn't own or verify — same category of gap as `devpod`/package-manager
// subprocess integrations elsewhere in this module.
type PreviewURLFunc func(workspaceID string) string

// runProcess is one running console/web execution for a Workspace.
type runProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// RunManager implements FR-038/FR-039/FR-040/FR-045: starting/stopping a
// Project's console or web run, streaming stdout/stderr live, and
// exposing the Workspace's preview URL for web runs.
type RunManager struct {
	workspacePath func(workspaceID string) string
	previewURL    PreviewURLFunc

	mu      sync.Mutex
	running map[string]*runProcess // key: workspaceID
}

// NewRunManager builds a RunManager.
func NewRunManager(workspacePath func(workspaceID string) string, previewURL PreviewURLFunc) *RunManager {
	return &RunManager{workspacePath: workspacePath, previewURL: previewURL, running: make(map[string]*runProcess)}
}

// Handler returns the ws.Handler to register for ws.ChannelRun.
func (m *RunManager) Handler() ws.Handler {
	return func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {
		if env.WorkspaceID == nil {
			conn.SendError(env, "workspace-required", "el canal run requiere workspaceId", nil)
			return
		}
		workspaceID := *env.WorkspaceID

		var payload RunPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			conn.SendError(env, "invalid-payload", err.Error(), nil)
			return
		}

		switch payload.Action {
		case "start":
			m.start(ctx, conn, workspaceID, payload)
		case "stop":
			m.stop(workspaceID)
			_ = conn.SendPayload(ctx, env.ID, ws.ChannelRun, &workspaceID, RunPayload{Action: "stop"})
		default:
			conn.SendError(env, "unknown-action", fmt.Sprintf("acción de run desconocida: %q", payload.Action), nil)
		}
	}
}

func (m *RunManager) start(ctx context.Context, conn *ws.Conn, workspaceID string, payload RunPayload) {
	m.stop(workspaceID) // FR-045: starting again replaces any prior run for this Workspace

	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, "sh", "-c", payload.Command)
	cmd.Dir = m.workspacePath(workspaceID)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		conn.SendError(ws.Envelope{Channel: ws.ChannelRun, WorkspaceID: &workspaceID}, "run-start-failed", err.Error(), nil)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		conn.SendError(ws.Envelope{Channel: ws.ChannelRun, WorkspaceID: &workspaceID}, "run-start-failed", err.Error(), nil)
		return
	}
	if err := cmd.Start(); err != nil {
		cancel()
		conn.SendError(ws.Envelope{Channel: ws.ChannelRun, WorkspaceID: &workspaceID}, "run-start-failed", err.Error(), nil)
		return
	}

	m.mu.Lock()
	m.running[workspaceID] = &runProcess{cmd: cmd, cancel: cancel}
	m.mu.Unlock()

	if payload.Kind == RunKindWeb {
		previewURL := ""
		if m.previewURL != nil {
			previewURL = m.previewURL(workspaceID)
		}
		_ = conn.SendPayload(ctx, uuid.NewString(), ws.ChannelRun, &workspaceID, RunPayload{
			Action: "start", Kind: RunKindWeb, PreviewURL: previewURL,
		})
	}

	// FR-039: stdout/stderr stream live, not batched at the end.
	go m.pump(ctx, conn, workspaceID, stdout, false)
	go m.pump(ctx, conn, workspaceID, stderr, true)
}

func (m *RunManager) pump(ctx context.Context, conn *ws.Conn, workspaceID string, pipe io.Reader, isStderr bool) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		payload := RunPayload{Action: "start"}
		if isStderr {
			payload.Stderr = scanner.Text()
		} else {
			payload.Stdout = scanner.Text()
		}
		_ = conn.SendPayload(ctx, uuid.NewString(), ws.ChannelRun, &workspaceID, payload)
	}
}

// stop terminates workspaceID's running process, if any (FR-045).
func (m *RunManager) stop(workspaceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proc, ok := m.running[workspaceID]; ok {
		proc.cancel()
		delete(m.running, workspaceID)
	}
}
