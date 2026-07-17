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
	"time"

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

	// Guards handshakeReplayID/handshakeReplayDone below — unlike before,
	// these are now genuinely accessed from two goroutines concurrently
	// (`replayHandshake`, called from `Handler`'s goroutine, and `pump`'s
	// own goroutine), since `replayHandshake` has to *wait* for `pump` to
	// observe the reply instead of the two being safely sequenced by a
	// single happens-before edge the way they used to be.
	handshakeMu sync.Mutex
	// handshakeReplayID, if set, is the JSON-RPC id of an `initialize`
	// request `replayHandshake` wrote to this server on the client's
	// behalf (see that function's doc comment) — `pump` swallows the one
	// response carrying this id instead of relaying it to the client,
	// which already got its own (synthetic) `initialize` response
	// earlier and would treat a second one for the same id as a stray.
	handshakeReplayID string
	// handshakeReplayDone is closed by `pump` the moment it sees the
	// response matching handshakeReplayID — `replayHandshake` blocks on
	// it before sending the replayed `initialized`, since a real LSP
	// client always waits for `initialize`'s response before proceeding
	// and skipping that wait is what caused the bug this exists to fix
	// (see `replayHandshake`'s doc comment).
	handshakeReplayDone chan struct{}
}

// setHandshakeReplay records id as the JSON-RPC id `pump` should watch
// for and treat as this server's own (not the client's) handshake reply.
func (s *lspServer) setHandshakeReplay(id string, done chan struct{}) {
	s.handshakeMu.Lock()
	s.handshakeReplayID = id
	s.handshakeReplayDone = done
	s.handshakeMu.Unlock()
}

// matchHandshakeReplay reports whether id is the one `replayHandshake` is
// waiting on — if so, clears it (so it's only ever matched once) and
// returns the channel to close.
func (s *lspServer) matchHandshakeReplay(id string) (chan struct{}, bool) {
	s.handshakeMu.Lock()
	defer s.handshakeMu.Unlock()
	if s.handshakeReplayID == "" || s.handshakeReplayID != id {
		return nil, false
	}
	done := s.handshakeReplayDone
	s.handshakeReplayID = ""
	s.handshakeReplayDone = nil
	return done, true
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

// lspHandshake buffers a Workspace's real `initialize`/`initialized`
// messages (answered synthetically to the client the moment they arrive —
// see `Handler`) so `replayHandshake` can send the *real* client-supplied
// params (rootUri, capabilities, ...) to whichever Language Server process
// gets spawned first, keeping that real subprocess's own handshake honest
// even though the client itself heard back long before any subprocess
// existed. Also buffers every currently-open document's `didOpen` — see
// `openDocuments`'s own doc comment for why that's not optional.
type lspHandshake struct {
	initializePayload  json.RawMessage
	initializedPayload json.RawMessage // nil until `initialized` arrives
	// openDocuments holds the most recent `didOpen` payload per URI,
	// removed on `didClose` — replayed into a respawned server the same
	// way initialize/initialized are (`replayHandshake`). Without this, a
	// Language Server that dies mid-session (confirmed live: a real
	// process can be SIGKILLed under memory pressure, e.g. `claude`
	// running heavy in the same Workspace's Terminal) gets a fresh
	// replacement on the next request — but that replacement has never
	// heard of any file the Desarrollador already had open, so completion/
	// diagnostics/etc. against it resolve nothing, silently, forever
	// (no error either — the server just genuinely has no document at
	// that URI). Exactly the "autocompletar dejó de funcionar de repente"
	// symptom, confirmed live: the respawned process was real and healthy
	// (low CPU, freshly started), it simply never got taught the file
	// existed.
	openDocuments map[string]json.RawMessage
}

type LSPProxy struct {
	command         LSPCommand
	ensureInstalled EnsureInstalledFunc

	mu             sync.Mutex
	servers        map[string]*lspServer          // key: workspaceID + "\x00" + languageID
	activeLanguage map[string]string              // workspaceID -> most recently opened languageID
	active         map[string]lspActiveConnection // workspaceID -> most recent (ctx, conn) pair
	handshake      map[string]*lspHandshake       // workspaceID -> buffered initialize/initialized to replay into each newly spawned server
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
		handshake:       make(map[string]*lspHandshake),
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

		// `initialize`/`initialized` are LSP's own handshake, and a
		// spec-compliant client always sends `initialize` *before*
		// anything else — including the very first `textDocument/
		// didOpen` this proxy relies on to learn which language's
		// server to route to. Routing them like every other message
		// would mean rejecting that first `initialize` outright (no
		// language known yet) with `lsp-no-active-session`, which a
		// real client can't recover from: `initialize` blocks
		// everything else the client sends until it gets a response,
		// so the `didOpen` that would've told us the language never
		// arrives either — a deadlock, not just a dropped message.
		// Answered here instead, synthetically, without forwarding to
		// any subprocess: this app never inspects the capabilities a
		// real server would've negotiated, so an honest "nothing
		// negotiated yet" result satisfies the handshake contract
		// (LSPClientService.connect awaits exactly this) while leaving
		// the actual language server spawn deferred to the first real
		// `didOpen`, same as before. The real payloads are buffered so
		// `replayHandshake` can still give whichever subprocess gets
		// spawned first the *real* handshake it needs (rootUri,
		// capabilities, ...) — a real Language Server that never sees
		// its own `initialize` won't have activated its language
		// service by the time `didOpen` arrives, so completion/etc.
		// would fail even though the routing deadlock is gone.
		if method, ok := messageMethod(env.Payload); ok {
			switch method {
			case "initialize":
				p.mu.Lock()
				p.handshake[workspaceID] = &lspHandshake{
					initializePayload: env.Payload,
					openDocuments:     make(map[string]json.RawMessage),
				}
				p.mu.Unlock()
				respondToInitialize(ctx, conn, env, workspaceID)
				return
			case "initialized":
				p.mu.Lock()
				if hs, ok := p.handshake[workspaceID]; ok {
					hs.initializedPayload = env.Payload
				}
				p.mu.Unlock()
				return // notification — no response expected, nothing to forward yet
			case "textDocument/didClose":
				if uri, ok := textDocumentURI(env.Payload); ok {
					p.mu.Lock()
					if hs, ok := p.handshake[workspaceID]; ok {
						delete(hs.openDocuments, uri)
					}
					p.mu.Unlock()
				}
				// Falls through to normal routing below — the close still
				// has to reach whichever server is currently live.
			}
		}

		languageID, isDidOpen := languageIDFromDidOpen(env.Payload)
		if isDidOpen {
			p.mu.Lock()
			p.activeLanguage[workspaceID] = languageID
			p.mu.Unlock()
		} else {
			var ok bool
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
			// `pump` now starts *before* the replay, not after — the
			// replay has to actually wait for the subprocess's own
			// response to the replayed `initialize` (see
			// `replayHandshake`'s doc comment for why), which needs
			// something already reading stdout to ever observe that
			// response and signal it.
			go p.pump(workspaceID, languageID, server)
			p.replayHandshake(workspaceID, server)
		}

		// Recorded only *after* any replay above already ran — replaying
		// "every document already open before this request" must never
		// include the very `didOpen` this request itself is, or a brand
		// new server would receive it twice (once via replay, once via
		// the write below).
		if isDidOpen {
			if uri, ok := textDocumentURI(env.Payload); ok {
				p.mu.Lock()
				if hs, exists := p.handshake[workspaceID]; exists {
					hs.openDocuments[uri] = env.Payload
				}
				p.mu.Unlock()
			}
		}

		if err := server.write(env.Payload); err != nil {
			conn.SendError(env, "lsp-write-failed", err.Error(), nil)
		}
	}
}

// replayHandshake sends server the real `initialize` (and, if it already
// arrived, `initialized`) the client sent earlier for workspaceID — see
// `Handler`'s doc comment on why the client itself was already answered
// synthetically and can't just be forwarded the subprocess's own response
// for the same request id. The replayed `initialize` gets a fresh id
// (`lspServer.handshakeReplayID`) so `pump` can recognize and swallow its
// response instead of relaying a second, stray answer to the client.
// No-ops if this Workspace's client never sent `initialize` (e.g. a
// synthetic/manual test) — server behaves exactly as it did before this
// replay mechanism existed.
//
// Blocks until `pump` confirms the subprocess actually answered the
// replayed `initialize` (or 30s elapses) before sending `initialized` —
// this used to fire both writes back-to-back with no wait in between,
// which a real LSP client never does (it always waits for `initialize`'s
// response first). That race stayed invisible against a fast, trivial
// project, but confirmed live against a real one (a Next.js + Amplify
// project with a real tsconfig.json and a large node_modules): `vtsls`
// stopped registering its ordinary request handlers when `initialized`/
// the caller's real `didOpen` arrived while it was still synchronously
// processing `initialize`, so every later `textDocument/completion` in
// that same session came back `-32601 Unhandled method` instead of ever
// answering — exactly the "no aparece nada" symptom, on a real project,
// that a self-contained one-file test could never reproduce.
func (p *LSPProxy) replayHandshake(workspaceID string, server *lspServer) {
	p.mu.Lock()
	hs, ok := p.handshake[workspaceID]
	p.mu.Unlock()
	if !ok {
		return
	}

	replayID := uuid.NewString()
	payload, err := withReplayID(hs.initializePayload, replayID)
	if err != nil {
		return
	}
	done := make(chan struct{})
	server.setHandshakeReplay(replayID, done)
	if err := server.write(payload); err != nil {
		return
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}
	if hs.initializedPayload != nil {
		_ = server.write(hs.initializedPayload)
	}

	// Re-teaches the freshly (re)spawned server about every file that was
	// already open before it existed — see `lspHandshake.openDocuments`'s
	// doc comment for the real symptom this fixes. Order across different
	// URIs doesn't matter to the LSP spec; each `didOpen` only concerns
	// its own document.
	p.mu.Lock()
	openDocuments := make([]json.RawMessage, 0, len(hs.openDocuments))
	for _, payload := range hs.openDocuments {
		openDocuments = append(openDocuments, payload)
	}
	p.mu.Unlock()
	for _, docPayload := range openDocuments {
		_ = server.write(docPayload)
	}
}

// textDocumentURI extracts a JSON-RPC message's params.textDocument.uri
// without otherwise interpreting it — shared by the didOpen buffering and
// didClose eviction above, both of which only care about this one field
// regardless of the message's other params.
func textDocumentURI(payload json.RawMessage) (string, bool) {
	var msg struct {
		Params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		} `json:"params"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || msg.Params.TextDocument.URI == "" {
		return "", false
	}
	return msg.Params.TextDocument.URI, true
}

// withReplayID returns payload with its top-level JSON-RPC "id" replaced
// by id, leaving every other field untouched — used to give a replayed
// `initialize` request an id `pump` can recognize without colliding with
// any id the real client itself used.
func withReplayID(payload json.RawMessage, id string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	quotedID, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	fields["id"] = quotedID
	return json.Marshal(fields)
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
//
// `readMessage` returning an error means the process is gone (crashed,
// killed — confirmed live: a real `vtsls` can die from `SIGKILL` under
// memory pressure, entirely independent of anything the client does) or
// its stdout closed. Before this called `evictDeadServer`, that left the
// dead entry in `p.servers` forever: every future request for this same
// (workspaceID, languageID) kept being routed to a handle that could
// never respond again — writes to its closed stdin pipe either error
// immediately or vanish, and either way no client request against it
// ever completes again, silently, until the whole daemon restarts.
func (p *LSPProxy) pump(workspaceID, languageID string, server *lspServer) {
	defer p.evictDeadServer(workspaceID, languageID, server)
	for {
		payload, err := server.readMessage()
		if err != nil {
			return
		}
		if id, ok := messageID(payload); ok {
			if done, matched := server.matchHandshakeReplay(id); matched {
				if done != nil {
					close(done) // wakes replayHandshake's select, waiting to send `initialized`
				}
				continue
			}
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

// evictDeadServer removes server from p.servers if it's still the
// current entry for (workspaceID, languageID) — the `server ==` check
// guards against a narrow race where a newer server has already replaced
// this one (e.g. this exact (workspaceID, languageID) got re-spawned
// between this pump's last read and its eviction running), which would
// otherwise evict the wrong (live) entry.
func (p *LSPProxy) evictDeadServer(workspaceID, languageID string, server *lspServer) {
	key := workspaceID + "\x00" + languageID
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.servers[key] == server {
		delete(p.servers, key)
	}
}

// messageMethod peeks at a JSON-RPC message's `method` field without
// otherwise interpreting it — used to special-case the handshake
// (`initialize`/`initialized`) before language-based routing applies.
func messageMethod(payload json.RawMessage) (string, bool) {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || msg.Method == "" {
		return "", false
	}
	return msg.Method, true
}

// messageID extracts a JSON-RPC message's string "id" field — used only to
// recognize `pump`'s own replayed-handshake response (always a UUID string
// this proxy itself generates via `replayHandshake`), not as general
// id extraction: a real client's ids are just as often numbers, which this
// deliberately doesn't attempt to decode.
func messageID(payload json.RawMessage) (string, bool) {
	var msg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || msg.ID == "" {
		return "", false
	}
	return msg.ID, true
}

// respondToInitialize answers an `initialize` request with a minimal,
// honest "nothing negotiated" result, echoing the request's own JSON-RPC
// `id` (not `env.ID`, the unrelated WebSocket envelope id) so the caller's
// pending request resolves.
func respondToInitialize(ctx context.Context, conn *Conn, env Envelope, workspaceID string) {
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(env.Payload, &msg); err != nil || len(msg.ID) == 0 {
		return
	}
	_ = conn.SendPayload(ctx, uuid.NewString(), ChannelLSP, &workspaceID, map[string]any{
		"jsonrpc": "2.0",
		"id":      msg.ID,
		"result":  map[string]any{"capabilities": map[string]any{}},
	})
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
