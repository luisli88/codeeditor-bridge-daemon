package gitmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	// listSecretsPages lets a test simulate pagination — List keeps
	// calling ListSecrets as long as the *previous* page's NextToken was
	// non-nil, so each successive call here pops the next page.
	listSecretsPages []*secretsmanager.ListSecretsOutput
	listSecretsErr   error
	listSecretsCalls int
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

func (f *fakeSecretsManagerAPI) ListSecrets(
	ctx context.Context, params *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options),
) (*secretsmanager.ListSecretsOutput, error) {
	if f.listSecretsErr != nil {
		return nil, f.listSecretsErr
	}
	if f.listSecretsCalls >= len(f.listSecretsPages) {
		return &secretsmanager.ListSecretsOutput{}, nil
	}
	page := f.listSecretsPages[f.listSecretsCalls]
	f.listSecretsCalls++
	return page, nil
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

func TestSecretsManagerStore_List_FiltersByNameFilter_UsesPrefix(t *testing.T) {
	prefix := "codeeditor/git-credentials/owner-1/"
	api := &fakeSecretsManagerAPI{
		listSecretsPages: []*secretsmanager.ListSecretsOutput{{
			SecretList: []types.SecretListEntry{
				{Name: aws.String(prefix + "cred-1")},
				{Name: aws.String(prefix + "cred-2")},
			},
		}},
	}
	store := &SecretsManagerStore{Client: api}

	refs, err := store.List(context.Background(), prefix)

	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d: %v", len(refs), refs)
	}
	if refs[0] != prefix+"cred-1" || refs[1] != prefix+"cred-2" {
		t.Errorf("unexpected refs: %v", refs)
	}
}

// The Filters param sent to ListSecrets is a hint, not a guarantee — the
// real API's "prefix match" isn't specified precisely enough to trust
// blindly, so List re-checks every result itself. A result that doesn't
// actually share the prefix (a filter-matching quirk, or a fake/mock in a
// test) must not leak through.
func TestSecretsManagerStore_List_RechecksPrefixLocally_DropsMismatches(t *testing.T) {
	api := &fakeSecretsManagerAPI{
		listSecretsPages: []*secretsmanager.ListSecretsOutput{{
			SecretList: []types.SecretListEntry{
				{Name: aws.String("codeeditor/git-credentials/owner-1/cred-1")},
				{Name: aws.String("codeeditor/git-credentials/owner-2/cred-1")},
			},
		}},
	}
	store := &SecretsManagerStore{Client: api}

	refs, err := store.List(context.Background(), "codeeditor/git-credentials/owner-1/")

	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != "codeeditor/git-credentials/owner-1/cred-1" {
		t.Errorf("expected only the owner-1 ref, got %v", refs)
	}
}

func TestSecretsManagerStore_List_PaginatesUntilNextTokenIsNil(t *testing.T) {
	prefix := "codeeditor/git-credentials/owner-1/"
	api := &fakeSecretsManagerAPI{
		listSecretsPages: []*secretsmanager.ListSecretsOutput{
			{
				SecretList: []types.SecretListEntry{{Name: aws.String(prefix + "cred-1")}},
				NextToken:  aws.String("page-2"),
			},
			{
				SecretList: []types.SecretListEntry{{Name: aws.String(prefix + "cred-2")}},
			},
		},
	}
	store := &SecretsManagerStore{Client: api}

	refs, err := store.List(context.Background(), prefix)

	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs across both pages, got %d: %v", len(refs), refs)
	}
	if api.listSecretsCalls != 2 {
		t.Errorf("expected 2 ListSecrets calls (one per page), got %d", api.listSecretsCalls)
	}
}

func TestSecretsManagerStore_List_ClientError_Propagates(t *testing.T) {
	api := &fakeSecretsManagerAPI{listSecretsErr: errors.New("boom")}
	store := &SecretsManagerStore{Client: api}

	_, err := store.List(context.Background(), "codeeditor/git-credentials/owner-1/")

	if err == nil {
		t.Fatal("expected an error")
	}
}
