// Package gitmanager implements the Git Manager component of the Bridge
// Daemon — credential lifecycle (FR-015 to FR-021), and, from later tasks,
// the clone-before-container flow (02_arquitectura_solucion.md §3.2).
package gitmanager

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
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
	// List returns every ref beginning with prefix — used by
	// CredentialManager.List to reconstruct which credentials already
	// exist for an ownerUserID (FR-021's "indexadas por usuario" cuts
	// both ways: it's also how a Host that already has credentials on it
	// gets discovered again after the app that registered them loses its
	// own local record, e.g. a reinstall). An empty result for a prefix
	// that has no secrets under it is not an error.
	List(ctx context.Context, prefix string) ([]string, error)
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

// metaRef is where a Credential's own metadata (everything but the secret
// itself: alias, domain, kind, status, ...) lives — a sibling of its
// secret under the same SecretStore, one level of indirection SecretStore
// itself doesn't know about. Needed because SecretStore.Get(ref) only
// ever returns the raw secret value: without this, List has no way to
// reconstruct which alias/domain/kind/status a given secretRef belongs
// to, and a Host that already has credentials on it (e.g. registered from
// a device whose local SwiftData record is gone — a reinstall, a second
// device) would stay invisible to List forever even though the secret
// itself is right there.
func (m *CredentialManager) metaRef(secretRef string) string {
	return secretRef + ".meta.json"
}

// putMeta persists cred's own metadata alongside its secret. Best-effort,
// same as the rest of this package's git-credentials writes tolerate a
// storage hiccup without failing the whole registration: the credential
// still works for this response either way, it would just stay invisible
// to a future List call if this particular write fails while the secret
// write right before it (which does fail the caller) succeeded.
func (m *CredentialManager) putMeta(ctx context.Context, cred Credential) {
	data, err := json.Marshal(cred)
	if err != nil {
		return
	}
	_ = m.secrets.Put(ctx, m.metaRef(cred.SecretRef), string(data))
}

// List reconstructs every Credential already registered for ownerUserID
// by reading back the metadata List writes — see metaRef's doc comment
// for why this can't just enumerate secrets directly. A metadata blob
// that fails to read or parse (e.g. written by a future format this
// version doesn't understand) is skipped rather than failing the whole
// list — one bad entry shouldn't hide every other real credential.
func (m *CredentialManager) List(ctx context.Context, ownerUserID string) ([]Credential, error) {
	prefix := fmt.Sprintf("codeeditor/git-credentials/%s/", ownerUserID)
	refs, err := m.secrets.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("gitmanager: list credentials: %w", err)
	}
	creds := make([]Credential, 0, len(refs))
	for _, ref := range refs {
		if !strings.HasSuffix(ref, ".meta.json") {
			continue
		}
		data, err := m.secrets.Get(ctx, ref)
		if err != nil {
			continue
		}
		var cred Credential
		if err := json.Unmarshal([]byte(data), &cred); err != nil {
			continue
		}
		creds = append(creds, cred)
	}
	return creds, nil
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

// normalizeSSHPrivateKeyPEM guarantees privateKeyPEM ends in exactly one
// trailing newline. Go's own ssh.ParsePrivateKey (used for validation just
// below) tolerates a PEM blob missing the newline after
// "-----END ... KEY-----" just fine, but the real `ssh`/`git` binaries that
// actually clone/push/pull (internal/gitmanager's Clone/Operations, run as
// subprocesses) reject it with a cryptic "error in libcrypto" — a real
// gotcha when the key arrives from a client text field that preserves
// every newline *within* the pasted text but drops the trailing one.
func normalizeSSHPrivateKeyPEM(privateKeyPEM string) string {
	return strings.TrimRight(privateKeyPEM, "\r\n") + "\n"
}

// ImportSSHKey validates that privateKeyPEM parses as an SSH private key,
// then stores it.
func (m *CredentialManager) ImportSSHKey(
	ctx context.Context,
	ownerUserID, alias, domain, privateKeyPEM string,
) (Credential, error) {
	privateKeyPEM = normalizeSSHPrivateKeyPEM(privateKeyPEM)
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

	cred := Credential{
		ID:          id,
		OwnerUserID: ownerUserID,
		Alias:       alias,
		Domain:      domain,
		Kind:        kind,
		Status:      CredentialStatusVerified,
		SecretRef:   ref,
	}
	m.putMeta(ctx, cred)
	return cred, nil
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

	cred := Credential{
		ID:          id,
		OwnerUserID: ownerUserID,
		Alias:       alias,
		Domain:      domain,
		Kind:        CredentialKindSSHKey,
		Status:      CredentialStatusUnverified,
		SecretRef:   ref,
	}
	m.putMeta(ctx, cred)
	return cred, nil
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
	m.putMeta(ctx, *cred)
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
	if cred.Kind == CredentialKindSSHKey {
		newSecret = normalizeSSHPrivateKeyPEM(newSecret)
	}
	if err := m.secrets.Put(ctx, cred.SecretRef, newSecret); err != nil {
		return fmt.Errorf("gitmanager: store secret: %w", err)
	}
	if err := m.validator.Validate(ctx, cred.Domain, cred.Kind, newSecret); err != nil {
		return fmt.Errorf("gitmanager: reauthentication failed validation: %w", err)
	}
	cred.Status = CredentialStatusVerified
	m.putMeta(ctx, *cred)
	return nil
}
