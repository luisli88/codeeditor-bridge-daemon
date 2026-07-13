package entitlements

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDynamoDBAPI struct {
	getItemOutput *dynamodb.GetItemOutput
	getItemErr    error
	queryOutput   *dynamodb.QueryOutput
	queryErr      error
}

func (f *fakeDynamoDBAPI) GetItem(
	ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options),
) (*dynamodb.GetItemOutput, error) {
	return f.getItemOutput, f.getItemErr
}

func (f *fakeDynamoDBAPI) Query(
	ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options),
) (*dynamodb.QueryOutput, error) {
	return f.queryOutput, f.queryErr
}

func TestDynamoDBStore_GetEntitlement_ItemFound(t *testing.T) {
	item, err := attributevalue.MarshalMap(entitlementItem{
		OwnerUserID: "u1", ConcurrentEnvironmentLimit: 3, HasActiveSubscription: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &DynamoDBStore{Client: &fakeDynamoDBAPI{getItemOutput: &dynamodb.GetItemOutput{Item: item}}}

	ent, err := store.GetEntitlement(context.Background(), "u1")

	if err != nil {
		t.Fatal(err)
	}
	if ent.OwnerUserID != "u1" || ent.ConcurrentEnvironmentLimit != 3 || !ent.HasActiveSubscription {
		t.Errorf("unexpected entitlement: %+v", ent)
	}
}

func TestDynamoDBStore_GetEntitlement_NoRow_MeansNoActivePlan(t *testing.T) {
	store := &DynamoDBStore{Client: &fakeDynamoDBAPI{getItemOutput: &dynamodb.GetItemOutput{Item: nil}}}

	ent, err := store.GetEntitlement(context.Background(), "u1")

	if err != nil {
		t.Fatal(err)
	}
	if ent.HasActiveSubscription {
		t.Errorf("expected no active subscription, got %+v", ent)
	}
}

func TestDynamoDBStore_GetEntitlement_ClientError_Propagates(t *testing.T) {
	store := &DynamoDBStore{Client: &fakeDynamoDBAPI{getItemErr: errors.New("boom")}}

	_, err := store.GetEntitlement(context.Background(), "u1")

	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestDynamoDBStore_CountActiveProjects_ReturnsCount(t *testing.T) {
	store := &DynamoDBStore{Client: &fakeDynamoDBAPI{queryOutput: &dynamodb.QueryOutput{Count: 2}}}

	count, err := store.CountActiveProjects(context.Background(), "u1")

	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestDynamoDBStore_CountActiveProjects_ClientError_Propagates(t *testing.T) {
	store := &DynamoDBStore{Client: &fakeDynamoDBAPI{queryErr: errors.New("boom")}}

	_, err := store.CountActiveProjects(context.Background(), "u1")

	if err == nil {
		t.Fatal("expected an error")
	}
}

// A malformed item (wrong attribute type) fails to unmarshal.
func TestDynamoDBStore_GetEntitlement_MalformedItem_ReturnsError(t *testing.T) {
	malformed := map[string]types.AttributeValue{
		"ownerUserId":                &types.AttributeValueMemberS{Value: "u1"},
		"concurrentEnvironmentLimit": &types.AttributeValueMemberS{Value: "not-a-number"},
		"hasActiveSubscription":      &types.AttributeValueMemberBOOL{Value: true},
	}
	store := &DynamoDBStore{Client: &fakeDynamoDBAPI{getItemOutput: &dynamodb.GetItemOutput{Item: malformed}}}

	_, err := store.GetEntitlement(context.Background(), "u1")

	if err == nil {
		t.Fatal("expected an error for a malformed item")
	}
}
