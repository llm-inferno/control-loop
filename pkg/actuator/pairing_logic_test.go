package actuator

import (
	"sort"
	"testing"
)

// uuidGen returns a deterministic UUID factory for tests: "uuid-0", "uuid-1", ...
func uuidGen() func() string {
	i := 0
	return func() string {
		s := "uuid-" + itoa(i)
		i++
		return s
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
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
