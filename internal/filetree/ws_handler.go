package filetree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// Handler returns the ws.Handler to register for ws.ChannelFS.
func (b *Browser) Handler() ws.Handler {
	return func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {
		if env.WorkspaceID == nil {
			conn.SendError(env, "workspace-required", "el canal fs requiere workspaceId", nil)
			return
		}
		workspaceID := *env.WorkspaceID

		var req Request
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			conn.SendError(env, "invalid-payload", err.Error(), nil)
			return
		}

		switch req.Action {
		case "list":
			entries, err := b.List(workspaceID, req.Path)
			if err != nil {
				conn.SendError(env, fsErrorCode(err), err.Error(), nil)
				return
			}
			_ = conn.SendPayload(ctx, env.ID, ws.ChannelFS, &workspaceID, Response{Action: "list", Path: req.Path, Entries: entries})
		case "read":
			content, err := b.Read(workspaceID, req.Path)
			if err != nil {
				conn.SendError(env, fsErrorCode(err), err.Error(), nil)
				return
			}
			_ = conn.SendPayload(ctx, env.ID, ws.ChannelFS, &workspaceID, Response{Action: "read", Path: req.Path, Content: content})
		case "write":
			if err := b.Write(workspaceID, req.Path, req.Content); err != nil {
				conn.SendError(env, fsErrorCode(err), err.Error(), nil)
				return
			}
			_ = conn.SendPayload(ctx, env.ID, ws.ChannelFS, &workspaceID, Response{Action: "write", Path: req.Path})
		default:
			conn.SendError(env, "unknown-action", fmt.Sprintf("acción de fs desconocida: %q", req.Action), nil)
		}
	}
}

// fsErrorCode uses distinct codes per failure class, matching
// lsp_proxy.go/debug/adapters.go's convention (not git's single blanket
// code, which is explained by dispatch already branching into
// differently-shaped successful responses per phase, not by a house style
// of "always one code") — "path escapes workspace" in particular is
// security-relevant and shouldn't look like a generic "not found".
func fsErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidPath):
		return "fs-invalid-path"
	case errors.Is(err, ErrNotFound):
		return "fs-not-found"
	case errors.Is(err, ErrNotAFile):
		return "fs-not-a-file"
	case errors.Is(err, ErrNotADirectory):
		return "fs-not-a-directory"
	default:
		return "fs-operation-failed"
	}
}
