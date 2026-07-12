package gitmanager

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// GitOperationRequest is the `git` channel's daily-operation request
// payload (contracts/websocket-protocol.md → Canal `git`, "Operación
// diaria"). `clone` isn't handled here — Cloner streams that separately,
// before any Workspace/WebSocket session exists.
type GitOperationRequest struct {
	Phase string `json:"phase"` // "status"|"diff"|"stage"|"commit"|"push"|"pull"|"branch"|"merge-conflict"

	Staged bool     `json:"staged,omitempty"`
	Path   string   `json:"path,omitempty"`
	Paths  []string `json:"paths,omitempty"`

	Message string `json:"message,omitempty"`

	// CredentialSecretRef/CredentialKind: the app only ever holds a
	// credential's SecretRef, never its raw secret (FR-021) — same
	// pattern as devpod.Request.
	CredentialSecretRef string `json:"credentialSecretRef,omitempty"`
	CredentialKind      string `json:"credentialKind,omitempty"`

	BranchAction string `json:"branchAction,omitempty"` // "list"|"create"|"switch"
	BranchName   string `json:"branchName,omitempty"`

	ConflictAction     string `json:"conflictAction,omitempty"` // "list"|"resolve"|"stage"
	ConflictBlockIndex int    `json:"conflictBlockIndex,omitempty"`
	ConflictResolution string `json:"conflictResolution,omitempty"` // "mine"|"theirs"|"manual"
	ManualText         string `json:"manualText,omitempty"`
}

// GitOperationResponse is the `git` channel's daily-operation response
// payload — Phase always echoes what actually happened, which for `pull`
// is `"merge-conflict"` rather than `"pull"` when the merge left
// conflicts (FR-035), not a hard error.
type GitOperationResponse struct {
	Phase           string           `json:"phase"`
	Files           []GitFileStatus  `json:"files,omitempty"`
	Diff            string           `json:"diff,omitempty"`
	Hash            string           `json:"hash,omitempty"`
	Branches        []Branch         `json:"branches,omitempty"`
	ConflictedFiles []ConflictedFile `json:"conflictedFiles,omitempty"`
}

// ResolveSecretFunc resolves a credential's SecretRef to its actual
// secret value (gitmanager.SecretStore.Get) — see devpod.ResolveSecretFunc
// for the same pattern applied to provisioning.
type ResolveSecretFunc func(ctx context.Context, secretRef string) (string, error)

// Handler returns the ws.Handler to register for ws.ChannelGit's
// day-to-day operations (FR-032/FR-033/FR-035).
func (o *Operations) Handler(resolveSecret ResolveSecretFunc) ws.Handler {
	return func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {
		if env.WorkspaceID == nil {
			conn.SendError(env, "workspace-required", "el canal git requiere workspaceId", nil)
			return
		}
		workspaceID := *env.WorkspaceID

		var req GitOperationRequest
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			conn.SendError(env, "invalid-payload", err.Error(), nil)
			return
		}

		resp, err := o.dispatch(ctx, workspaceID, req, resolveSecret)
		if err != nil {
			conn.SendError(env, "git-operation-failed", err.Error(), nil)
			return
		}
		_ = conn.SendPayload(ctx, env.ID, ws.ChannelGit, &workspaceID, resp)
	}
}

func (o *Operations) dispatch(
	ctx context.Context, workspaceID string, req GitOperationRequest, resolveSecret ResolveSecretFunc,
) (GitOperationResponse, error) {
	switch req.Phase {
	case "status":
		files, err := o.Status(ctx, workspaceID)
		return GitOperationResponse{Phase: "status", Files: files}, err
	case "diff":
		diff, err := o.Diff(ctx, workspaceID, req.Staged, req.Path)
		return GitOperationResponse{Phase: "diff", Diff: diff}, err
	case "stage":
		err := o.Stage(ctx, workspaceID, req.Paths)
		return GitOperationResponse{Phase: "stage"}, err
	case "commit":
		hash, err := o.Commit(ctx, workspaceID, req.Message)
		return GitOperationResponse{Phase: "commit", Hash: hash}, err
	case "push":
		return o.dispatchPush(ctx, workspaceID, req, resolveSecret)
	case "pull":
		return o.dispatchPull(ctx, workspaceID, req, resolveSecret)
	case "branch":
		return o.dispatchBranch(ctx, workspaceID, req)
	case "merge-conflict":
		return o.dispatchConflict(ctx, workspaceID, req)
	default:
		return GitOperationResponse{}, fmt.Errorf("gitmanager: unknown phase %q", req.Phase)
	}
}

func (o *Operations) dispatchPush(
	ctx context.Context, workspaceID string, req GitOperationRequest, resolveSecret ResolveSecretFunc,
) (GitOperationResponse, error) {
	credential, err := credentialFor(ctx, req, resolveSecret)
	if err != nil {
		return GitOperationResponse{}, err
	}
	if err := o.Push(ctx, workspaceID, credential); err != nil {
		return GitOperationResponse{}, err
	}
	return GitOperationResponse{Phase: "push"}, nil
}

// dispatchPull reports a merge conflict left behind by `git pull` as
// `phase: "merge-conflict"`, not as an error — FR-035 expects the
// Desarrollador to resolve it block by block, not just see a failure.
func (o *Operations) dispatchPull(
	ctx context.Context, workspaceID string, req GitOperationRequest, resolveSecret ResolveSecretFunc,
) (GitOperationResponse, error) {
	credential, err := credentialFor(ctx, req, resolveSecret)
	if err != nil {
		return GitOperationResponse{}, err
	}
	if pullErr := o.Pull(ctx, workspaceID, credential); pullErr != nil {
		conflicted, conflictErr := o.ConflictedFiles(ctx, workspaceID)
		if conflictErr == nil && len(conflicted) > 0 {
			return GitOperationResponse{Phase: "merge-conflict", ConflictedFiles: conflicted}, nil
		}
		return GitOperationResponse{}, pullErr
	}
	return GitOperationResponse{Phase: "pull"}, nil
}

func (o *Operations) dispatchBranch(
	ctx context.Context, workspaceID string, req GitOperationRequest,
) (GitOperationResponse, error) {
	switch req.BranchAction {
	case "create":
		if err := o.CreateBranch(ctx, workspaceID, req.BranchName); err != nil {
			return GitOperationResponse{}, err
		}
	case "switch":
		if err := o.SwitchBranch(ctx, workspaceID, req.BranchName); err != nil {
			return GitOperationResponse{}, err
		}
	case "list", "":
	default:
		return GitOperationResponse{}, fmt.Errorf("gitmanager: unknown branch action %q", req.BranchAction)
	}
	branches, err := o.Branches(ctx, workspaceID)
	return GitOperationResponse{Phase: "branch", Branches: branches}, err
}

func (o *Operations) dispatchConflict(
	ctx context.Context, workspaceID string, req GitOperationRequest,
) (GitOperationResponse, error) {
	switch req.ConflictAction {
	case "resolve":
		resolution := ConflictResolution(req.ConflictResolution)
		if err := o.ResolveBlock(ctx, workspaceID, req.Path, req.ConflictBlockIndex, resolution, req.ManualText); err != nil {
			return GitOperationResponse{}, err
		}
	case "stage":
		if err := o.StageResolvedFile(ctx, workspaceID, req.Path); err != nil {
			return GitOperationResponse{}, err
		}
	case "list", "":
	default:
		return GitOperationResponse{}, fmt.Errorf("gitmanager: unknown conflict action %q", req.ConflictAction)
	}
	conflicted, err := o.ConflictedFiles(ctx, workspaceID)
	return GitOperationResponse{Phase: "merge-conflict", ConflictedFiles: conflicted}, err
}

func credentialFor(ctx context.Context, req GitOperationRequest, resolveSecret ResolveSecretFunc) (*CloneCredential, error) {
	if req.CredentialSecretRef == "" {
		return nil, nil
	}
	secret, err := resolveSecret(ctx, req.CredentialSecretRef)
	if err != nil {
		return nil, fmt.Errorf("gitmanager: resolve credential secret: %w", err)
	}
	return &CloneCredential{Kind: CredentialKind(req.CredentialKind), Secret: secret}, nil
}
