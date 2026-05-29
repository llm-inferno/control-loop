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
