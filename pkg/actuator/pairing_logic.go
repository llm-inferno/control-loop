package actuator

import "sort"

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

	// Build pair-id index from both sides.
	type entry struct {
		mPod *PodSnapshot
		vPod *PodSnapshot
	}
	idx := map[string]*entry{}
	for i := range managed {
		p := &managed[i]
		if p.PairID == "" {
			continue
		}
		if e := idx[p.PairID]; e != nil {
			e.mPod = p
		} else {
			idx[p.PairID] = &entry{mPod: p}
		}
	}
	for i := range vllm {
		p := &vllm[i]
		if p.PairID == "" {
			continue
		}
		if e := idx[p.PairID]; e != nil {
			e.vPod = p
		} else {
			idx[p.PairID] = &entry{vPod: p}
		}
	}

	// A pair-id is healthy iff both peers are present and Ready.
	pruned := map[string]bool{} // pod name + ns -> already in prunes
	keyOf := func(p PodSnapshot) string { return p.Namespace + "/" + p.Name }
	addPrune := func(p *PodSnapshot) {
		if p == nil {
			return
		}
		k := keyOf(*p)
		if pruned[k] {
			return
		}
		pruned[k] = true
		plan.Prunes = append(plan.Prunes, PodRef{Name: p.Name, Namespace: p.Namespace})
	}
	for _, e := range idx {
		healthy := e.mPod != nil && e.vPod != nil && e.mPod.Ready && e.vPod.Ready
		if !healthy {
			addPrune(e.mPod)
			addPrune(e.vPod)
		}
	}

	// Now compute who is effectively unpaired-Ready: never-labelled OR scheduled for prune.
	effectivelyUnpaired := func(pods []PodSnapshot) []PodSnapshot {
		out := make([]PodSnapshot, 0, len(pods))
		for _, p := range pods {
			if !p.Ready {
				continue
			}
			if p.PairID == "" || pruned[keyOf(p)] {
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			// Never-labelled pods sort before prune-released pods so that
			// fresh pods are preferred for new pairings.
			iNeverLabelled := out[i].PairID == ""
			jNeverLabelled := out[j].PairID == ""
			if iNeverLabelled != jNeverLabelled {
				return iNeverLabelled
			}
			return out[i].Name < out[j].Name
		})
		return out
	}
	mUnpaired := effectivelyUnpaired(managed)
	vUnpaired := effectivelyUnpaired(vllm)

	n := min(len(mUnpaired), len(vUnpaired))
	for i := 0; i < n; i++ {
		plan.Bindings = append(plan.Bindings, Pairing{
			Managed: PodRef{Name: mUnpaired[i].Name, Namespace: mUnpaired[i].Namespace},
			VLLM:    PodRef{Name: vUnpaired[i].Name, Namespace: vUnpaired[i].Namespace},
			UUID:    newUUID(),
		})
	}
	return plan
}

