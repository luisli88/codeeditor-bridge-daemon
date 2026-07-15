package gitmanager

import (
	"context"
	"errors"
	"testing"
)

// fakeValidator lets tests control Validate's outcome directly, instead of
// hitting a real provider — CredentialManager only depends on the
// Validator interface, never on ProviderValidator concretely.
type fakeValidator struct {
	err error
}

func (v fakeValidator) Validate(_ context.Context, _ string, _ CredentialKind, _ string) error {
	return v.err
}

func TestCredentialManager_RegisterPAT_ValidationFails_NothingPersisted(t *testing.T) {
	secrets := NewLocalFileStore(t.TempDir())
	manager := NewCredentialManager(secrets, fakeValidator{err: errors.New("token rejected")})

	cred, err := manager.RegisterPAT(context.Background(), "owner-1", "alias-1", "github.com", "bad-token")

	if err == nil {
		t.Fatal("expected an error when validation fails")
	}
	if cred.ID != "" {
		t.Fatalf("expected a zero-value Credential on validation failure, got %+v", cred)
	}
	// The whole point: no secret behind an alias that "failed" — a retry
	// with the same alias must not collide with a leftover from this one.
	if _, getErr := secrets.Get(context.Background(), manager.secretRef("owner-1", cred.ID)); getErr == nil {
		t.Fatal("expected no secret to have been stored for a credential that failed validation")
	}
}

func TestCredentialManager_RegisterPAT_ValidationSucceeds_StoresAndVerifies(t *testing.T) {
	secrets := NewLocalFileStore(t.TempDir())
	manager := NewCredentialManager(secrets, fakeValidator{err: nil})

	cred, err := manager.RegisterPAT(context.Background(), "owner-1", "alias-1", "github.com", "good-token")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cred.Status != CredentialStatusVerified {
		t.Fatalf("expected status %q, got %q", CredentialStatusVerified, cred.Status)
	}
	stored, getErr := secrets.Get(context.Background(), cred.SecretRef)
	if getErr != nil {
		t.Fatalf("expected the secret to have been stored: %v", getErr)
	}
	if stored != "good-token" {
		t.Fatalf("expected stored secret %q, got %q", "good-token", stored)
	}
}

// GenerateSSHKey never validates at all (not just "before storing") —
// there's nothing to check against yet the moment a key is born, since
// the Desarrollador hasn't had a chance to add its public half anywhere.
// It always persists starting `unverified`, public key included either
// way, and stays that way until an explicit Verify call.
func TestCredentialManager_GenerateSSHKey_AlwaysStoresUnverified(t *testing.T) {
	secrets := NewLocalFileStore(t.TempDir())
	// A validator that would say "yes" if asked — proving GenerateSSHKey
	// really does skip calling it, not just that it happens to fail here.
	manager := NewCredentialManager(secrets, fakeValidator{err: nil})

	cred, publicKey, err := manager.GenerateSSHKey(context.Background(), "owner-1", "alias-1", "github.com")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cred.Status != CredentialStatusUnverified {
		t.Fatalf("expected status %q, got %q", CredentialStatusUnverified, cred.Status)
	}
	if publicKey == "" {
		t.Fatal("expected a public key")
	}
	if _, getErr := secrets.Get(context.Background(), cred.SecretRef); getErr != nil {
		t.Fatalf("expected the private key to have been stored: %v", getErr)
	}
}

func TestCredentialManager_Verify_Succeeds_MarksVerified(t *testing.T) {
	secrets := NewLocalFileStore(t.TempDir())
	manager := NewCredentialManager(secrets, fakeValidator{err: nil})
	cred, _, err := manager.GenerateSSHKey(context.Background(), "owner-1", "alias-1", "github.com")
	if err != nil {
		t.Fatalf("setup: generate key: %v", err)
	}

	err = manager.Verify(context.Background(), &cred)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cred.Status != CredentialStatusVerified {
		t.Fatalf("expected status %q, got %q", CredentialStatusVerified, cred.Status)
	}
}

func TestCredentialManager_Verify_StillRejected_ReturnsErrorAndStaysUnverified(t *testing.T) {
	secrets := NewLocalFileStore(t.TempDir())
	validator := &mutableFakeValidator{err: nil}
	manager := NewCredentialManager(secrets, validator)
	cred, _, err := manager.GenerateSSHKey(context.Background(), "owner-1", "alias-1", "github.com")
	if err != nil {
		t.Fatalf("setup: generate key: %v", err)
	}
	validator.err = errors.New("still not added to the provider")

	err = manager.Verify(context.Background(), &cred)

	if err == nil {
		t.Fatal("expected an error")
	}
	if cred.Status != CredentialStatusUnverified {
		t.Fatalf("expected status to stay %q, got %q", CredentialStatusUnverified, cred.Status)
	}
}

// mutableFakeValidator lets a test change the outcome between calls (e.g.
// "generate, then verify after the Desarrollador still hasn't added the
// key anywhere") — fakeValidator's err is fixed for its whole lifetime.
type mutableFakeValidator struct {
	err error
}

func (v *mutableFakeValidator) Validate(_ context.Context, _ string, _ CredentialKind, _ string) error {
	return v.err
}
