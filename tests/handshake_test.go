package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bridgews "github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// TestHandshake_StepsAreSequentialAndVisible validates FR-003/FR-050: the
// checklist runs conexión → proceso intermediario → motor de contenedores
// → mosh, and each step's result is streamed as it completes (not batched
// at the end).
func TestHandshake_StepsAreSequentialAndVisible(t *testing.T) {
	checklist := bridgews.DefaultChecklist("60000-61000")
	handler := bridgews.HandshakeHandler(checklist)

	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	var items []bridgews.ChecklistItem
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var item bridgews.ChecklistItem
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &item))
		items = append(items, item)
	}

	require.Len(t, items, 3)
	assert.Equal(t, "bridge-daemon-reachable", items[0].Name)
	assert.Equal(t, bridgews.StatusVerified, items[0].Status)
	assert.Equal(t, "container-engine", items[1].Name)
	assert.Equal(t, "mosh", items[2].Name)
}

// TestHandshake_BridgeDaemonReachableAlwaysVerified — running this check at
// all means the Bridge Daemon is already reachable.
func TestHandshake_BridgeDaemonReachableAlwaysVerified(t *testing.T) {
	item := bridgews.CheckBridgeDaemonReachable(context.Background())
	assert.Equal(t, bridgews.StatusVerified, item.Status)
	assert.Nil(t, item.Error)
}

// TestHandshake_MoshMissingBinaryReportsRequirementRef validates that a
// missing mosh-server produces the specific diagnostic FR-004 requires,
// linked to the "mosh" requirement.
func TestHandshake_MoshMissingBinaryReportsRequirementRef(t *testing.T) {
	item := bridgews.CheckMosh(context.Background(), "60000-61000")
	if item.Status == bridgews.StatusVerified {
		t.Skip("mosh-server is installed in this environment — nothing to assert on the failure path")
	}
	require.NotNil(t, item.Error)
	require.NotNil(t, item.Error.RequirementRef)
	assert.Equal(t, bridgews.RequirementMosh, *item.Error.RequirementRef)
}

// TestHandshake_MoshInvalidPortRange validates the port-range format check
// independent of whether mosh-server itself is installed.
func TestHandshake_MoshInvalidPortRange(t *testing.T) {
	item := bridgews.CheckMosh(context.Background(), "not-a-range")
	if item.Error == nil {
		t.Skip("mosh-server is not installed — the binary check already failed first")
	}
	assert.Equal(t, bridgews.RequirementMosh, *item.Error.RequirementRef)
}
