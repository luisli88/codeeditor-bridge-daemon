// Package ws implements the single multiplexed WebSocket that the client
// speaks to the Bridge Daemon over — see
// specs/001-core-development-flows/contracts/websocket-protocol.md in the
// coordinator repo for the wire contract this package implements.
package ws

import "encoding/json"

// Channel identifies which subsystem a message belongs to. The Bridge Daemon
// never reorders messages within a channel for a given WorkspaceID.
type Channel string

const (
	ChannelLSP          Channel = "lsp"
	ChannelShell        Channel = "shell"
	ChannelClaude       Channel = "claude"
	ChannelFS           Channel = "fs"
	ChannelRun          Channel = "run"
	ChannelGit          Channel = "git"
	ChannelDebug        Channel = "debug"
	ChannelEntitlements Channel = "entitlements"
)

// Envelope is the top-level frame every message uses, in both directions.
type Envelope struct {
	ID          string          `json:"id"`
	Channel     Channel         `json:"channel"`
	WorkspaceID *string         `json:"workspaceId"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Error       *ErrorPayload   `json:"error,omitempty"`
}

// ErrorPayload is the shape any channel can respond with on failure.
// RequirementRef is what the client uses for the specific diagnostic
// required by FR-004 (e.g. "docker-engine", "bridge-daemon-reachable").
type ErrorPayload struct {
	Code           string  `json:"code"`
	Message        string  `json:"message"`
	RequirementRef *string `json:"requirementRef,omitempty"`
}
