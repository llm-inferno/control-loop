package actuator

import (
	"sort"
	"strconv"
	"testing"
)

// uuidGen returns a deterministic UUID factory for tests: "uuid-0", "uuid-1", ...
func uuidGen() func() string {
	i := 0
	return func() string {
		s := "uuid-" + strconv.Itoa(i)
		i++
		return s
	}
}

func TestColdStart_AllReadyNoLabels(t *testing.T) {
	managed := []PodSnapshot{
		{Name: "m-1", Namespace: "ns", Ready: true},
		{Name: "m-2", Namespace: "ns", Ready: true},
	}
	vllm := []PodSnapshot{
		{Name: "v-1", Namespace: "ns", Ready: true},
		{Name: "v-2", Namespace: "ns", Ready: true},
	}

	plan := ComputePairingPatches(managed, vllm, uuidGen())

	if len(plan.Prunes) != 0 {
		t.Fatalf("expected zero prunes, got %d", len(plan.Prunes))
	}
	if len(plan.Bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(plan.Bindings))
	}

	// UUIDs must be distinct
	seen := map[string]bool{}
	for _, b := range plan.Bindings {
		if seen[b.UUID] {
			t.Fatalf("duplicate UUID %s", b.UUID)
		}
		seen[b.UUID] = true
	}

	// Each managed pod must appear exactly once
	got := []string{plan.Bindings[0].Managed.Name, plan.Bindings[1].Managed.Name}
	sort.Strings(got)
	want := []string{"m-1", "m-2"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("managed pods in bindings: got %v, want %v", got, want)
	}
}

func TestSteadyState_AlreadyPaired_NoOp(t *testing.T) {
	managed := []PodSnapshot{
		{Name: "m-1", Namespace: "ns", Ready: true, PairID: "X"},
		{Name: "m-2", Namespace: "ns", Ready: true, PairID: "Y"},
	}
	vllm := []PodSnapshot{
		{Name: "v-1", Namespace: "ns", Ready: true, PairID: "X"},
		{Name: "v-2", Namespace: "ns", Ready: true, PairID: "Y"},
	}

	plan := ComputePairingPatches(managed, vllm, uuidGen())

	if len(plan.Prunes) != 0 || len(plan.Bindings) != 0 {
		t.Fatalf("expected no-op, got prunes=%d bindings=%d", len(plan.Prunes), len(plan.Bindings))
	}
}

func TestOrphanedUUID_PrunesAndRepairs(t *testing.T) {
	// m-1 carries pair-id "X" but no vllm pod has X.
	managed := []PodSnapshot{
		{Name: "m-1", Namespace: "ns", Ready: true, PairID: "X"},
	}
	vllm := []PodSnapshot{
		{Name: "v-1", Namespace: "ns", Ready: true, PairID: ""},
	}

	plan := ComputePairingPatches(managed, vllm, uuidGen())

	// m-1's stale label should be pruned.
	if len(plan.Prunes) != 1 || plan.Prunes[0].Name != "m-1" {
		t.Fatalf("expected one prune for m-1, got %v", plan.Prunes)
	}
	// And m-1 should be re-paired with v-1 in the same plan.
	if len(plan.Bindings) != 1 ||
		plan.Bindings[0].Managed.Name != "m-1" ||
		plan.Bindings[0].VLLM.Name != "v-1" {
		t.Fatalf("expected one binding m-1<->v-1, got %v", plan.Bindings)
	}
}

func TestMismatchedUUIDs_BothPrunedAndRepaired(t *testing.T) {
	managed := []PodSnapshot{
		{Name: "m-1", Namespace: "ns", Ready: true, PairID: "X"},
	}
	vllm := []PodSnapshot{
		{Name: "v-1", Namespace: "ns", Ready: true, PairID: "Y"},
	}

	plan := ComputePairingPatches(managed, vllm, uuidGen())

	// Both pods should be pruned (both UUIDs orphaned).
	if len(plan.Prunes) != 2 {
		t.Fatalf("expected 2 prunes, got %d", len(plan.Prunes))
	}
	// And they should be paired in the same plan with one new UUID.
	if len(plan.Bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(plan.Bindings))
	}
}

func TestAsymmetric_3Managed_2VLLM_TwoBindings(t *testing.T) {
	managed := []PodSnapshot{
		{Name: "m-1", Namespace: "ns", Ready: true},
		{Name: "m-2", Namespace: "ns", Ready: true},
		{Name: "m-3", Namespace: "ns", Ready: true},
	}
	vllm := []PodSnapshot{
		{Name: "v-1", Namespace: "ns", Ready: true},
		{Name: "v-2", Namespace: "ns", Ready: true},
	}

	plan := ComputePairingPatches(managed, vllm, uuidGen())

	if len(plan.Bindings) != 2 {
		t.Fatalf("expected 2 bindings (limited by vllm side), got %d", len(plan.Bindings))
	}
	if len(plan.Prunes) != 0 {
		t.Fatalf("expected no prunes, got %d", len(plan.Prunes))
	}
}

func TestNotReadyCarrier_PrunesPeerAndLeavesNotReadyAlone(t *testing.T) {
	// m-1 has pair-id X but is NotReady. v-1 has pair-id X and is Ready. v-2 is Ready and unpaired.
	managed := []PodSnapshot{
		{Name: "m-1", Namespace: "ns", Ready: false, PairID: "X"},
		{Name: "m-2", Namespace: "ns", Ready: true,  PairID: ""},
	}
	vllm := []PodSnapshot{
		{Name: "v-1", Namespace: "ns", Ready: true, PairID: "X"},
		{Name: "v-2", Namespace: "ns", Ready: true, PairID: ""},
	}

	plan := ComputePairingPatches(managed, vllm, uuidGen())

	// v-1 should be pruned (its peer m-1 is NotReady → unhealthy pairing).
	// m-1 should also be in prunes (defensive — clear stale ID even on NotReady pod;
	// it is harmless because the pod is being torn down or recovering).
	pruneNames := map[string]bool{}
	for _, p := range plan.Prunes {
		pruneNames[p.Name] = true
	}
	if !pruneNames["v-1"] {
		t.Fatalf("expected v-1 to be pruned, got %v", plan.Prunes)
	}
	if !pruneNames["m-1"] {
		t.Fatalf("expected m-1 to be pruned (NotReady carrier of stale pair-id), got %v", plan.Prunes)
	}

	// m-2 (Ready, unpaired) should be paired with v-2 (Ready, unpaired-effective after prune).
	if len(plan.Bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d: %+v", len(plan.Bindings), plan.Bindings)
	}
	if plan.Bindings[0].Managed.Name != "m-2" || plan.Bindings[0].VLLM.Name != "v-2" {
		t.Fatalf("expected m-2<->v-2, got %+v", plan.Bindings[0])
	}
}

func TestOrphanedUUID_VLLMSide_PrunesAndRepairs(t *testing.T) {
	// v-1 carries pair-id "X" but no managed pod has X.
	managed := []PodSnapshot{
		{Name: "m-1", Namespace: "ns", Ready: true, PairID: ""},
	}
	vllm := []PodSnapshot{
		{Name: "v-1", Namespace: "ns", Ready: true, PairID: "X"},
	}

	plan := ComputePairingPatches(managed, vllm, uuidGen())

	// v-1's stale label should be pruned.
	if len(plan.Prunes) != 1 || plan.Prunes[0].Name != "v-1" {
		t.Fatalf("expected one prune for v-1, got %v", plan.Prunes)
	}
	// And v-1 should be re-paired with m-1 in the same plan.
	if len(plan.Bindings) != 1 ||
		plan.Bindings[0].Managed.Name != "m-1" ||
		plan.Bindings[0].VLLM.Name != "v-1" {
		t.Fatalf("expected one binding m-1<->v-1, got %v", plan.Bindings)
	}
}
