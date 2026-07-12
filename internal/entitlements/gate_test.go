package entitlements

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	entitlement   Entitlement
	activeCount   int
	entitlementErr error
	countErr       error
}

func (f *fakeStore) GetEntitlement(ctx context.Context, userID string) (Entitlement, error) {
	return f.entitlement, f.entitlementErr
}

func (f *fakeStore) CountActiveProjects(ctx context.Context, userID string) (int, error) {
	return f.activeCount, f.countErr
}

func TestCheckQuota_SelfHostedAlwaysAllowed(t *testing.T) {
	g := NewGate(&fakeStore{}) // store never consulted for self-hosted
	res, err := g.CheckQuota(context.Background(), CheckQuotaRequest{HostKind: "self-hosted", UserID: "u1"})
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Nil(t, res.Reason)
}

func TestCheckQuota_NoActivePlan(t *testing.T) {
	store := &fakeStore{entitlement: Entitlement{OwnerUserID: "u1", HasActiveSubscription: false, ConcurrentEnvironmentLimit: 1}}
	g := NewGate(store)
	res, err := g.CheckQuota(context.Background(), CheckQuotaRequest{HostKind: "managed", UserID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	require.NotNil(t, res.Reason)
	assert.Equal(t, ReasonNoActivePlan, *res.Reason)
}

func TestCheckQuota_LimitReached(t *testing.T) {
	store := &fakeStore{
		entitlement: Entitlement{OwnerUserID: "u1", HasActiveSubscription: true, ConcurrentEnvironmentLimit: 2},
		activeCount: 2,
	}
	g := NewGate(store)
	res, err := g.CheckQuota(context.Background(), CheckQuotaRequest{HostKind: "managed", UserID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	require.NotNil(t, res.Reason)
	assert.Equal(t, ReasonQuotaExceeded, *res.Reason)
	assert.Equal(t, 2, res.CurrentUsage)
	assert.Equal(t, 2, res.Limit)
}

func TestCheckQuota_Allowed(t *testing.T) {
	store := &fakeStore{
		entitlement: Entitlement{OwnerUserID: "u1", HasActiveSubscription: true, ConcurrentEnvironmentLimit: 3},
		activeCount: 1,
	}
	g := NewGate(store)
	res, err := g.CheckQuota(context.Background(), CheckQuotaRequest{HostKind: "managed", UserID: "u1"})
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Nil(t, res.Reason)
	assert.Equal(t, 1, res.CurrentUsage)
	assert.Equal(t, 3, res.Limit)
}
