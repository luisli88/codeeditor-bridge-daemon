package entitlements

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DynamoDBAPI is the subset of the DynamoDB client Store needs — kept
// narrow so tests can supply a fake without pulling in the real SDK.
type DynamoDBAPI interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// DynamoDBStore is the production Store, backed by the Entitlement and
// Project tables Amplify Data provisions from
// contracts/amplify-data-schema.md.
type DynamoDBStore struct {
	Client               DynamoDBAPI
	EntitlementTableName string
	ProjectTableName     string
	// ProjectsByOwnerIndex is the GSI Amplify Data generates for the
	// Project.ownerUserId field.
	ProjectsByOwnerIndex string
}

type entitlementItem struct {
	OwnerUserID                string `dynamodbav:"ownerUserId"`
	ConcurrentEnvironmentLimit int    `dynamodbav:"concurrentEnvironmentLimit"`
	HasActiveSubscription      bool   `dynamodbav:"hasActiveSubscription"`
}

func (s *DynamoDBStore) GetEntitlement(ctx context.Context, userID string) (Entitlement, error) {
	key, err := attributevalue.MarshalMap(map[string]string{"ownerUserId": userID})
	if err != nil {
		return Entitlement{}, err
	}
	out, err := s.Client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.EntitlementTableName),
		Key:       key,
	})
	if err != nil {
		return Entitlement{}, fmt.Errorf("dynamodb GetItem %s: %w", s.EntitlementTableName, err)
	}
	if out.Item == nil {
		// No entitlement row for this user == no active plan.
		return Entitlement{OwnerUserID: userID, HasActiveSubscription: false}, nil
	}

	var item entitlementItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return Entitlement{}, fmt.Errorf("unmarshal entitlement item: %w", err)
	}
	return Entitlement(item), nil
}

// CountActiveProjects counts Project items owned by userID whose status is
// not stopped/error (data-model.md → Proyecto.status).
func (s *DynamoDBStore) CountActiveProjects(ctx context.Context, userID string) (int, error) {
	keyCond := "ownerUserId = :uid"
	filter := "#status <> :stopped AND #status <> :errorStatus"
	values, err := attributevalue.MarshalMap(map[string]string{
		":uid":         userID,
		":stopped":     "stopped",
		":errorStatus": "error",
	})
	if err != nil {
		return 0, err
	}

	out, err := s.Client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(s.ProjectTableName),
		IndexName:                 aws.String(s.ProjectsByOwnerIndex),
		KeyConditionExpression:    aws.String(keyCond),
		FilterExpression:          aws.String(filter),
		ExpressionAttributeNames:  map[string]string{"#status": "status"},
		ExpressionAttributeValues: values,
		Select:                    types.SelectCount,
	})
	if err != nil {
		return 0, fmt.Errorf("dynamodb Query %s: %w", s.ProjectTableName, err)
	}
	return int(out.Count), nil
}
