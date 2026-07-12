package ws

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
)

// ChecklistStatus mirrors data-model.md's Host.requirementsChecklist value
// enum.
type ChecklistStatus string

const (
	StatusPending       ChecklistStatus = "pending"
	StatusVerified      ChecklistStatus = "verified"
	StatusNotApplicable ChecklistStatus = "not-applicable"
)

// ChecklistItem is one row of the Host requirements checklist (FR-002).
type ChecklistItem struct {
	Name   string          `json:"name"`
	Status ChecklistStatus `json:"status"`
	Error  *ErrorPayload   `json:"error,omitempty"`
}

// Checker runs one checklist item.
type Checker func(ctx context.Context) ChecklistItem

// CheckBridgeDaemonReachable is trivially satisfied — if this code is
// running, the Bridge Daemon is reachable.
func CheckBridgeDaemonReachable(ctx context.Context) ChecklistItem {
	return ChecklistItem{Name: "bridge-daemon-reachable", Status: StatusVerified}
}

// CheckContainerEngine verifies Docker (or a compatible engine) is
// reachable, per FR-003's "verificación del motor de contenedores".
func CheckContainerEngine(ctx context.Context) ChecklistItem {
	const name = "container-engine"
	if _, err := exec.LookPath("docker"); err != nil {
		return ChecklistItem{
			Name:   name,
			Status: StatusPending,
			Error:  errorPtr(NewRequirementError(RequirementContainerEngine, "docker-not-found", "el motor de contenedores (docker) no está instalado en el Host")),
		}
	}
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		return ChecklistItem{
			Name:   name,
			Status: StatusPending,
			Error:  errorPtr(NewRequirementError(RequirementContainerEngine, "docker-not-running", "docker está instalado pero el daemon no responde")),
		}
	}
	return ChecklistItem{Name: name, Status: StatusVerified}
}

// DefaultChecklist is the sequence FR-003/FR-050 require: conexión (implicit
// — this handler only runs once the app already reached the Bridge Daemon),
// proceso intermediario (self), motor de contenedores, mosh.
func DefaultChecklist(moshUDPPortRange string) []Checker {
	return []Checker{
		CheckBridgeDaemonReachable,
		CheckContainerEngine,
		func(ctx context.Context) ChecklistItem { return CheckMosh(ctx, moshUDPPortRange) },
	}
}

// HandshakeHandler streams each Checker's result as newline-delimited JSON,
// flushing after every item, so the client can render "progreso de cada
// paso" (FR-003) instead of waiting for the whole sequence to finish.
func HandshakeHandler(checklist []Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		writer := bufio.NewWriter(w)

		for _, check := range checklist {
			item := check(r.Context())
			data, err := json.Marshal(item)
			if err != nil {
				continue
			}
			_, _ = writer.Write(data)
			_, _ = writer.WriteString("\n")
			_ = writer.Flush()
			flusher.Flush()
		}
	}
}
