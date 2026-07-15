// Package gitmanager implements the Git Manager component of the Bridge
// Daemon — credential lifecycle (FR-015 to FR-021), and, from later tasks,
// the clone-before-container flow (02_arquitectura_solucion.md §3.2).
package gitmanager

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// CredentialKind mirrors data-model.md → Credencial de git.kind. There is
// no "github-oauth-derived" constructor here — that credential is created
// by the github-oauth-exchange Lambda (contracts/auth-flows.md), not by
// the Bridge Daemon.
type CredentialKind string

const (
	CredentialKindSSHKey CredentialKind = "ssh-key"
	CredentialKindPAT    CredentialKind = "personal-access-token"
	// CredentialKindGitHubOAuthDerived is read-only from the Desarrollador's
	// perspective — it is only ever created by RegisterGitHubDerived, off
	// the access obtained at GitHub sign-in (FR-014), and revoked by
	// reauthenticating GitHub rather than by direct deletion.
	CredentialKindGitHubOAuthDerived CredentialKind = "github-oauth-derived"
)

// CredentialStatus mirrors data-model.md → Credencial de git.status.
type CredentialStatus string

const (
	CredentialStatusVerified   CredentialStatus = "verified"
	CredentialStatusUnverified CredentialStatus = "unverified"
	CredentialStatusRevoked    CredentialStatus = "revoked"
)

// Credential is the Bridge Daemon's in-memory/transport view of a git
// credential — the real secret lives only in SecretStore, referenced by
// SecretRef (FR-021).
type Credential struct {
	ID          string           `json:"id"`
	OwnerUserID string           `json:"ownerUserId"`
	Alias       string           `json:"alias"`
	Domain      string           `json:"domain"`
	Kind        CredentialKind   `json:"kind"`
	Status      CredentialStatus `json:"status"`
	LastUsedAt  *time.Time       `json:"lastUsedAt,omitempty"`
	SecretRef   string           `json:"secretRef"`
}

// SecretStore is the persistence seam for the actual secret material —
// AWS Secrets Manager in production (02_arquitectura_solucion.md §3.12),
// indexed by user, never by container.
type SecretStore interface {
	Put(ctx context.Context, ref string, value string) error
	Get(ctx context.Context, ref string) (string, error)
}

// Validator checks that a credential actually authenticates against its
// domain before it can be marked verified (FR-016).
type Validator interface {
	Validate(ctx context.Context, domain string, kind CredentialKind, secret string) error
}

// CredentialManager implements FR-015/FR-016/FR-020.
type CredentialManager struct {
	secrets   SecretStore
	validator Validator
}

// NewCredentialManager builds a CredentialManager.
func NewCredentialManager(secrets SecretStore, validator Validator) *CredentialManager {
	return &CredentialManager{secrets: secrets, validator: validator}
}

func (m *CredentialManager) secretRef(ownerUserID, credentialID string) string {
	return fmt.Sprintf("codeeditor/git-credentials/%s/%s", ownerUserID, credentialID)
}

// GenerateSSHKey creates a new ed25519 key pair, stores the private key,
// and returns the Credential plus the public key the Desarrollador needs
// to add to their git provider. Unlike RegisterPAT/ImportSSHKey, this one
// can never validate *before* storing — the provider can't possibly
// accept a key it hasn't been given yet — so it always persists and
// starts `unverified`, deliberately *not* attempting validation yet
// either (that would just fail every time, since the Desarrollador hasn't
// had a chance to add the public half anywhere). `Verify` is the explicit
// second step, called once they have.
func (m *CredentialManager) GenerateSSHKey(
	ctx context.Context,
	ownerUserID, alias, domain string,
) (Credential, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Credential{}, "", fmt.Errorf("gitmanager: generate ed25519 key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Credential{}, "", fmt.Errorf("gitmanager: encode public key: %w", err)
	}
	privatePEM, err := marshalPrivateKey(priv)
	if err != nil {
		return Credential{}, "", err
	}

	cred, err := m.storeGenerated(ctx, ownerUserID, alias, domain, privatePEM)
	if err != nil {
		return Credential{}, "", err
	}
	return cred, string(ssh.MarshalAuthorizedKey(sshPub)), nil
}

// ImportSSHKey validates that privateKeyPEM parses as an SSH private key,
// then stores it.
func (m *CredentialManager) ImportSSHKey(
	ctx context.Context,
	ownerUserID, alias, domain, privateKeyPEM string,
) (Credential, error) {
	if _, err := ssh.ParsePrivateKey([]byte(privateKeyPEM)); err != nil {
		return Credential{}, fmt.Errorf("gitmanager: invalid SSH private key: %w", err)
	}
	return m.store(ctx, ownerUserID, alias, domain, CredentialKindSSHKey, privateKeyPEM)
}

// RegisterPAT stores a personal access token for domain (FR-015).
func (m *CredentialManager) RegisterPAT(
	ctx context.Context,
	ownerUserID, alias, domain, token string,
) (Credential, error) {
	return m.store(ctx, ownerUserID, alias, domain, CredentialKindPAT, token)
}

// RegisterGitHubDerived stores the GitHub access token obtained during
// GitHub sign-in as a git credential, reused automatically for private
// GitHub repositories without the Desarrollador adding one manually
// (FR-014). Alias and domain are fixed: there is exactly one GitHub-derived
// credential per user.
func (m *CredentialManager) RegisterGitHubDerived(
	ctx context.Context,
	ownerUserID, accessToken string,
) (Credential, error) {
	return m.store(ctx, ownerUserID, "GitHub", "github.com", CredentialKindGitHubOAuthDerived, accessToken)
}

func (m *CredentialManager) store(
	ctx context.Context,
	ownerUserID, alias, domain string,
	kind CredentialKind,
	secret string,
) (Credential, error) {
	// FR-016: validate against the provider *before* persisting anything —
	// a credential that doesn't actually work is never stored (no secret
	// written, no Credential returned) instead of sitting around
	// "unverified" and colliding with a retry that reuses the same alias.
	if err := m.validator.Validate(ctx, domain, kind, secret); err != nil {
		return Credential{}, fmt.Errorf("gitmanager: credential validation failed: %w", err)
	}

	id := uuid.NewString()
	ref := m.secretRef(ownerUserID, id)
	if err := m.secrets.Put(ctx, ref, secret); err != nil {
		return Credential{}, fmt.Errorf("gitmanager: store secret: %w", err)
	}

	return Credential{
		ID:          id,
		OwnerUserID: ownerUserID,
		Alias:       alias,
		Domain:      domain,
		Kind:        kind,
		Status:      CredentialStatusVerified,
		SecretRef:   ref,
	}, nil
}

// storeGenerated is GenerateSSHKey's own persistence path — see its doc
// comment for why it can't gate on (or even attempt) validation the way
// store does.
func (m *CredentialManager) storeGenerated(
	ctx context.Context,
	ownerUserID, alias, domain, secret string,
) (Credential, error) {
	id := uuid.NewString()
	ref := m.secretRef(ownerUserID, id)
	if err := m.secrets.Put(ctx, ref, secret); err != nil {
		return Credential{}, fmt.Errorf("gitmanager: store secret: %w", err)
	}

	return Credential{
		ID:          id,
		OwnerUserID: ownerUserID,
		Alias:       alias,
		Domain:      domain,
		Kind:        CredentialKindSSHKey,
		Status:      CredentialStatusUnverified,
		SecretRef:   ref,
	}, nil
}

// Verify re-checks an already-stored credential's existing secret against
// the provider — the explicit second step after GenerateSSHKey, once the
// Desarrollador has actually added the public key to their provider.
// Unlike Reauthenticate, no new secret comes from the caller: the same
// one that's already stored is re-read and re-validated.
func (m *CredentialManager) Verify(ctx context.Context, cred *Credential) error {
	secret, err := m.secrets.Get(ctx, cred.SecretRef)
	if err != nil {
		return fmt.Errorf("gitmanager: read secret: %w", err)
	}
	if err := m.validator.Validate(ctx, cred.Domain, cred.Kind, secret); err != nil {
		return fmt.Errorf("gitmanager: verification failed: %w", err)
	}
	cred.Status = CredentialStatusVerified
	return nil
}

// Revoke marks a credential invalid because the provider revoked it
// (FR-020) — called when a git operation using it fails with an auth
// error.
func Revoke(cred *Credential) {
	cred.Status = CredentialStatusRevoked
}

// Reauthenticate replaces the secret behind an existing credential without
// changing its alias or ID (FR-020).
func (m *CredentialManager) Reauthenticate(ctx context.Context, cred *Credential, newSecret string) error {
	if err := m.secrets.Put(ctx, cred.SecretRef, newSecret); err != nil {
		return fmt.Errorf("gitmanager: store secret: %w", err)
	}
	if err := m.validator.Validate(ctx, cred.Domain, cred.Kind, newSecret); err != nil {
		return fmt.Errorf("gitmanager: reauthentication failed validation: %w", err)
	}
	cred.Status = CredentialStatusVerified
	return nil
}
