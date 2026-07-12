package ws

// requirementRef values match the examples in
// contracts/websocket-protocol.md's "Errores" section — the app uses these
// to render the specific diagnostic required by FR-004.
const (
	RequirementBridgeDaemonReachable = "bridge-daemon-reachable"
	RequirementContainerEngine       = "docker-engine"
	RequirementMosh                  = "mosh"
)

// NewRequirementError builds an ErrorPayload tied to a specific checklist
// requirement, so the client can show a diagnostic linked to the exact item
// that failed (FR-004) instead of a generic error.
func NewRequirementError(requirementRef, code, message string) ErrorPayload {
	ref := requirementRef
	return ErrorPayload{Code: code, Message: message, RequirementRef: &ref}
}
