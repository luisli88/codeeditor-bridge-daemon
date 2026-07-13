package gitmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

type fakeSecretsManagerAPI struct {
	createSecretErr     error
	putSecretValueErr   error
	getSecretValueOut   *secretsmanager.GetSecretValueOutput
	getSecretValueErr   error
	createSecretCalls   int
	putSecretValueCalls int
}

func (f *fakeSecretsManagerAPI) CreateSecret(
	ctx context.Context, params *secretsmanager.CreateSecretInput, optFns ...func(*secretsmanager.Options),
) (*secretsmanager.CreateSecretOutput, error) {
	f.createSecretCalls++
	if f.createSecretErr != nil {
		return nil, f.createSecretErr
	}
	return &secretsmanager.CreateSecretOutput{}, nil
}

func (f *fakeSecretsManagerAPI) PutSecretValue(
	ctx context.Context, params *secretsmanager.PutSecretValueInput, optFns ...func(*secretsmanager.Options),
) (*secretsmanager.PutSecretValueOutput, error) {
	f.putSecretValueCalls++
	if f.putSecretValueErr != nil {
		return nil, f.putSecretValueErr
	}
	return &secretsmanager.PutSecretValueOutput{}, nil
}

func (f *fakeSecretsManagerAPI) GetSecretValue(
	ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options),
) (*secretsmanager.GetSecretValueOutput, error) {
	return f.getSecretValueOut, f.getSecretValueErr
}

func TestSecretsManagerStore_Put_NewSecret_CallsCreateSecret(t *testing.T) {
	api := &fakeSecretsManagerAPI{}
	store := &SecretsManagerStore{Client: api}

	err := store.Put(context.Background(), "ref/1", "value")

	if err != nil {
		t.Fatal(err)
	}
	if api.createSecretCalls != 1 || api.putSecretValueCalls != 0 {
		t.Errorf("expected 1 CreateSecret call, got create=%d put=%d", api.createSecretCalls, api.putSecretValueCalls)
	}
}

// FR-020: reauthentication — the secret already exists, so Put falls
// back to replacing its value instead of failing.
func TestSecretsManagerStore_Put_ExistingSecret_FallsBackToPutSecretValue(t *testing.T) {
	api := &fakeSecretsManagerAPI{createSecretErr: &types.ResourceExistsException{}}
	store := &SecretsManagerStore{Client: api}

	err := store.Put(context.Background(), "ref/1", "new-value")

	if err != nil {
		t.Fatal(err)
	}
	if api.putSecretValueCalls != 1 {
		t.Errorf("expected 1 PutSecretValue call, got %d", api.putSecretValueCalls)
	}
}

func TestSecretsManagerStore_Put_OtherCreateError_Propagates(t *testing.T) {
	api := &fakeSecretsManagerAPI{createSecretErr: errors.New("boom")}
	store := &SecretsManagerStore{Client: api}

	err := store.Put(context.Background(), "ref/1", "value")

	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestSecretsManagerStore_Put_PutSecretValueError_Propagates(t *testing.T) {
	api := &fakeSecretsManagerAPI{
		createSecretErr:   &types.ResourceExistsException{},
		putSecretValueErr: errors.New("boom"),
	}
	store := &SecretsManagerStore{Client: api}

	err := store.Put(context.Background(), "ref/1", "value")

	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestSecretsManagerStore_Get_ReturnsSecretString(t *testing.T) {
	secretValue := "the-secret"
	api := &fakeSecretsManagerAPI{getSecretValueOut: &secretsmanager.GetSecretValueOutput{SecretString: &secretValue}}
	store := &SecretsManagerStore{Client: api}

	value, err := store.Get(context.Background(), "ref/1")

	if err != nil {
		t.Fatal(err)
	}
	if value != "the-secret" {
		t.Errorf("expected 'the-secret', got %q", value)
	}
}

func TestSecretsManagerStore_Get_ClientError_Propagates(t *testing.T) {
	api := &fakeSecretsManagerAPI{getSecretValueErr: errors.New("boom")}
	store := &SecretsManagerStore{Client: api}

	_, err := store.Get(context.Background(), "ref/1")

	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestSecretsManagerStore_Get_NoStringValue_ReturnsError(t *testing.T) {
	api := &fakeSecretsManagerAPI{getSecretValueOut: &secretsmanager.GetSecretValueOutput{SecretString: nil}}
	store := &SecretsManagerStore{Client: api}

	_, err := store.Get(context.Background(), "ref/1")

	if err == nil {
		t.Fatal("expected an error")
	}
}
