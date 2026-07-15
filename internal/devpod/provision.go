package devpod

import (
	"context"
	"fmt"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/bootstrap"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/entitlements"
)

// StepName mirrors one entry of data-model.md → Proyecto.provisioningSteps.
type StepName string

const (
	StepEntitlementsGate StepName = "entitlements-gate"
	StepClone            StepName = "clone"
	StepDevcontainer     StepName = "devcontainer"
	StepDevPodUp         StepName = "devpod-up"
	StepLSPBootstrap     StepName = "lsp-bootstrap"
	StepClaudeBootstrap  StepName = "claude-bootstrap"
)

// orderedSteps is FR-027's provisioning sequence. The Entitlements Gate
// runs first: FR-024 says to stop "antes de iniciar cualquier
// aprovisionamiento", so nothing — not even the clone — starts on a
// quota-exceeded account.
var orderedSteps = []StepName{
	StepEntitlementsGate, StepClone, StepDevcontainer, StepDevPodUp, StepLSPBootstrap, StepClaudeBootstrap,
}

// StepStatus mirrors data-model.md's provisioningSteps[].status enum.
type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepRunning StepStatus = "running"
	StepDone    StepStatus = "done"
	StepError   StepStatus = "error"
)

// ProvisioningStep is one entry of Proyecto.provisioningSteps. Error is
// only set when Status is StepError — without it, the app had no way to
// show *why* a step failed, only that it did (a bare red X with nothing
// else to go on).
type ProvisioningStep struct {
	Name   StepName   `json:"name"`
	Status StepStatus `json:"status"`
	Error  *string    `json:"error,omitempty"`
}

// Project status values, mirroring data-model.md → Proyecto.status.
const (
	StatusPendingCredential = "pending-credential"
	StatusCloning           = "cloning"
	StatusProvisioning      = "provisioning"
	StatusReady             = "ready"
	StatusError             = "error"
)

// Request is everything Provision needs to bring a Proyecto up. The app
// never holds a credential's raw secret (FR-021) — only its SecretRef —
// so Request carries that reference, and Provisioner resolves the actual
// secret itself via resolveSecret right before cloning.
type Request struct {
	WorkspaceID string `json:"workspaceId"`
	OwnerUserID string `json:"ownerUserId"`
	// HostKind is "self-hosted" | "managed" — passed straight to entitlements.Gate.
	HostKind      string `json:"hostKind"`
	RepositoryURL string `json:"repositoryUrl"`
	// CredentialSecretRef is "" for a public repository.
	CredentialSecretRef string `json:"credentialSecretRef,omitempty"`
	// CredentialKind is "ssh-key" | "personal-access-token" | "github-oauth-derived".
	CredentialKind string `json:"credentialKind,omitempty"`
}

// Result is the provisioning-relevant subset of Proyecto — Status,
// ProvisioningSteps, and (once detected/generated) DevcontainerConfig.
type Result struct {
	Status             string                     `json:"status"`
	Steps              []ProvisioningStep         `json:"provisioningSteps"`
	DevcontainerConfig map[string]any             `json:"devcontainerConfig,omitempty"`
	LanguageServers    []bootstrap.LanguageServer `json:"languageServers,omitempty"`
	Reason             *string                    `json:"reason,omitempty"` // entitlements.Reason* on a gate denial
}

func newResult() *Result {
	steps := make([]ProvisioningStep, len(orderedSteps))
	for i, name := range orderedSteps {
		steps[i] = ProvisioningStep{Name: name, Status: StepPending}
	}
	return &Result{Status: StatusCloning, Steps: steps}
}

func (r *Result) stepIndex(name StepName) int {
	for i, s := range r.Steps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// CloneCredential mirrors gitmanager.CloneCredential's shape without
// importing gitmanager directly — keeps Provisioner decoupled from
// gitmanager's package, wired together by whatever constructs a
// Provisioner (main.go).
type CloneCredential struct {
	Kind   string
	Secret string
}

type (
	CloneFunc         func(ctx context.Context, workspaceID, repositoryURL string, credential *CloneCredential) error
	WorkspacePathFunc func(workspaceID string) string
	DevcontainerFunc  func(workspacePath string) (map[string]any, error)
	LanguagesFunc     func(workspacePath string) []string
	DevPodUpFunc      func(ctx context.Context, workspacePath string) error
	// LSPInstallFunc takes workspaceID (not workspacePath like the steps
	// before it) because installing happens *inside* the devpod Workspace
	// container via `devpod ssh <workspaceID>` — devpod addresses a
	// Workspace by ID, not by the host-side clone path.
	LSPInstallFunc func(ctx context.Context, workspaceID string, languages []string) []bootstrap.LanguageServer
	// ClaudeInstallFunc takes workspaceID for the same reason as
	// LSPInstallFunc above.
	ClaudeInstallFunc func(ctx context.Context, workspaceID string) error
	// ResolveSecretFunc resolves a credential's SecretRef to its actual
	// secret value (gitmanager.SecretStore.Get) — the app only ever holds
	// SecretRef (FR-021), never the secret itself, so Provisioner must
	// resolve it server-side right before the clone step needs it.
	ResolveSecretFunc func(ctx context.Context, secretRef string) (string, error)
)

// Provisioner runs FR-027's sequence and FR-028's per-step retry. Every
// dependency is a function value rather than a concrete package import —
// the same testable-seam convention as entitlements.Store/gitmanager.Validator
// elsewhere in this module.
type Provisioner struct {
	gate          *entitlements.Gate
	workspacePath WorkspacePathFunc
	clone         CloneFunc
	resolveSecret ResolveSecretFunc
	devcontainer  DevcontainerFunc
	languagesOf   LanguagesFunc
	devPodUp      DevPodUpFunc
	installLSP    LSPInstallFunc
	installClaude ClaudeInstallFunc
}

// NewProvisioner builds a Provisioner. Every func parameter is required;
// there is no sensible zero-value default for any provisioning step.
func NewProvisioner(
	gate *entitlements.Gate,
	workspacePath WorkspacePathFunc,
	clone CloneFunc,
	resolveSecret ResolveSecretFunc,
	devcontainer DevcontainerFunc,
	languagesOf LanguagesFunc,
	devPodUp DevPodUpFunc,
	installLSP LSPInstallFunc,
	installClaude ClaudeInstallFunc,
) *Provisioner {
	return &Provisioner{
		gate: gate, workspacePath: workspacePath, clone: clone, resolveSecret: resolveSecret, devcontainer: devcontainer,
		languagesOf: languagesOf, devPodUp: devPodUp, installLSP: installLSP, installClaude: installClaude,
	}
}

// Provision runs every step in order, stopping at the first failure —
// including a gate denial, which stops before the clone even starts
// (FR-024).
func (p *Provisioner) Provision(ctx context.Context, req Request) *Result {
	result := newResult()
	for _, name := range orderedSteps {
		if !p.runStep(ctx, req, result, name) {
			break
		}
	}
	return result
}

// RetryStep re-runs exactly one previously failed step (FR-028) — it
// never re-clones and never re-runs any *earlier* step. Every step
// function is idempotent on its own inputs (clone re-clones only if
// called; devcontainer detection just re-reads the same repo path;
// devpod up / LSP install / claude install are safe to repeat). Once the
// retried step succeeds, provisioning resumes through the remaining
// still-pending steps automatically — those never got a chance to run
// the first time around, so this is resuming a paused pipeline, not
// repeating work.
func (p *Provisioner) RetryStep(ctx context.Context, req Request, result *Result, name StepName) {
	if !p.runStep(ctx, req, result, name) {
		return
	}
	idx := result.stepIndex(name)
	for _, step := range orderedSteps[idx+1:] {
		if !p.runStep(ctx, req, result, step) {
			return
		}
	}
}

// runStep executes one step, updating result in place, and returns
// whether provisioning should continue to the next step.
func (p *Provisioner) runStep(ctx context.Context, req Request, result *Result, name StepName) bool {
	idx := result.stepIndex(name)
	if idx < 0 {
		return false
	}
	result.Steps[idx].Status = StepRunning

	var err error
	switch name {
	case StepEntitlementsGate:
		err = p.runEntitlementsGate(ctx, req, result)
	case StepClone:
		result.Status = StatusCloning
		var credential *CloneCredential
		credential, err = p.cloneCredentialFor(ctx, req)
		if err == nil {
			err = p.clone(ctx, req.WorkspaceID, req.RepositoryURL, credential)
		}
	case StepDevcontainer:
		result.Status = StatusProvisioning
		result.DevcontainerConfig, err = p.devcontainer(p.workspacePath(req.WorkspaceID))
	case StepDevPodUp:
		err = p.devPodUp(ctx, p.workspacePath(req.WorkspaceID))
	case StepLSPBootstrap:
		languages := p.languagesOf(p.workspacePath(req.WorkspaceID))
		result.LanguageServers = p.installLSP(ctx, req.WorkspaceID, languages)
		err = firstLanguageServerError(result.LanguageServers)
	case StepClaudeBootstrap:
		err = p.installClaude(ctx, req.WorkspaceID)
	default:
		err = fmt.Errorf("devpod: unknown step %q", name)
	}

	if err != nil {
		result.Steps[idx].Status = StepError
		message := err.Error()
		result.Steps[idx].Error = &message
		if name == StepClone && req.CredentialSecretRef == "" {
			// FR-023: a clone failure with no credential at all is treated
			// as "this repo likely needs one" rather than a generic error
			// — git's own exit code doesn't cleanly distinguish "private,
			// needs auth" from "doesn't exist"/"network unreachable", so
			// this is a heuristic, not a certainty.
			result.Status = StatusPendingCredential
		} else {
			result.Status = StatusError
		}
		return false
	}
	result.Steps[idx].Status = StepDone
	result.Steps[idx].Error = nil
	if name == StepClaudeBootstrap {
		result.Status = StatusReady // FR-029: ready only once every step is done
	}
	return true
}

// runEntitlementsGate special-cases self-hosted (see entitlements.Gate)
// and turns a denial into a step error without needing a separate
// "denied" status value — Result.Reason carries the specific reason.
func (p *Provisioner) runEntitlementsGate(ctx context.Context, req Request, result *Result) error {
	resp, err := p.gate.CheckQuota(ctx, entitlements.CheckQuotaRequest{HostKind: req.HostKind, UserID: req.OwnerUserID})
	if err != nil {
		return err
	}
	if !resp.Allowed {
		result.Reason = resp.Reason
		return fmt.Errorf("devpod: entitlements gate denied provisioning: %s", derefReason(resp.Reason))
	}
	return nil
}

func (p *Provisioner) cloneCredentialFor(ctx context.Context, req Request) (*CloneCredential, error) {
	if req.CredentialSecretRef == "" {
		return nil, nil
	}
	secret, err := p.resolveSecret(ctx, req.CredentialSecretRef)
	if err != nil {
		return nil, fmt.Errorf("devpod: resolve credential secret: %w", err)
	}
	return &CloneCredential{Kind: req.CredentialKind, Secret: secret}, nil
}

func firstLanguageServerError(servers []bootstrap.LanguageServer) error {
	for _, s := range servers {
		if s.Status == bootstrap.LanguageServerStatusError {
			return fmt.Errorf("devpod: language server install failed for %s", s.Language)
		}
	}
	return nil
}

func derefReason(reason *string) string {
	if reason == nil {
		return "unknown"
	}
	return *reason
}
