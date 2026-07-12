// Package entitlements implements the Entitlements Gate — a check the
// Bridge Daemon MUST run before ever provisioning or resuming an
// environment (FR-024, 02_arquitectura_solucion.md §3.4). It lives inside
// the Bridge Daemon and queries DynamoDB directly, it is not a separate
// service.
package entitlements

import (
	"context"
	"fmt"
)

// ReasonQuotaExceeded and ReasonNoActivePlan mirror the "reason" values of
// contracts/websocket-protocol.md's entitlements channel response.
const (
	ReasonQuotaExceeded = "quota-exceeded"
	ReasonNoActivePlan  = "no-active-plan"
)

// Entitlement mirrors the Entitlement item shape from
// contracts/amplify-data-schema.md.
type Entitlement struct {
	OwnerUserID                string
	ConcurrentEnvironmentLimit int
	HasActiveSubscription      bool
}

// Store is the persistence dependency of Gate — a thin seam so CheckQuota is
// unit-testable without a real DynamoDB table. DynamoDBStore (store_dynamodb.go)
// is the production implementation.
type Store interface {
	GetEntitlement(ctx context.Context, userID string) (Entitlement, error)
	CountActiveProjects(ctx context.Context, userID string) (int, error)
}

// Gate is the Entitlements Gate itself.
type Gate struct {
	store Store
}

// NewGate builds a Gate backed by store.
func NewGate(store Store) *Gate {
	return &Gate{store: store}
}

// CheckQuotaRequest matches the entitlements channel's request payload.
type CheckQuotaRequest struct {
	HostKind string // "self-hosted" | "managed" — self-hosted always allows (02_arquitectura_solucion.md §3.4 pt.4)
	UserID   string
}

// CheckQuotaResponse matches contracts/websocket-protocol.md's
// entitlements channel response payload exactly.
type CheckQuotaResponse struct {
	Allowed      bool
	Reason       *string
	CurrentUsage int
	Limit        int
}

// CheckQuota MUST be called before any call to DevPod (FR-024). It never
// calls DevPod itself — callers are responsible for stopping the
// provisioning flow when Allowed is false.
func (g *Gate) CheckQuota(ctx context.Context, req CheckQuotaRequest) (CheckQuotaResponse, error) {
	if req.HostKind == "self-hosted" {
		// 02_arquitectura_solucion.md §3.4 pt.4: no gate on self-hosted.
		return CheckQuotaResponse{Allowed: true}, nil
	}

	ent, err := g.store.GetEntitlement(ctx, req.UserID)
	if err != nil {
		return CheckQuotaResponse{}, fmt.Errorf("entitlements: get entitlement for %s: %w", req.UserID, err)
	}
	if !ent.HasActiveSubscription {
		reason := ReasonNoActivePlan
		return CheckQuotaResponse{Allowed: false, Reason: &reason, Limit: ent.ConcurrentEnvironmentLimit}, nil
	}

	usage, err := g.store.CountActiveProjects(ctx, req.UserID)
	if err != nil {
		return CheckQuotaResponse{}, fmt.Errorf("entitlements: count active projects for %s: %w", req.UserID, err)
	}
	if usage >= ent.ConcurrentEnvironmentLimit {
		reason := ReasonQuotaExceeded
		return CheckQuotaResponse{Allowed: false, Reason: &reason, CurrentUsage: usage, Limit: ent.ConcurrentEnvironmentLimit}, nil
	}

	return CheckQuotaResponse{Allowed: true, CurrentUsage: usage, Limit: ent.ConcurrentEnvironmentLimit}, nil
}
