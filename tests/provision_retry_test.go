package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/bootstrap"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpod"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/entitlements"
)

type allowAllStore struct{}

func (allowAllStore) GetEntitlement(ctx context.Context, userID string) (entitlements.Entitlement, error) {
	return entitlements.Entitlement{OwnerUserID: userID, HasActiveSubscription: true, ConcurrentEnvironmentLimit: 5}, nil
}

func (allowAllStore) CountActiveProjects(ctx context.Context, userID string) (int, error) {
	return 0, nil
}

type denyStore struct{}

func (denyStore) GetEntitlement(ctx context.Context, userID string) (entitlements.Entitlement, error) {
	return entitlements.Entitlement{OwnerUserID: userID, HasActiveSubscription: false}, nil
}

func (denyStore) CountActiveProjects(ctx context.Context, userID string) (int, error) {
	return 0, nil
}

type provisionerFakes struct {
	cloneCalls             int
	cloneShouldFail        bool
	devcontainerCalls      int
	devPodUpCalls          int
	devPodUpShouldFail     bool
	devPodDeleteCalls      int
	devPodDeleteWorkspace  string
	devPodDeleteShouldFail bool
	deleteCloneCalls       int
	deleteClonePath        string
	deleteCloneShouldFail  bool
}

func newProvisioner(gate *entitlements.Gate, fakes *provisionerFakes) *devpod.Provisioner {
	return devpod.NewProvisioner(
		gate,
		func(workspaceID string) string { return "/efs/" + workspaceID },
		func(ctx context.Context, workspaceID, repositoryURL string, credential *devpod.CloneCredential) error {
			fakes.cloneCalls++
			if fakes.cloneShouldFail {
				return errors.New("clone failed")
			}
			return nil
		},
		func(ctx context.Context, secretRef string) (string, error) {
			return "resolved-secret", nil
		},
		func(workspacePath string) (map[string]any, error) {
			fakes.devcontainerCalls++
			return map[string]any{"image": "mcr.microsoft.com/devcontainers/go:latest"}, nil
		},
		func(workspacePath string) []string { return []string{"go"} },
		func(ctx context.Context, workspacePath string) error {
			fakes.devPodUpCalls++
			if fakes.devPodUpShouldFail {
				return errors.New("devpod up failed")
			}
			return nil
		},
		func(ctx context.Context, workspaceID string, languages []string) []bootstrap.LanguageServer {
			servers := make([]bootstrap.LanguageServer, len(languages))
			for i, lang := range languages {
				servers[i] = bootstrap.LanguageServer{Language: lang, Status: bootstrap.LanguageServerStatusReady}
			}
			return servers
		},
		func(ctx context.Context, workspaceID string) error { return nil },
		func(ctx context.Context, workspaceID string) error {
			fakes.devPodDeleteCalls++
			fakes.devPodDeleteWorkspace = workspaceID
			if fakes.devPodDeleteShouldFail {
				return errors.New("devpod delete failed")
			}
			return nil
		},
		func(workspacePath string) error {
			fakes.deleteCloneCalls++
			fakes.deleteClonePath = workspacePath
			if fakes.deleteCloneShouldFail {
				return errors.New("delete clone failed")
			}
			return nil
		},
	)
}

func TestProvision_AllStepsSucceed_ReachesReady(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	fakes := &provisionerFakes{}
	p := newProvisioner(gate, fakes)

	result := p.Provision(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})

	assert.Equal(t, devpod.StatusReady, result.Status)
	assert.Equal(t, 1, fakes.cloneCalls)
	for _, step := range result.Steps {
		assert.Equal(t, devpod.StepDone, step.Status, "step %s should be done", step.Name)
	}
}

// FR-024
func TestProvision_QuotaExceeded_StopsBeforeCloning(t *testing.T) {
	gate := entitlements.NewGate(denyStore{})
	fakes := &provisionerFakes{}
	p := newProvisioner(gate, fakes)

	result := p.Provision(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})

	assert.Equal(t, devpod.StatusError, result.Status)
	assert.Equal(t, 0, fakes.cloneCalls, "must not clone when the gate denies provisioning")
	require.NotNil(t, result.Reason)
	assert.Equal(t, entitlements.ReasonNoActivePlan, *result.Reason)
}

// FR-023: a clone failure with no credential at all reads as "this repo
// needs one", not a generic error.
func TestProvision_CloneFailsWithNoCredential_ReportsPendingCredential(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	fakes := &provisionerFakes{cloneShouldFail: true}
	p := newProvisioner(gate, fakes)

	result := p.Provision(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})

	assert.Equal(t, devpod.StatusPendingCredential, result.Status)
}

// FR-028: retry only the failed step, without re-cloning.
func TestRetryStep_AfterDevPodUpFailure_RetriesOnlyThatStepWithoutRecloning(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	fakes := &provisionerFakes{devPodUpShouldFail: true}
	p := newProvisioner(gate, fakes)

	result := p.Provision(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})
	require.Equal(t, devpod.StatusError, result.Status)
	assert.Equal(t, 1, fakes.cloneCalls)
	assert.Equal(t, 1, fakes.devPodUpCalls)

	fakes.devPodUpShouldFail = false
	p.RetryStep(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	}, result, devpod.StepDevPodUp)

	assert.Equal(t, 1, fakes.cloneCalls, "retrying devpod-up must not re-clone")
	assert.Equal(t, 2, fakes.devPodUpCalls)

	var devPodStepStatus devpod.StepStatus
	for _, step := range result.Steps {
		if step.Name == devpod.StepDevPodUp {
			devPodStepStatus = step.Status
		}
	}
	assert.Equal(t, devpod.StepDone, devPodStepStatus)
}

// A failed step used to leave the Desarrollador with only a red X and no
// way to tell *why* — Error is what "sale un error, pero no sé cuál" was
// actually missing.
func TestProvision_DevPodUpFails_StepErrorHasTheMessage(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	fakes := &provisionerFakes{devPodUpShouldFail: true}
	p := newProvisioner(gate, fakes)

	result := p.Provision(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})

	var devPodStep devpod.ProvisioningStep
	for _, step := range result.Steps {
		if step.Name == devpod.StepDevPodUp {
			devPodStep = step
		}
	}
	require.NotNil(t, devPodStep.Error)
	assert.Equal(t, "devpod up failed", *devPodStep.Error)
}

// A retry that actually succeeds shouldn't leave the previous attempt's
// error message sitting there next to a green check.
func TestRetryStep_Succeeds_ClearsThePreviousError(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	fakes := &provisionerFakes{devPodUpShouldFail: true}
	p := newProvisioner(gate, fakes)
	result := p.Provision(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	})

	fakes.devPodUpShouldFail = false
	p.RetryStep(context.Background(), devpod.Request{
		WorkspaceID: "ws-1", OwnerUserID: "user-1", HostKind: "managed", RepositoryURL: "https://github.com/x/y.git",
	}, result, devpod.StepDevPodUp)

	var devPodStep devpod.ProvisioningStep
	for _, step := range result.Steps {
		if step.Name == devpod.StepDevPodUp {
			devPodStep = step
		}
	}
	assert.Nil(t, devPodStep.Error)
}

func TestDeprovision_DeletesDevPodWorkspaceAndClonedRepository(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	fakes := &provisionerFakes{}
	p := newProvisioner(gate, fakes)

	err := p.Deprovision(context.Background(), "ws-1")

	require.NoError(t, err)
	assert.Equal(t, 1, fakes.devPodDeleteCalls)
	assert.Equal(t, "ws-1", fakes.devPodDeleteWorkspace)
	assert.Equal(t, 1, fakes.deleteCloneCalls)
	assert.Equal(t, "/efs/ws-1", fakes.deleteClonePath)
}

// Both teardown steps must run even if one fails — a container that
// won't delete shouldn't leave the clone on disk, and vice versa.
func TestDeprovision_OneStepFails_StillRunsTheOtherAndReportsTheError(t *testing.T) {
	gate := entitlements.NewGate(allowAllStore{})
	fakes := &provisionerFakes{devPodDeleteShouldFail: true}
	p := newProvisioner(gate, fakes)

	err := p.Deprovision(context.Background(), "ws-1")

	require.Error(t, err)
	assert.Equal(t, 1, fakes.devPodDeleteCalls)
	assert.Equal(t, 1, fakes.deleteCloneCalls, "must still delete the clone even though devpod delete failed")
}
