package gitmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// SecretsManagerAPI is the subset of the Secrets Manager client SecretStore
// needs — kept narrow so tests can supply a fake.
type SecretsManagerAPI interface {
	CreateSecret(
		ctx context.Context, params *secretsmanager.CreateSecretInput, optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(
		ctx context.Context, params *secretsmanager.PutSecretValueInput, optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.PutSecretValueOutput, error)
	GetSecretValue(
		ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.GetSecretValueOutput, error)
	ListSecrets(
		ctx context.Context, params *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.ListSecretsOutput, error)
}

// SecretsManagerStore is the production SecretStore
// (02_arquitectura_solucion.md §3.12: credenciales centralizadas en
// Secrets Manager, indexadas por usuario, nunca en el contenedor).
type SecretsManagerStore struct {
	Client SecretsManagerAPI
}

func (s *SecretsManagerStore) Put(ctx context.Context, ref, value string) error {
	_, err := s.Client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(ref),
		SecretString: aws.String(value),
	})
	if err == nil {
		return nil
	}

	var alreadyExists *types.ResourceExistsException
	if errors.As(err, &alreadyExists) {
		// FR-020: reautenticación — el secreto ya existe, se reemplaza el
		// valor en vez de fallar.
		_, putErr := s.Client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
			SecretId:     aws.String(ref),
			SecretString: aws.String(value),
		})
		if putErr != nil {
			return fmt.Errorf("secretsmanager PutSecretValue %s: %w", ref, putErr)
		}
		return nil
	}
	return fmt.Errorf("secretsmanager CreateSecret %s: %w", ref, err)
}

func (s *SecretsManagerStore) Get(ctx context.Context, ref string) (string, error) {
	out, err := s.Client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(ref)})
	if err != nil {
		return "", fmt.Errorf("secretsmanager GetSecretValue %s: %w", ref, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secretsmanager: secret %s has no string value", ref)
	}
	return *out.SecretString, nil
}

// List returns every secret name under prefix. Secrets Manager's "name"
// filter is a real (case-sensitive) prefix match server-side, but results
// are re-checked with strings.HasPrefix anyway — "prefix match" in the
// filter's own docs isn't specified precisely enough to trust blindly
// against an adversarial or merely coincidental name, and the recheck
// costs nothing.
func (s *SecretsManagerStore) List(ctx context.Context, prefix string) ([]string, error) {
	var refs []string
	var nextToken *string
	for {
		out, err := s.Client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{
			Filters:   []types.Filter{{Key: types.FilterNameStringTypeName, Values: []string{prefix}}},
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("secretsmanager ListSecrets %s: %w", prefix, err)
		}
		for _, secret := range out.SecretList {
			if secret.Name != nil && strings.HasPrefix(*secret.Name, prefix) {
				refs = append(refs, *secret.Name)
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return refs, nil
}
