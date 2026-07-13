// Package debug proxies the Debug Adapter Protocol (DAP) — same
// Content-Length-framed JSON base protocol as LSP — through to a
// per-language debug adapter subprocess, passed through unmodified
// (contracts/websocket-protocol.md → Canal `debug`: "mensajes DAP
// reenviados sin reinterpretar, igual patrón que lsp"), except for the
// two points FR-042/FR-043 require the Bridge Daemon to actually enforce:
// explicit "not supported" for C/C++, and stopOnEntry gated to JS/TS,
// Python, Swift.
package debug

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// AdapterCommand resolves the subprocess command that starts language's
// debug adapter. FR-041 requires full debugging for JS/TS, Python, Go,
// Java, Swift, Rust; FR-042 requires C/C++ (and anything else) to
// explicitly report unsupported rather than fail silently.
type AdapterCommand func(language string) (name string, args []string, supported bool)

// DefaultAdapterCommand maps each FR-041 language to its real debug
// adapter binary — debugpy (Python), vscode-js-debug (JS/TS), delve
// (Go), java-debug on jdt.ls (Java), lldb-dap (Swift and Rust, same LLDB
// backend for both). Nobody has run this against any of these adapters
// actually installed in this environment, so treat the specific
// binaries/args as unverified, same as the Bridge Daemon's other
// external-process integrations.
func DefaultAdapterCommand(language string) (string, []string, bool) {
	switch language {
	case "python":
		return "python3", []string{"-m", "debugpy.adapter"}, true
	case "javascript", "typescript":
		return "js-debug", nil, true
	case "go":
		return "dlv", []string{"dap"}, true
	case "java":
		return "java", []string{"-jar", "/opt/java-debug/plugins/com.microsoft.java.debug.plugin.jar"}, true
	case "swift", "rust":
		return "lldb-dap", nil, true
	default:
		return "", nil, false // includes c/cpp — FR-042
	}
}

// stopOnEntrySupported mirrors FR-043 — Playground mode (pause on the
// first line without a prior breakpoint) only applies to JS/TS, Python,
// Swift.
func stopOnEntrySupported(language string) bool {
	switch language {
	case "javascript", "typescript", "python", "swift":
		return true
	default:
		return false
	}
}

// adapterServer is one running debug adapter process for a Workspace.
type adapterServer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
}

func startAdapterServer(ctx context.Context, name string, args []string) (*adapterServer, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &adapterServer{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}, nil
}

func (s *adapterServer) write(payload json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintf(s.stdin, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
	return err
}

func (s *adapterServer) readMessage() (json.RawMessage, error) {
	var contentLength int
	for {
		line, err := s.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("debug: invalid Content-Length %q: %w", value, err)
			}
		}
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(s.stdout, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Proxy multiplexes the `debug` channel to per-Workspace debug adapter
// processes — one debug session at a time per Workspace, which is the
// natural DAP usage pattern (unlike `lsp`, DAP has no standard notion of
// running several concurrent sessions against one Workspace).
type Proxy struct {
	command AdapterCommand

	mu         sync.Mutex
	servers    map[string]*adapterServer // key: workspaceID
	languageOf map[string]string         // workspaceID -> language, set on first launch/attach
}

// NewProxy builds a Proxy that starts adapters via command.
func NewProxy(command AdapterCommand) *Proxy {
	return &Proxy{command: command, servers: make(map[string]*adapterServer), languageOf: make(map[string]string)}
}

// Handler returns the ws.Handler to register for ws.ChannelDebug.
func (p *Proxy) Handler() ws.Handler {
	return func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {
		if env.WorkspaceID == nil {
			conn.SendError(env, "workspace-required", "el canal debug requiere workspaceId", nil)
			return
		}
		workspaceID := *env.WorkspaceID

		server, isNew, err := p.serverFor(ctx, workspaceID, env.Payload)
		if err != nil {
			conn.SendError(env, "debug-launch-failed", err.Error(), nil)
			return
		}
		if isNew {
			go p.pump(ctx, conn, workspaceID, server)
		}

		if err := p.rejectUnsupportedStopOnEntry(workspaceID, env.Payload); err != nil {
			conn.SendError(env, "stop-on-entry-unsupported", err.Error(), nil)
			return
		}

		if err := server.write(env.Payload); err != nil {
			conn.SendError(env, "debug-write-failed", err.Error(), nil)
		}
	}
}

func (p *Proxy) serverFor(ctx context.Context, workspaceID string, payload json.RawMessage) (*adapterServer, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if server, ok := p.servers[workspaceID]; ok {
		return server, false, nil
	}

	language, ok := extractLaunchLanguage(payload)
	if !ok {
		return nil, false, fmt.Errorf("debug: no hay sesión de depuración activa para este workspace")
	}
	name, args, supported := p.command(language)
	if !supported {
		// FR-042: explicit, not a silent failure.
		return nil, false, fmt.Errorf("debug: %s no soporta depuración completa", language)
	}
	server, err := startAdapterServer(ctx, name, args)
	if err != nil {
		return nil, false, fmt.Errorf("debug: iniciar adaptador para %q: %w", language, err)
	}
	p.servers[workspaceID] = server
	p.languageOf[workspaceID] = language
	return server, true, nil
}

// rejectUnsupportedStopOnEntry is the one place this proxy actually reads
// DAP semantics rather than passing bytes through untouched — necessary
// to enforce FR-043's language restriction on Playground mode.
func (p *Proxy) rejectUnsupportedStopOnEntry(workspaceID string, payload json.RawMessage) error {
	var msg struct {
		Command   string `json:"command"`
		Arguments struct {
			StopOnEntry bool `json:"stopOnEntry"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil
	}
	if msg.Command != "launch" || !msg.Arguments.StopOnEntry {
		return nil
	}

	p.mu.Lock()
	language := p.languageOf[workspaceID]
	p.mu.Unlock()

	if !stopOnEntrySupported(language) {
		return fmt.Errorf("debug: stopOnEntry (modo Playground) no aplica a %s", language)
	}
	return nil
}

func (p *Proxy) pump(ctx context.Context, conn *ws.Conn, workspaceID string, server *adapterServer) {
	for {
		payload, err := server.readMessage()
		if err != nil {
			return
		}
		_ = conn.SendPayload(ctx, uuid.NewString(), ws.ChannelDebug, &workspaceID, json.RawMessage(payload))
	}
}

// extractLaunchLanguage peeks at a `launch`/`attach` request's
// `arguments.language` — a convention this project defines on top of DAP
// (which has no standard field for this), the same way the LSP proxy
// peeks at `textDocument/didOpen`'s `languageId`.
func extractLaunchLanguage(payload json.RawMessage) (string, bool) {
	var msg struct {
		Command   string `json:"command"`
		Arguments struct {
			Language string `json:"language"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return "", false
	}
	if (msg.Command != "launch" && msg.Command != "attach") || msg.Arguments.Language == "" {
		return "", false
	}
	return msg.Arguments.Language, true
}
