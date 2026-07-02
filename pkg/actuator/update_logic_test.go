package actuator

import (
	"sort"
	"testing"

	"github.com/llm-inferno/control-loop/pkg/backend"
	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	"github.com/llm-inferno/optimizer-light/pkg/config"
)

func TestComputeUpdates_AllocationApplied(t *testing.T) {
	serverMap := map[string]ctrl.ServerKubeInfo{
		"srv-a": {UID: "uid-a", Name: "dep-a", Space: "ns"},
	}
	allocMap := map[string]config.AllocationData{
		"srv-a": {Accelerator: "H100", NumReplicas: 3, MaxBatch: 16},
	}

	updates := ComputeUpdates(allocMap, serverMap)

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	u := updates[0]
	if u.ServerName != "srv-a" || u.DeployName != "dep-a" || u.Namespace != "ns" {
		t.Fatalf("unexpected target: %+v", u)
	}
	if u.Allocation.Accelerator != "H100" || u.Allocation.NumReplicas != 3 || u.Allocation.MaxBatch != 16 {
		t.Fatalf("unexpected allocation: %+v", u.Allocation)
	}
}

func TestComputeUpdates_NoAllocationZerosOut(t *testing.T) {
	serverMap := map[string]ctrl.ServerKubeInfo{
		"srv-a": {UID: "uid-a", Name: "dep-a", Space: "ns"},
	}
	allocMap := map[string]config.AllocationData{} // empty

	updates := ComputeUpdates(allocMap, serverMap)

	if len(updates) != 1 {
		t.Fatalf("expected 1 update (zero-out), got %d", len(updates))
	}
	u := updates[0]
	if u.Allocation.Accelerator != "" || u.Allocation.NumReplicas != 0 || u.Allocation.MaxBatch != 0 {
		t.Fatalf("expected zero allocation, got %+v", u.Allocation)
	}
	if u.Allocation.Load.ArrivalRate != 0 || u.Allocation.Load.AvgInTokens != 0 || u.Allocation.Load.AvgOutTokens != 0 {
		t.Fatalf("expected zero load, got %+v", u.Allocation.Load)
	}
}

func TestComputeUpdates_MixedSet(t *testing.T) {
	serverMap := map[string]ctrl.ServerKubeInfo{
		"srv-a": {UID: "uid-a", Name: "dep-a", Space: "ns"},
		"srv-b": {UID: "uid-b", Name: "dep-b", Space: "ns"},
		"srv-c": {UID: "uid-c", Name: "dep-c", Space: "other-ns"},
	}
	allocMap := map[string]config.AllocationData{
		"srv-a": {Accelerator: "H100", NumReplicas: 2, MaxBatch: 8},
		// srv-b and srv-c have no allocation -> should be zeroed
	}

	updates := ComputeUpdates(allocMap, serverMap)

	if len(updates) != 3 {
		t.Fatalf("expected 3 updates (one per serverMap entry), got %d", len(updates))
	}

	byName := map[string]backend.DeploymentUpdate{}
	for _, u := range updates {
		byName[u.ServerName] = u
	}
	if a := byName["srv-a"].Allocation; a.NumReplicas != 2 || a.Accelerator != "H100" {
		t.Fatalf("srv-a wrong: %+v", a)
	}
	if b := byName["srv-b"].Allocation; b.NumReplicas != 0 || b.Accelerator != "" {
		t.Fatalf("srv-b should be zeroed: %+v", b)
	}
	if c := byName["srv-c"].Allocation; c.NumReplicas != 0 || c.Accelerator != "" {
		t.Fatalf("srv-c should be zeroed: %+v", c)
	}
}

func TestComputeUpdates_AllocationWithoutServerMapEntryIsIgnored(t *testing.T) {
	// allocMap may carry servers the Collector did not include in serverMap
	// (e.g. discovered by the optimizer from static input). The Actuator can
	// only patch what it has Kube refs for; orphan allocations are dropped.
	serverMap := map[string]ctrl.ServerKubeInfo{
		"srv-a": {UID: "uid-a", Name: "dep-a", Space: "ns"},
	}
	allocMap := map[string]config.AllocationData{
		"srv-a":      {Accelerator: "H100", NumReplicas: 1},
		"srv-orphan": {Accelerator: "A100", NumReplicas: 5},
	}

	updates := ComputeUpdates(allocMap, serverMap)

	if len(updates) != 1 {
		t.Fatalf("expected 1 update (orphan dropped), got %d", len(updates))
	}
	if updates[0].ServerName != "srv-a" {
		t.Fatalf("unexpected server: %s", updates[0].ServerName)
	}
}

func TestComputeUpdates_BothEmpty(t *testing.T) {
	updates := ComputeUpdates(map[string]config.AllocationData{}, map[string]ctrl.ServerKubeInfo{})
	if len(updates) != 0 {
		t.Fatalf("expected 0 updates, got %d", len(updates))
	}
}

func TestComputeUpdates_NilMaps(t *testing.T) {
	updates := ComputeUpdates(nil, nil)
	if len(updates) != 0 {
		t.Fatalf("expected 0 updates, got %d", len(updates))
	}
}

func TestComputeUpdates_AllocOnlyEmptyServerMap(t *testing.T) {
	allocMap := map[string]config.AllocationData{
		"srv-a": {Accelerator: "H100", NumReplicas: 1},
	}
	updates := ComputeUpdates(allocMap, map[string]ctrl.ServerKubeInfo{})
	if len(updates) != 0 {
		t.Fatalf("expected 0 updates (no Kube refs to patch), got %d", len(updates))
	}
}

func TestComputeUpdates_Ordering(t *testing.T) {
	// Output order is deterministic by server name (lexical) so logs and
	// patch-error reports are stable across cycles.
	serverMap := map[string]ctrl.ServerKubeInfo{
		"srv-c": {UID: "uid-c", Name: "dep-c", Space: "ns"},
		"srv-a": {UID: "uid-a", Name: "dep-a", Space: "ns"},
		"srv-b": {UID: "uid-b", Name: "dep-b", Space: "ns"},
	}
	allocMap := map[string]config.AllocationData{}

	updates := ComputeUpdates(allocMap, serverMap)
	names := make([]string, len(updates))
	for i, u := range updates {
		names[i] = u.ServerName
	}
	expected := []string{"srv-a", "srv-b", "srv-c"}
	if !sort.IsSorted(sort.StringSlice(names)) {
		t.Fatalf("not sorted: %v", names)
	}
	for i := range expected {
		if names[i] != expected[i] {
			t.Fatalf("order mismatch: got %v, want %v", names, expected)
		}
	}
}
