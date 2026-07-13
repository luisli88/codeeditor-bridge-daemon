package tests

import (
	"context"
	"errors"
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

func TestRegisterPAT_InvalidCredential_StaysUnverified(t *testing.T) {
	store := newFakeSecretStore()
	manager := gitmanager.NewCredentialManager(store, &fakeValidator{shouldFail: true})

	cred, err := manager.RegisterPAT(context.Background(), "user-1", "Bad Token", "github.com", "bad-token")

	require.NoError(t, err)
	assert.Equal(t, gitmanager.CredentialStatusUnverified, cred.Status)
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
