package session

import (
	"context"
	"regexp"
	"sync"

	"github.com/google/uuid"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// AuthEvent mirrors the `claude` channel's payload
// (contracts/auth-flows.md → Canal `claude`) — FR-047 requires the
// Desarrollador to authenticate their own personal Claude Code session
// before the CLI accepts its first command in each container session.
type AuthEvent struct {
	Type             string `json:"type"` // "auth-required" | "auth-complete"
	LoginURL         string `json:"loginUrl,omitempty"`
	VerificationCode string `json:"verificationCode,omitempty"`
}

// loginPromptPattern/successPattern are a best-effort heuristic for
// recognizing `claude`'s login device-flow prompt (a URL alongside a
// short verification code — the same general shape as GitHub CLI's and
// Docker CLI's device-flow prompts) and its success message in the PTY
// output stream.
//
// **Not verified against the real CLI**: this project doesn't have
// byte-exact knowledge of `claude`'s actual prompt/success text, so
// these patterns are a reasonable approximation, not a confirmed match.
// Real integration testing against the installed `claude` CLI (itself
// unverified in this environment — see bootstrap/claude.go) is needed
// before relying on this detector in production.
var (
	loginPromptPattern = regexp.MustCompile(`(https?://\S+).{0,80}?\b([A-Z0-9]{4,10}-?[A-Z0-9]{0,10})\b`)
	successPattern     = regexp.MustCompile(`(?i)(authenticated|logged in|login successful)`)
)

// AuthDetector watches a Workspace's tmux/`claude` session output
// (shared with the `shell` channel, via ShellSessions.SetOutputWatcher)
// for the CLI's authentication prompt and completion, translating it
// into `claude` channel Envelopes — the app never reads the Terminal
// session directly to get this signal.
//
// FR-049/FR-090: `claude` only ever runs attached to this same
// interactive tmux PTY — nowhere in this package (or anywhere else in
// the Bridge Daemon) is `claude` invoked as a separate subprocess with
// `-p`/headless flags. There is no code path that could run it
// autonomously; this is a structural guarantee, not a runtime check.
type AuthDetector struct {
	mu           sync.Mutex
	awaitingAuth map[string]bool // workspaceID -> auth-required signaled, still waiting for auth-complete
}

// NewAuthDetector builds an empty AuthDetector.
func NewAuthDetector() *AuthDetector {
	return &AuthDetector{awaitingAuth: make(map[string]bool)}
}

// Watch returns an OutputWatcher (session/shell.go) that scans every PTY
// chunk and sends the resulting AuthEvent, if any, over ws.ChannelClaude.
func (d *AuthDetector) Watch() OutputWatcher {
	return func(ctx context.Context, conn *ws.Conn, workspaceID string, data []byte) {
		event := d.scan(workspaceID, data)
		if event == nil {
			return
		}
		_ = conn.SendPayload(ctx, uuid.NewString(), ws.ChannelClaude, &workspaceID, event)
	}
}

// scan inspects one chunk of PTY output for workspaceID and returns the
// AuthEvent to signal, if any — nil when nothing new was detected.
func (d *AuthDetector) scan(workspaceID string, chunk []byte) *AuthEvent {
	text := string(chunk)

	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.awaitingAuth[workspaceID] {
		if match := loginPromptPattern.FindStringSubmatch(text); match != nil {
			d.awaitingAuth[workspaceID] = true
			return &AuthEvent{Type: "auth-required", LoginURL: match[1], VerificationCode: match[2]}
		}
		return nil
	}

	if successPattern.MatchString(text) {
		delete(d.awaitingAuth, workspaceID)
		return &AuthEvent{Type: "auth-complete"}
	}
	return nil
}
