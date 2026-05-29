package actuator

// PodSnapshot is the minimum pod state the pairing logic needs.
// It is intentionally decoupled from corev1.Pod so the pure logic can be
// tested without K8s API objects.
type PodSnapshot struct {
	Name      string // pod name
	Namespace string // pod namespace
	Ready     bool   // true iff status.conditions[type=Ready].status == "True"
	PairID    string // value of the inferno.server.pair-id label, "" if absent
}

// PodRef points to a single pod that needs a label change.
type PodRef struct {
	Name      string
	Namespace string
}

// Pairing is a decision to label one managed pod and one vllm pod with the same UUID.
type Pairing struct {
	Managed PodRef
	VLLM    PodRef
	UUID    string
}

// PatchPlan is the full result of one reconciliation decision.
type PatchPlan struct {
	Prunes   []PodRef  // pods whose pair-id label should be cleared
	Bindings []Pairing // new pairings to apply (after prunes)
}

// ComputePairingPatches inspects current pod snapshots and returns the prune+
// binding decisions needed to satisfy the four pairing invariants. It does no
// I/O; the caller applies the resulting patches.
//
// newUUID is injected so callers can provide deterministic IDs in tests.
func ComputePairingPatches(managed, vllm []PodSnapshot, newUUID func() string) PatchPlan {
	plan := PatchPlan{}

	// Pair unpaired-Ready pods 1:1 in deterministic order.
	mUnpaired := readyUnpaired(managed)
	vUnpaired := readyUnpaired(vllm)
	n := min2(len(mUnpaired), len(vUnpaired))
	for i := 0; i < n; i++ {
		plan.Bindings = append(plan.Bindings, Pairing{
			Managed: PodRef{Name: mUnpaired[i].Name, Namespace: mUnpaired[i].Namespace},
			VLLM:    PodRef{Name: vUnpaired[i].Name, Namespace: vUnpaired[i].Namespace},
			UUID:    newUUID(),
		})
	}
	return plan
}

func readyUnpaired(pods []PodSnapshot) []PodSnapshot {
	out := make([]PodSnapshot, 0, len(pods))
	for _, p := range pods {
		if p.Ready && p.PairID == "" {
			out = append(out, p)
		}
	}
	return out
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
