package devpod

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes wires Provisioner and Lister onto mux. Like gitmanager's
// HTTP surface, this is stateless across requests: RetryStep's request
// carries the full prior Result back (the app already persists it locally
// via ProjectRepository), so the Bridge Daemon never needs to remember
// in-flight provisioning state itself.
func RegisterRoutes(mux *http.ServeMux, provisioner *Provisioner, lister *Lister) {
	mux.HandleFunc("POST /projects/provision", provisionHandler(provisioner))
	mux.HandleFunc("POST /projects/retry-step", retryStepHandler(provisioner))
	mux.HandleFunc("POST /projects/deprovision", deprovisionHandler(provisioner))
	mux.HandleFunc("GET /projects", listProjectsHandler(lister))
}

func provisionHandler(provisioner *Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		result := provisioner.Provision(r.Context(), req)
		writeJSON(w, result)
	}
}

func retryStepHandler(provisioner *Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Request Request  `json:"request"`
			Result  Result   `json:"result"`
			Step    StepName `json:"step"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		result := body.Result
		provisioner.RetryStep(r.Context(), body.Request, &result, body.Step)
		writeJSON(w, &result)
	}
}

// deprovisionHandler backs the App's "Eliminar" swipe on a Project
// (BridgeProvisioningService.deprovision) — tears down the devpod
// workspace and removes the cloned repository. Best-effort from the
// caller's side (the App deletes its own local record regardless of the
// outcome here, see ProjectListViewModel.confirmDeletion's doc comment),
// but this still reports a real error when both teardown steps fail, so a
// completely unreachable Bridge Daemon isn't silently treated the same as
// a clean deprovision.
func deprovisionHandler(provisioner *Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			WorkspaceID string `json:"workspaceId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := provisioner.Deprovision(r.Context(), body.WorkspaceID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// listProjectsHandler answers `GET /projects` — reconstructs which
// Projects already exist on this Host so a client whose own local record
// of them is gone (a reinstall, a second device) can discover them again.
// No owner-scoping query param, unlike GET /git-credentials: self-hosted
// has exactly one implicit owner per Host, and Lister has no notion of
// ownership to filter by in the first place (see Lister's own doc
// comment on what it can and can't recover).
func listProjectsHandler(lister *Lister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projects, err := lister.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, struct {
			Projects []ProjectInfo `json:"projects"`
		}{Projects: projects})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
