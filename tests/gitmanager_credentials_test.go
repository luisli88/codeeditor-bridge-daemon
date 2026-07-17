package tests

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/gitmanager"
)

type fakeSecretStore struct {
	values map[string]string
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{values: make(map[string]string)}
}

func (f *fakeSecretStore) Put(ctx context.Context, ref, value string) error {
	f.values[ref] = value
	return nil
}

func (f *fakeSecretStore) Get(ctx context.Context, ref string) (string, error) {
	v, ok := f.values[ref]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

type fakeValidator struct {
	shouldFail bool
}

func (f *fakeValidator) Validate(ctx context.Context, domain string, kind gitmanager.CredentialKind, secret string) error {
	if f.shouldFail {
		return errors.New("provider rejected the credential")
	}
	return nil
}

func TestRegisterPAT_ValidCredential_MarksVerified(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: false})

	cred, err := manager.RegisterPAT(context.Background(), "user-1", "GitHub Token", "github.com", "ghp_token")

	require.NoError(t, err)
	assert.Equal(t, gitmanager.CredentialStatusVerified, cred.Status)
	assert.Equal(t, gitmanager.CredentialKindPAT, cred.Kind)
	storedValue, err := store.Get(context.Background(), cred.SecretRef)
	require.NoError(t, err)
	assert.Equal(t, "ghp_token", storedValue)
}

// FR-016: a credential that fails provider validation is never persisted
// at all — no secret stored, nothing returned — instead of being saved
// "unverified" and colliding with a retry that reuses the same alias.
func TestRegisterPAT_InvalidCredential_NotPersisted(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: true})

	cred, err := manager.RegisterPAT(context.Background(), "user-1", "Bad Token", "github.com", "bad-token")

	require.Error(t, err)
	assert.Empty(t, cred.ID)
	assert.Empty(t, store.values)
}

func TestGenerateSSHKey_ReturnsPublicKeyAndStoresPrivateKey(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: false})

	cred, publicKey, err := manager.GenerateSSHKey(context.Background(), "user-1", "My Key", "github.com")

	require.NoError(t, err)
	assert.Equal(t, gitmanager.CredentialKindSSHKey, cred.Kind)
	assert.Contains(t, publicKey, "ssh-ed25519")
	_, err = store.Get(context.Background(), cred.SecretRef)
	require.NoError(t, err)
}

func TestImportSSHKey_InvalidPEM_ReturnsError(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: false})

	_, err := manager.ImportSSHKey(context.Background(), "user-1", "Bad Import", "github.com", "not a real key")

	require.Error(t, err)
}

// testEd25519PrivateKeyPEM is a real, throwaway OpenSSH private key
// (generated for this test suite only, never used against a real
// provider) — needed because normalizeSSHPrivateKeyPEM must round-trip
// through Go's own ssh.ParsePrivateKey validation, which a synthetic
// string can't satisfy.
const testEd25519PrivateKeyPEM = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
	"QyNTUxOQAAACCKVzMG5CmaNPeCa24QYuDxje7uz6YluuMZB51fiY/rjgAAAJCHFE3IhxRN\n" +
	"yAAAAAtzc2gtZWQyNTUxOQAAACCKVzMG5CmaNPeCa24QYuDxje7uz6YluuMZB51fiY/rjg\n" +
	"AAAEAUeY/BD1egs//JKJkDS8a1KEKfv8EhxahjOVOkrm6uBopXMwbkKZo094JrbhBi4PGN\n" +
	"7u7PpiW64xkHnV+Jj+uOAAAADHRlc3QtZml4dHVyZQE=\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

// A client text field can preserve every newline *within* a pasted PEM
// but drop the trailing one after "-----END ... KEY-----" (confirmed
// live: SwiftUI's TextField(axis: .vertical) does exactly this) — Go's
// own ssh.ParsePrivateKey tolerates that missing newline just fine, but
// the real `ssh`/`git` binaries used to actually clone don't, and fail
// with a cryptic "error in libcrypto". ImportSSHKey must normalize this
// before storing, so every later consumer of the secret gets a
// byte-correct key regardless of what the client sent.
func TestImportSSHKey_MissingTrailingNewline_NormalizedBeforeStoring(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: false})
	keyMissingTrailingNewline := strings.TrimRight(testEd25519PrivateKeyPEM, "\n")
	require.NotEqual(t, testEd25519PrivateKeyPEM, keyMissingTrailingNewline, "fixture must actually be missing the trailing newline")

	cred, err := manager.ImportSSHKey(context.Background(), "user-1", "My Key", "github.com", keyMissingTrailingNewline)

	require.NoError(t, err)
	stored, err := store.Get(context.Background(), cred.SecretRef)
	require.NoError(t, err)
	assert.Equal(t, testEd25519PrivateKeyPEM, stored)
}

// TestRevoke_ThenReauthenticate_RestoresVerifiedStatus — FR-020: marcar
// inválida cuando el proveedor la revoca, y permitir reautenticarla sin
// perder su alias.
func TestRevoke_ThenReauthenticate_RestoresVerifiedStatus(t *testing.T) {
	store := newFakeSecretStore()
	validator := &fakeValidator{shouldFail: false}
	manager := gitmanager.NewCredentialManager(store, validator)

	cred, err := manager.RegisterPAT(context.Background(), "user-1", "My Token", "github.com", "old-token")
	require.NoError(t, err)
	require.Equal(t, gitmanager.CredentialStatusVerified, cred.Status)
	originalAlias := cred.Alias

	gitmanager.Revoke(&cred)
	assert.Equal(t, gitmanager.CredentialStatusRevoked, cred.Status)

	err = manager.Reauthenticate(context.Background(), &cred, "new-token")

	require.NoError(t, err)
	assert.Equal(t, gitmanager.CredentialStatusVerified, cred.Status)
	assert.Equal(t, originalAlias, cred.Alias)
	storedValue, err := store.Get(context.Background(), cred.SecretRef)
	require.NoError(t, err)
	assert.Equal(t, "new-token", storedValue)
}

func TestReauthenticate_StillInvalid_ReturnsError(t *testing.T) {
	store := newFakeSecretStore()
	validator := &fakeValidator{shouldFail: false}
	manager := gitmanager.NewCredentialManager(store, validator)
	cred, err := manager.RegisterPAT(context.Background(), "user-1", "My Token", "github.com", "old-token")
	require.NoError(t, err)

	gitmanager.Revoke(&cred)
	validator.shouldFail = true

	err = manager.Reauthenticate(context.Background(), &cred, "still-bad-token")

	require.Error(t, err)
}

func TestGenerateSSHKey_SecretStoreFails_ReturnsError(t *testing.T) {
	manager := gitmanager.NewCredentialManager(failingSecretStore{}, &fakeValidator{shouldFail: false})

	_, _, err := manager.GenerateSSHKey(context.Background(), "user-1", "My Key", "github.com")

	require.Error(t, err)
}

func TestRegisterPAT_SecretStoreFails_ReturnsError(t *testing.T) {
	manager := gitmanager.NewCredentialManager(failingSecretStore{}, &fakeValidator{shouldFail: false})

	_, err := manager.RegisterPAT(context.Background(), "user-1", "Token", "github.com", "tok")

	require.Error(t, err)
}

func TestReauthenticate_SecretStoreFails_ReturnsError(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: false})
	cred, err := manager.RegisterPAT(context.Background(), "user-1", "My Token", "github.com", "old-token")
	require.NoError(t, err)

	failingManager := gitmanager.NewCredentialManager(failingSecretStore{}, &fakeValidator{shouldFail: false})
	err = failingManager.Reauthenticate(context.Background(), &cred, "new-token")

	require.Error(t, err)
}
