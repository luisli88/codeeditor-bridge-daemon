package gitmanager

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes wires CredentialManager onto mux (FR-015/FR-016/FR-020).
// Credential management is a one-shot request/response, not a stream, and
// happens before any Workspace exists, so it rides plain HTTPS rather than
// the `git` WebSocket channel (contracts/websocket-protocol.md, which only
// covers clone progress and in-Workspace git operations).
func RegisterRoutes(mux *http.ServeMux, manager *CredentialManager) {
	mux.HandleFunc("POST /git-credentials/ssh-key/generate", generateSSHKeyHandler(manager))
	mux.HandleFunc("POST /git-credentials/ssh-key/import", importSSHKeyHandler(manager))
	mux.HandleFunc("POST /git-credentials/pat", registerPATHandler(manager))
	mux.HandleFunc("POST /git-credentials/github-derived", registerGitHubDerivedHandler(manager))
	mux.HandleFunc("POST /git-credentials/reauthenticate", reauthenticateHandler(manager))
	mux.HandleFunc("POST /git-credentials/verify", verifyHandler(manager))
}

type credentialResponse struct {
	Credential Credential `json:"credential"`
	PublicKey  string     `json:"publicKey,omitempty"`
}

func generateSSHKeyHandler(manager *CredentialManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OwnerUserID string `json:"ownerUserId"`
			Alias       string `json:"alias"`
			Domain      string `json:"domain"`
		}
		if !decodeRequest(w, r, &req) {
			return
		}
		cred, publicKey, err := manager.GenerateSSHKey(r.Context(), req.OwnerUserID, req.Alias, req.Domain)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, credentialResponse{Credential: cred, PublicKey: publicKey})
	}
}

func importSSHKeyHandler(manager *CredentialManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OwnerUserID   string `json:"ownerUserId"`
			Alias         string `json:"alias"`
			Domain        string `json:"domain"`
			PrivateKeyPEM string `json:"privateKeyPem"`
		}
		if !decodeRequest(w, r, &req) {
			return
		}
		cred, err := manager.ImportSSHKey(r.Context(), req.OwnerUserID, req.Alias, req.Domain, req.PrivateKeyPEM)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, credentialResponse{Credential: cred})
	}
}

func registerPATHandler(manager *CredentialManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OwnerUserID string `json:"ownerUserId"`
			Alias       string `json:"alias"`
			Domain      string `json:"domain"`
			Token       string `json:"token"`
		}
		if !decodeRequest(w, r, &req) {
			return
		}
		cred, err := manager.RegisterPAT(r.Context(), req.OwnerUserID, req.Alias, req.Domain, req.Token)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, credentialResponse{Credential: cred})
	}
}

func registerGitHubDerivedHandler(manager *CredentialManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OwnerUserID string `json:"ownerUserId"`
			AccessToken string `json:"accessToken"`
		}
		if !decodeRequest(w, r, &req) {
			return
		}
		cred, err := manager.RegisterGitHubDerived(r.Context(), req.OwnerUserID, req.AccessToken)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, credentialResponse{Credential: cred})
	}
}

func reauthenticateHandler(manager *CredentialManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Credential Credential `json:"credential"`
			NewSecret  string     `json:"newSecret"`
		}
		if !decodeRequest(w, r, &req) {
			return
		}
		cred := req.Credential
		if err := manager.Reauthenticate(r.Context(), &cred, req.NewSecret); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, credentialResponse{Credential: cred})
	}
}

func verifyHandler(manager *CredentialManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Credential Credential `json:"credential"`
		}
		if !decodeRequest(w, r, &req) {
			return
		}
		cred := req.Credential
		if err := manager.Verify(r.Context(), &cred); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, credentialResponse{Credential: cred})
	}
}

func decodeRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusUnprocessableEntity)
}
