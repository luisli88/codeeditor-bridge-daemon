package devpod

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes wires Provisioner onto mux. Like gitmanager's HTTP
// surface, this is stateless across requests: RetryStep's request carries
// the full prior Result back (the app already persists it locally via
// ProjectRepository), so the Bridge Daemon never needs to remember
// in-flight provisioning state itself.
func RegisterRoutes(mux *http.ServeMux, provisioner *Provisioner) {
	mux.HandleFunc("POST /projects/provision", provisionHandler(provisioner))
	mux.HandleFunc("POST /projects/retry-step", retryStepHandler(provisioner))
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
