package ws

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

	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpodexec"
)

// LSPCommand resolves the subprocess command+args that start languageID's
// Language Server (research.md §10): pyright-langserver --stdio (Python),
// vtsls --stdio (JS/TS), rust-analyzer (Rust), gopls (Go), clangd (C/C++),
// sourcekit-lsp (Swift), the jdt.ls launcher (Java) — whatever wires an
// LSPProxy together (main.go) supplies the actual mapping.
type LSPCommand func(languageID string) (name string, args []string, err error)

// lspServer is one running Language Server process for a single
// (workspaceID, languageID) pair, spoken to over stdio using the LSP base
// protocol's `Content-Length` framing — the JSON-RPC payload itself is
// never reinterpreted (contracts/websocket-protocol.md → Canal `lsp`).
type lspServer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex // guards concurrent writes to stdin
}

// startLSPServer runs name+args inside workspaceID's devpod Workspace
// (devpodexec.StartPiped) rather than on the Bridge Daemon's own host —
// the Language Server needs the Workspace's own toolchain (its installed
// node_modules, its Python venv, ...), which only exists inside that
// container, not wherever the daemon itself happens to be running.
//
// Deliberately does *not* take the caller's per-request `ctx` —
// `devpodexec.StartPiped` runs the subprocess via `exec.CommandContext`,
// which kills it the moment its context is cancelled, and the `ctx`
// `Handler` hands to `serverFor` is scoped to a single WebSocket
// connection's lifetime (`Server.ServeHTTP`'s `r.Context()`). A Language
// Server needs to outlive whichever connection happened to be the one
// that spawned it (an app relaunch, revisiting a Project, ...) — the
// same reasoning `internal/devpod/run.go` already gets right with its
// own `context.WithCancel(context.Background())` instead of reusing the
// request context. Confirmed live via this file's own reconnect test:
// without this, the *process itself* died on the first connection's
// close ("write: broken pipe" on the very next request), which is a
// deeper problem than `pump`'s stale-connection bug above and would
// still break a reconnect even with that fixed.
func startLSPServer(workspaceID, name string, args []string) (*lspServer, error) {
	cmd, stdin, stdout, err := devpodexec.StartPiped(context.Background(), workspaceID, name, args...)
	if err != nil {
		return nil, err
	}
	return &lspServer{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}, nil
}

// write frames payload with the LSP base protocol's Content-Length header
// and sends it to the server's stdin.
func (s *lspServer) write(payload json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintf(s.stdin, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
	return err
}

// readMessage reads one Content-Length-framed JSON-RPC message from the
// server's stdout and returns the raw JSON payload, unframed.
func (s *lspServer) readMessage() (json.RawMessage, error) {
	var contentLength int
	for {
		line, err := s.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line ends the header block
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("ws: invalid Content-Length %q: %w", value, err)
			}
		}
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(s.stdout, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// LSPProxy passes JSON-RPC 2.0 through to the right Language Server
// process per Workspace, without reinterpreting it.
//
// **MVP simplification**: one active language per Workspace connection —
// `textDocument/didOpen`'s `languageId` selects (and, if needed, starts)
// the server for every following message on that Workspace until a
// different language's `didOpen` arrives. A Workspace with files open in
// two languages at once would need per-file routing this doesn't do;
// research.md/the contract don't specify that level of multiplexing, so
// this is the smallest correct reading of "enruta por workspaceId +
// languageId" that covers the common case.
// lspActiveConnection is whichever (ctx, conn) pair most recently sent an
// Envelope for a Workspace's `lsp` channel — see `LSPProxy.pump`'s doc
// comment for why this needs to be looked up fresh on every relay
// instead of captured once. Same shape/reasoning as
// `internal/session.activeConnection` (that channel's own analogous
// fix) — not shared as a common type since the two packages don't
// otherwise depend on each other and this is two fields, not worth a
// new shared package for.
type lspActiveConnection struct {
	ctx  context.Context
	conn *Conn
}

type LSPProxy struct {
	command         LSPCommand
	ensureInstalled EnsureInstalledFunc

	mu             sync.Mutex
	servers        map[string]*lspServer          // key: workspaceID + "\x00" + languageID
	activeLanguage map[string]string              // workspaceID -> most recently opened languageID
	active         map[string]lspActiveConnection // workspaceID -> most recent (ctx, conn) pair
}

// EnsureInstalledFunc installs languageID's Language Server if it isn't
// already present — called before the very first spawn for a
// (workspaceID, languageID) pair, whether that's the up-front install
// from provisioning (FR-027, already done by then, so this is a no-op in
// practice) or a language nobody detected up front (FR-036). Takes
// workspaceID because — same as startLSPServer — installing has to
// happen inside that specific Workspace container, not on the Bridge
// Daemon's own host.
type EnsureInstalledFunc func(ctx context.Context, workspaceID, languageID string) error

// NewLSPProxy builds an LSPProxy that starts servers via command, first
// making sure each one is installed via ensureInstalled.
func NewLSPProxy(command LSPCommand, ensureInstalled EnsureInstalledFunc) *LSPProxy {
	return &LSPProxy{
		command:         command,
		ensureInstalled: ensureInstalled,
		servers:         make(map[string]*lspServer),
		activeLanguage:  make(map[string]string),
		active:          make(map[string]lspActiveConnection),
	}
}

// Handler returns the ws.Handler to register for ChannelLSP.
func (p *LSPProxy) Handler() Handler {
	return func(ctx context.Context, conn *Conn, env Envelope) {
		if env.WorkspaceID == nil {
			conn.SendError(env, "workspace-required", "el canal lsp requiere workspaceId", nil)
			return
		}
		workspaceID := *env.WorkspaceID

		// Recorded on every Envelope, not just the one that spawns a
		// server — see `pump`'s doc comment.
		p.mu.Lock()
		p.active[workspaceID] = lspActiveConnection{ctx: ctx, conn: conn}
		p.mu.Unlock()

		languageID, ok := languageIDFromDidOpen(env.Payload)
		if ok {
			p.mu.Lock()
			p.activeLanguage[workspaceID] = languageID
			p.mu.Unlock()
		} else {
			languageID, ok = p.currentLanguage(workspaceID)
			if !ok {
				conn.SendError(env, "lsp-no-active-session", "no hay una sesión LSP activa para este workspace", nil)
				return
			}
		}

		server, isNew, err := p.serverFor(ctx, workspaceID, languageID)
		if err != nil {
			conn.SendError(env, "lsp-launch-failed", err.Error(), nil)
			return
		}
		if isNew {
			go p.pump(workspaceID, server)
		}

		if err := server.write(env.Payload); err != nil {
			conn.SendError(env, "lsp-write-failed", err.Error(), nil)
		}
	}
}

func (p *LSPProxy) currentLanguage(workspaceID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	lang, ok := p.activeLanguage[workspaceID]
	return lang, ok
}

func (p *LSPProxy) serverFor(ctx context.Context, workspaceID, languageID string) (*lspServer, bool, error) {
	key := workspaceID + "\x00" + languageID
	p.mu.Lock()
	defer p.mu.Unlock()

	if server, ok := p.servers[key]; ok {
		return server, false, nil
	}
	if err := p.ensureInstalled(ctx, workspaceID, languageID); err != nil {
		return nil, false, fmt.Errorf("ws: install language server for %q: %w", languageID, err)
	}
	name, args, err := p.command(languageID)
	if err != nil {
		return nil, false, fmt.Errorf("ws: resolve language server for %q: %w", languageID, err)
	}
	server, err := startLSPServer(workspaceID, name, args)
	if err != nil {
		return nil, false, fmt.Errorf("ws: start language server for %q: %w", languageID, err)
	}
	p.servers[key] = server
	return server, true, nil
}

// pump streams every message the Language Server writes back to the
// client, on the same `lsp` channel and Workspace, until the process ends.
//
// Reads `p.active[workspaceID]` fresh on every message instead of a
// `conn`/`ctx` captured once at goroutine start — this goroutine is
// started only the *first* time a (workspaceID, languageID) server is
// spawned (`isNew` in `Handler`) and then lives for as long as that
// process does, but the WebSocket connection that happened to trigger
// that first spawn is under no obligation to live that long too (an app
// relaunch, a dropped connection, revisiting a Project, ...). Same bug,
// same fix as `internal/session.ShellSessions.pump` — see that one's
// doc comment for the full story (found live: Terminal reconnects never
// received PTY output again; this channel has the identical shape and
// was never actually exercised long enough in one sitting to notice it
// independently, but there's no reason to assume it doesn't have the
// exact same failure mode).
func (p *LSPProxy) pump(workspaceID string, server *lspServer) {
	for {
		payload, err := server.readMessage()
		if err != nil {
			return
		}
		p.mu.Lock()
		active, ok := p.active[workspaceID]
		p.mu.Unlock()
		if !ok {
			continue
		}
		_ = active.conn.SendPayload(active.ctx, uuid.NewString(), ChannelLSP, &workspaceID, json.RawMessage(payload))
	}
}

func languageIDFromDidOpen(payload json.RawMessage) (string, bool) {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			TextDocument struct {
				LanguageID string `json:"languageId"`
			} `json:"textDocument"`
		} `json:"params"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return "", false
	}
	if msg.Method != "textDocument/didOpen" || msg.Params.TextDocument.LanguageID == "" {
		return "", false
	}
	return msg.Params.TextDocument.LanguageID, true
}
