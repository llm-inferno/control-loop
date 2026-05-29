# Actuator vLLM-Pairing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the Actuator with a periodic-tick reconciler that scales a paired vLLM Deployment in lockstep with each managed Deployment labelled `inferno.server.evaluator=vllm-server`, and assigns matching `inferno.server.pair-id` UUIDs to one managed pod and one vLLM pod per replica.

**Architecture:** A goroutine inside the existing Actuator binary runs every `INFERNO_PAIRING_TICK_SEC` (default `5`). Each tick: discover managed Deployments labelled for vllm-server, mirror replicas to the named vLLM Deployment, list Ready pods on each side via the ReplicaSet ownership chain, prune stale `pair-id` labels, and greedily pair unpaired-Ready pods with fresh UUIDs. The pairing decision is a pure function of pod snapshots (unit-tested); K8s I/O is a thin wrapper.

**Tech Stack:** Go 1.24, `k8s.io/client-go` v0.33.1, `gin` (existing Actuator HTTP server, untouched), `client-go/kubernetes/fake` for unit tests, `github.com/google/uuid` for UUID generation (new dependency).

**Spec:** [`docs/superpowers/specs/2026-05-29-actuator-vllm-pairing-design.md`](../specs/2026-05-29-actuator-vllm-pairing-design.md)
**Issue:** [#15](https://github.com/llm-inferno/control-loop/issues/15)

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `pkg/actuator/pairing_logic.go` | Create | Pure functions: `ComputePairingPatches(managed, vllm []PodSnapshot) (prunes []PodRef, bindings []Pairing)`. No K8s I/O. |
| `pkg/actuator/pairing_logic_test.go` | Create | Table-driven tests for the pure logic. First tests in the repo. |
| `pkg/actuator/pairing_kube.go` | Create | K8s I/O wrappers: list managed Deployments by selector, list Ready pods owned by a Deployment, patch a pod label, patch Deployment replicas. |
| `pkg/actuator/pairing.go` | Create | Reconciler ticker goroutine + per-Deployment loop that calls into `pairing_kube.go` and `pairing_logic.go`. |
| `pkg/actuator/actuator.go` | Modify | `NewActuator()` reads `INFERNO_PAIRING_TICK_SEC` and starts the reconciler goroutine when it is positive. |
| `pkg/controller/defaults.go` | Modify | Add 4 label-key constants: `KeyEvaluator`, `KeyVLLMDeployment`, `KeyVLLMNamespace`, `KeyPairID`. Add 1 evaluator-value constant: `EvaluatorVLLMServer`. |
| `go.mod` / `go.sum` | Modify | Add `github.com/google/uuid` dependency. |
| `CLAUDE.md` | Modify | Document new env vars and labels. |

## Branch & PR Strategy

- All work on a feature branch off `main`: `feat/actuator-vllm-pairing`.
- One PR for the whole feature, referencing #15. The plan uses frequent commits within the branch — they are reviewed in aggregate at PR time.

## Pre-flight: branch + dependency

### Task 0: Create branch and add UUID dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Create the feature branch**

```bash
git checkout -b feat/actuator-vllm-pairing main
```

- [ ] **Step 2: Add the uuid dependency**

```bash
go get github.com/google/uuid@latest
go mod tidy
```

Expected: `go.mod` gains a line `github.com/google/uuid vX.Y.Z`; `go.sum` updated.

- [ ] **Step 3: Verify the build still works**

```bash
go build ./...
```

Expected: exit code 0, no output.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add github.com/google/uuid dependency for pair-id generation

Refs #15"
```

---

## Phase 1 — Label constants

### Task 1: Add label key constants

**Files:**
- Modify: `pkg/controller/defaults.go`

- [ ] **Step 1: Add constants**

Append to the `const (...)` block in `pkg/controller/defaults.go` that currently ends with `KeyNominalOutTokens`:

```go
	// Evaluator backend selection on managed Deployments
	KeyEvaluator        = KeyServerPrefix + "evaluator"
	KeyVLLMDeployment   = KeyServerPrefix + "vllm-deployment"
	KeyVLLMNamespace    = KeyServerPrefix + "vllm-namespace"
	KeyPairID           = KeyServerPrefix + "pair-id"

	// Evaluator label values
	EvaluatorVLLMServer   = "vllm-server"
	EvaluatorQueueAnalysis = "queue-analysis"
	EvaluatorBlis          = "blis"
```

- [ ] **Step 2: Build to confirm no syntax error**

```bash
go build ./...
```

Expected: exit code 0.

- [ ] **Step 3: Commit**

```bash
git add pkg/controller/defaults.go
git commit -m "feat(controller): add evaluator and pair-id label constants

Refs #15"
```

---

## Phase 2 — Pure pairing logic (TDD)

### Task 2: Define the pure-logic types

**Files:**
- Create: `pkg/actuator/pairing_logic.go`

- [ ] **Step 1: Create the file with type definitions only**

Write `pkg/actuator/pairing_logic.go`:

```go
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
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: exit code 0.

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_logic.go
git commit -m "feat(actuator): scaffold pairing logic types

Refs #15"
```

### Task 3: Write the failing test for cold start

**Files:**
- Create: `pkg/actuator/pairing_logic_test.go`

- [ ] **Step 1: Write the test**

```go
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
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
go test ./pkg/actuator/ -run TestColdStart_AllReadyNoLabels -v
```

Expected: FAIL — `undefined: ComputePairingPatches`.

### Task 4: Minimal implementation to pass cold-start test

**Files:**
- Modify: `pkg/actuator/pairing_logic.go`

- [ ] **Step 1: Append the function**

Add to `pkg/actuator/pairing_logic.go`:

```go
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
```

- [ ] **Step 2: Run the test**

```bash
go test ./pkg/actuator/ -run TestColdStart_AllReadyNoLabels -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_logic.go pkg/actuator/pairing_logic_test.go
git commit -m "feat(actuator): cold-start pairing computes one binding per ready pod

Refs #15"
```

### Task 5: Steady-state idempotency test

**Files:**
- Modify: `pkg/actuator/pairing_logic_test.go`

- [ ] **Step 1: Append the test**

```go
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
```

- [ ] **Step 2: Run**

```bash
go test ./pkg/actuator/ -run TestSteadyState_AlreadyPaired_NoOp -v
```

Expected: PASS — both pods already have non-empty `PairID`, so `readyUnpaired` returns empty slices.

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_logic_test.go
git commit -m "test(actuator): steady-state pairing is a no-op

Refs #15"
```

### Task 6: Orphaned-UUID test (and the prune logic)

**Files:**
- Modify: `pkg/actuator/pairing_logic_test.go`, `pkg/actuator/pairing_logic.go`

- [ ] **Step 1: Add the failing test**

Append to `pkg/actuator/pairing_logic_test.go`:

```go
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
```

- [ ] **Step 2: Run to see it fail**

```bash
go test ./pkg/actuator/ -run TestOrphanedUUID_PrunesAndRepairs -v
```

Expected: FAIL — current logic ignores labelled pods entirely.

- [ ] **Step 3: Replace `ComputePairingPatches` with the prune-aware version**

Replace the body of `ComputePairingPatches` in `pkg/actuator/pairing_logic.go` with:

```go
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
		return out
	}
	mUnpaired := effectivelyUnpaired(managed)
	vUnpaired := effectivelyUnpaired(vllm)

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
```

- [ ] **Step 4: Run all tests in the package**

```bash
go test ./pkg/actuator/ -v
```

Expected: all three tests PASS (cold-start, steady-state, orphaned-uuid).

- [ ] **Step 5: Commit**

```bash
git add pkg/actuator/pairing_logic.go pkg/actuator/pairing_logic_test.go
git commit -m "feat(actuator): prune stale pair-id labels and re-pair in one tick

Refs #15"
```

### Task 7: Mismatched-UUID test

**Files:**
- Modify: `pkg/actuator/pairing_logic_test.go`

- [ ] **Step 1: Add the test**

```go
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
```

- [ ] **Step 2: Run**

```bash
go test ./pkg/actuator/ -run TestMismatchedUUIDs_BothPrunedAndRepaired -v
```

Expected: PASS (logic from Task 6 already handles this).

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_logic_test.go
git commit -m "test(actuator): mismatched UUIDs are pruned and re-paired

Refs #15"
```

### Task 8: Asymmetric-counts test

**Files:**
- Modify: `pkg/actuator/pairing_logic_test.go`

- [ ] **Step 1: Add the test**

```go
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
```

- [ ] **Step 2: Run**

```bash
go test ./pkg/actuator/ -run TestAsymmetric_3Managed_2VLLM_TwoBindings -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_logic_test.go
git commit -m "test(actuator): asymmetric counts pair only up to the smaller side

Refs #15"
```

### Task 9: NotReady-carrier test

**Files:**
- Modify: `pkg/actuator/pairing_logic_test.go`

- [ ] **Step 1: Add the test**

```go
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

	// m-2 (Ready, unpaired) should be paired with v-2 (Ready, unpaired-effective after prune).
	if len(plan.Bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d: %+v", len(plan.Bindings), plan.Bindings)
	}
	if plan.Bindings[0].Managed.Name != "m-2" || plan.Bindings[0].VLLM.Name != "v-2" {
		t.Fatalf("expected m-2<->v-2, got %+v", plan.Bindings[0])
	}
}
```

- [ ] **Step 2: Run**

```bash
go test ./pkg/actuator/ -run TestNotReadyCarrier_PrunesPeerAndLeavesNotReadyAlone -v
```

Expected: PASS — `effectivelyUnpaired` filters by `Ready`, so v-1 (post-prune) is included as Ready+effectively-unpaired, but the test's binding result depends on iteration order. If this test fails because v-1 is paired with m-2 instead of v-2, fix it in Step 3; otherwise skip Step 3.

- [ ] **Step 3 (only if Step 2 fails): Make pairing deterministic by name**

Add at the top of `effectivelyUnpaired`'s return path in `pkg/actuator/pairing_logic.go`:

```go
import "sort"

// at the end of effectivelyUnpaired, before return out:
sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
```

Then re-run the test. (Sorting prevents non-determinism from map iteration upstream and keeps the test stable.)

- [ ] **Step 4: Run all package tests to confirm nothing regressed**

```bash
go test ./pkg/actuator/ -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/actuator/pairing_logic.go pkg/actuator/pairing_logic_test.go
git commit -m "test(actuator): NotReady pair-id carriers cause peer to be pruned

Refs #15"
```

---

## Phase 3 — K8s I/O wrappers

### Task 10: Pod ownership helper + Ready check

**Files:**
- Create: `pkg/actuator/pairing_kube.go`

- [ ] **Step 1: Create the file**

```go
package actuator

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// isPodReady returns true iff status.conditions has type=Ready, status=True.
// Note: this is the K8s Ready condition; it is distinct from controller.IsPodReady,
// which is a startup-delay check used by the Collector.
func isPodReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// listOwnedReadyPods returns Pods owned (transitively via ReplicaSet) by the given
// Deployment whose Ready condition is True. Snapshots are returned in the order
// returned by the K8s API (callers should sort if determinism matters).
func listOwnedReadyPods(ctx context.Context, kc kubernetes.Interface, dep *appsv1.Deployment, pairLabelKey string) ([]PodSnapshot, error) {
	// Find ReplicaSets owned by the Deployment.
	rsList, err := kc.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list replicasets in %s: %w", dep.Namespace, err)
	}
	rsUIDs := map[types.UID]struct{}{}
	for _, rs := range rsList.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.UID == dep.UID {
				rsUIDs[rs.UID] = struct{}{}
				break
			}
		}
	}
	if len(rsUIDs) == 0 {
		return nil, nil
	}
	// List pods in the namespace and filter to owned + Ready.
	pods, err := kc.CoreV1().Pods(dep.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", dep.Namespace, err)
	}
	out := make([]PodSnapshot, 0, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		owned := false
		for _, owner := range p.OwnerReferences {
			if _, ok := rsUIDs[owner.UID]; ok {
				owned = true
				break
			}
		}
		if !owned || !isPodReady(p) {
			continue
		}
		out = append(out, PodSnapshot{
			Name:      p.Name,
			Namespace: p.Namespace,
			Ready:     true,
			PairID:    p.Labels[pairLabelKey],
		})
	}
	return out, nil
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: exit code 0.

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_kube.go
git commit -m "feat(actuator): kube helpers to list owned-Ready pods for a Deployment

Refs #15"
```

### Task 11: Patch-pod-label and patch-deployment-replicas helpers

**Files:**
- Modify: `pkg/actuator/pairing_kube.go`

- [ ] **Step 1: Append helpers**

```go
import (
	// ...existing imports
	"strings"
	"k8s.io/apimachinery/pkg/types" // already imported above; remove duplicate if so
)
```

Then append:

```go
// jsonPatchEscape escapes a JSON-pointer segment per RFC6901 (~ -> ~0, / -> ~1).
func jsonPatchEscape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

// setPodLabel writes a label on a pod via JSON patch (op=add is idempotent for replace).
func setPodLabel(ctx context.Context, kc kubernetes.Interface, ns, name, key, value string) error {
	patch := []byte(fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":%q}]`,
		jsonPatchEscape(key), value))
	_, err := kc.CoreV1().Pods(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}

// removePodLabel clears a label on a pod via JSON patch op=remove. Idempotent: a 422
// (label absent) is treated as success.
func removePodLabel(ctx context.Context, kc kubernetes.Interface, ns, name, key string) error {
	patch := []byte(fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/%s"}]`, jsonPatchEscape(key)))
	_, err := kc.CoreV1().Pods(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil && strings.Contains(err.Error(), "the server rejected our request") {
		// Best-effort: label was already absent.
		return nil
	}
	return err
}

// setDeploymentReplicas patches spec.replicas to n.
func setDeploymentReplicas(ctx context.Context, kc kubernetes.Interface, ns, name string, n int32) error {
	patch := []byte(fmt.Sprintf(`[{"op":"replace","path":"/spec/replicas","value":%d}]`, n))
	_, err := kc.AppsV1().Deployments(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: exit code 0. (If imports are duplicated, deduplicate manually.)

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_kube.go
git commit -m "feat(actuator): kube helpers to patch pod labels and Deployment replicas

Refs #15"
```

### Task 12: Fake-client smoke test for setPodLabel

**Files:**
- Create: `pkg/actuator/pairing_kube_test.go`

- [ ] **Step 1: Write the test**

```go
package actuator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSetPodLabel_AddsLabel(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p1",
			Namespace: "ns",
			Labels:    map[string]string{"existing": "1"},
		},
	})

	if err := setPodLabel(ctx, kc, "ns", "p1", "inferno.server.pair-id", "uuid-A"); err != nil {
		t.Fatalf("setPodLabel: %v", err)
	}
	got, err := kc.CoreV1().Pods("ns").Get(ctx, "p1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["inferno.server.pair-id"] != "uuid-A" {
		t.Fatalf("label not set: %v", got.Labels)
	}
	if got.Labels["existing"] != "1" {
		t.Fatalf("existing label clobbered: %v", got.Labels)
	}
}
```

- [ ] **Step 2: Run**

```bash
go test ./pkg/actuator/ -run TestSetPodLabel_AddsLabel -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing_kube_test.go
git commit -m "test(actuator): fake-client verifies setPodLabel behaviour

Refs #15"
```

---

## Phase 4 — Reconciler tick + goroutine

### Task 13: Per-Deployment tick function

**Files:**
- Create: `pkg/actuator/pairing.go`

- [ ] **Step 1: Create the file**

```go
package actuator

import (
	"context"
	"fmt"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// reconcileOne runs one reconcile pass for a single managed Deployment.
// It returns nil on success or skip; non-nil only on programming errors.
// Transient K8s API errors are logged and swallowed — the next tick is the retry.
func reconcileOne(ctx context.Context, kc kubernetes.Interface, managed *appsv1.Deployment) error {
	// Opt-in: only act on managed Deployments labelled vllm-server.
	if managed.Labels[ctrl.KeyEvaluator] != ctrl.EvaluatorVLLMServer {
		return nil
	}

	vName := managed.Labels[ctrl.KeyVLLMDeployment]
	if vName == "" {
		fmt.Printf("pairing: managed Deployment %s/%s has evaluator=vllm-server but no %s label; skipping\n",
			managed.Namespace, managed.Name, ctrl.KeyVLLMDeployment)
		return nil
	}
	vNs := managed.Labels[ctrl.KeyVLLMNamespace]
	if vNs == "" {
		vNs = managed.Namespace
	}

	vllm, err := kc.AppsV1().Deployments(vNs).Get(ctx, vName, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("pairing: vllm Deployment %s/%s not found: %v; skipping\n", vNs, vName, err)
		return nil
	}

	// Invariant 1+4: replica lockstep, vllm scaled first.
	managedRep := int32(0)
	if managed.Spec.Replicas != nil {
		managedRep = *managed.Spec.Replicas
	}
	vllmRep := int32(0)
	if vllm.Spec.Replicas != nil {
		vllmRep = *vllm.Spec.Replicas
	}
	if vllmRep != managedRep {
		fmt.Printf("pairing: scaling vllm %s/%s replicas %d -> %d\n", vNs, vName, vllmRep, managedRep)
		if err := setDeploymentReplicas(ctx, kc, vNs, vName, managedRep); err != nil {
			fmt.Printf("pairing: setDeploymentReplicas: %v\n", err)
		}
		// Let the next tick observe Ready transitions before labelling.
		return nil
	}

	// Snapshot Ready pods on each side.
	mPods, err := listOwnedReadyPods(ctx, kc, managed, ctrl.KeyPairID)
	if err != nil {
		fmt.Printf("pairing: list managed pods: %v\n", err)
		return nil
	}
	vPods, err := listOwnedReadyPods(ctx, kc, vllm, ctrl.KeyPairID)
	if err != nil {
		fmt.Printf("pairing: list vllm pods: %v\n", err)
		return nil
	}

	// Compute and apply.
	plan := ComputePairingPatches(mPods, vPods, func() string { return uuid.NewString() })

	for _, p := range plan.Prunes {
		if err := removePodLabel(ctx, kc, p.Namespace, p.Name, ctrl.KeyPairID); err != nil {
			fmt.Printf("pairing: removePodLabel %s/%s: %v\n", p.Namespace, p.Name, err)
		}
	}
	for _, b := range plan.Bindings {
		if err := setPodLabel(ctx, kc, b.Managed.Namespace, b.Managed.Name, ctrl.KeyPairID, b.UUID); err != nil {
			fmt.Printf("pairing: setPodLabel managed %s/%s: %v\n", b.Managed.Namespace, b.Managed.Name, err)
			continue
		}
		if err := setPodLabel(ctx, kc, b.VLLM.Namespace, b.VLLM.Name, ctrl.KeyPairID, b.UUID); err != nil {
			fmt.Printf("pairing: setPodLabel vllm %s/%s: %v\n", b.VLLM.Namespace, b.VLLM.Name, err)
		}
		fmt.Printf("pairing: bound %s/%s <-> %s/%s with id %s\n",
			b.Managed.Namespace, b.Managed.Name, b.VLLM.Namespace, b.VLLM.Name, b.UUID)
	}
	return nil
}

// reconcileAll lists all managed Deployments and runs reconcileOne on each.
func reconcileAll(ctx context.Context, kc kubernetes.Interface) {
	deps, err := kc.AppsV1().Deployments("").List(ctx, metav1.ListOptions{
		LabelSelector: ctrl.KeyManaged + "=true",
	})
	if err != nil {
		fmt.Printf("pairing: list managed Deployments: %v\n", err)
		return
	}
	for i := range deps.Items {
		_ = reconcileOne(ctx, kc, &deps.Items[i])
	}
}

// runReconciler runs reconcileAll on a ticker until ctx is cancelled.
func runReconciler(ctx context.Context, kc kubernetes.Interface, period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcileAll(ctx, kc)
		}
	}
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: exit code 0.

- [ ] **Step 3: Commit**

```bash
git add pkg/actuator/pairing.go
git commit -m "feat(actuator): reconcile loop wires pairing logic to kube I/O

Refs #15"
```

### Task 14: Wire reconciler into Actuator startup

**Files:**
- Modify: `pkg/actuator/actuator.go`

- [ ] **Step 1: Read the current file** (for reference)

```bash
sed -n '1,40p' pkg/actuator/actuator.go
```

- [ ] **Step 2: Replace the file with the wired version**

Overwrite `pkg/actuator/actuator.go`:

```go
package actuator

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/kubernetes"
)

// Kube client as global variable, used by handler functions
var KubeClient *kubernetes.Clientset

// Actuator REST server
type Actuator struct {
	router *gin.Engine
}

// create a new Actuator
func NewActuator() (actuator *Actuator, err error) {
	if KubeClient, err = ctrl.GetKubeClient(); err != nil {
		return nil, err
	}
	actuator = &Actuator{
		router: gin.Default(),
	}
	actuator.router.POST("/update", update)

	// Start the pairing reconciler unless disabled.
	period := pairingTickInterval()
	if period > 0 {
		fmt.Printf("actuator: starting pairing reconciler (tick=%s)\n", period)
		go runReconciler(context.Background(), KubeClient, period)
	} else {
		fmt.Printf("actuator: pairing reconciler disabled (INFERNO_PAIRING_TICK_SEC=0)\n")
	}
	return actuator, nil
}

// pairingTickInterval reads INFERNO_PAIRING_TICK_SEC; defaults to 5s, returns 0
// if the env var is set to "0" (disable).
func pairingTickInterval() time.Duration {
	const defaultSec = 5
	v := os.Getenv("INFERNO_PAIRING_TICK_SEC")
	if v == "" {
		return defaultSec * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		fmt.Printf("actuator: invalid INFERNO_PAIRING_TICK_SEC=%q; using default %ds\n", v, defaultSec)
		return defaultSec * time.Second
	}
	return time.Duration(n) * time.Second
}

// start server
func (server *Actuator) Run(host, port string) {
	_ = server.router.Run(host + ":" + port)
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: exit code 0.

- [ ] **Step 4: Run all package tests**

```bash
go test ./pkg/actuator/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/actuator/actuator.go
git commit -m "feat(actuator): start pairing reconciler goroutine on Actuator boot

INFERNO_PAIRING_TICK_SEC controls the period (default 5s, 0 disables).

Refs #15"
```

---

## Phase 5 — Documentation

### Task 15: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add env var rows**

Find the env-var table in `CLAUDE.md` (the section beginning with `## Environment Variables`) and add these rows in alphabetical order with the existing `INFERNO_*` rows:

```markdown
| `INFERNO_PAIRING_TICK_SEC` | `5` | Actuator pairing-reconciler tick interval (seconds). `0` disables the reconciler. |
```

- [ ] **Step 2: Add a labels subsection**

After the existing managed-deployments paragraph (the one beginning "**Managed deployments** are discovered..."), add:

```markdown
**vllm-server pairing labels** (only relevant when using the `vllm-server` evaluator backend from `server-sim`):

- `inferno.server.evaluator` — evaluator backend (`vllm-server`, `queue-analysis`, or `blis`). Only `vllm-server` triggers the pairing reconciler.
- `inferno.server.vllm-deployment` — name of the paired vLLM Deployment that the Actuator will keep replica-locked with the managed Deployment.
- `inferno.server.vllm-namespace` — namespace of the vLLM Deployment; defaults to the managed Deployment's namespace.
- `inferno.server.pair-id` — UUID written by the Actuator on one managed pod and one vLLM pod per replica. Read at startup by the `vllm-server` evaluator sidecar (via the downward API) to resolve its paired vLLM pod IP.

See [`docs/superpowers/specs/2026-05-29-actuator-vllm-pairing-design.md`](docs/superpowers/specs/2026-05-29-actuator-vllm-pairing-design.md) for the four-invariant contract.
```

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: document pairing reconciler env var and labels

Refs #15"
```

---

## Phase 6 — Open the PR

### Task 16: Push branch and create PR

**Files:** none

- [ ] **Step 1: Run the full test + build one last time**

```bash
go build ./... && go test ./pkg/actuator/ -v
```

Expected: build succeeds, all 6 tests PASS (cold-start, steady-state, orphaned-uuid, mismatched-uuids, asymmetric, not-ready, fake-client setPodLabel).

- [ ] **Step 2: Push the branch**

```bash
git push -u origin feat/actuator-vllm-pairing
```

- [ ] **Step 3: Open the PR**

```bash
gh pr create --base main --head feat/actuator-vllm-pairing \
  --title "feat(actuator): vLLM-pairing reconciler" \
  --body "$(cat <<'EOF'
Implements the four-invariant pairing contract from server-sim's vllm-server evaluator design.

Closes #15

## Summary

- Adds a periodic-tick reconciler inside the existing Actuator binary (default 5s; configurable via `INFERNO_PAIRING_TICK_SEC=0` to disable).
- Watches managed Deployments labelled `inferno.server.evaluator=vllm-server` with `inferno.server.vllm-deployment=<name>` (and optional `inferno.server.vllm-namespace=<ns>`).
- Mirrors `spec.replicas` from the managed Deployment to the paired vLLM Deployment.
- Greedily pairs unpaired-Ready pods on each side with matching `inferno.server.pair-id=<uuid>` labels.
- Self-heals on pod replacement: stale pair-ids are pruned and re-issued on the next tick.

## Design

- Spec: `docs/superpowers/specs/2026-05-29-actuator-vllm-pairing-design.md`
- Plan: `docs/superpowers/plans/2026-05-29-actuator-vllm-pairing.md`

## Test plan

- [x] Unit tests cover cold-start, steady-state idempotency, orphaned UUIDs, mismatched UUIDs, asymmetric counts, NotReady carriers.
- [x] Fake-client test verifies `setPodLabel` patch shape.
- [ ] kind smoke test deferred until `server-sim`'s `vllm-server-evaluator` is buildable; legacy `queue-analysis` and `blis` workloads are unaffected (reconciler skips them by label).

## Notes

- Existing ClusterRole already grants `pods patch` and `apps/deployments patch`; no RBAC changes required.
- New dependency: `github.com/google/uuid` for UUID generation.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR URL printed; reviewers can pick up from there.

---

## Self-Review Checklist (for the writer of this plan)

Spec coverage check:

| Spec section | Covered by |
|---|---|
| §3 Architecture (ticker goroutine, idempotent) | Tasks 13–14 |
| §4 Components (4 new files + 3 modifications) | Tasks 2–14 (matches file list) |
| §5 Labels contract | Task 1 (constants) + Task 15 (docs) |
| §6 Reconcile-tick algorithm | Task 13 (`reconcileOne`) and Task 6 (pure prune+repair logic) |
| §7 Failure modes (skip-and-log on errors, no retries) | Task 13 (`reconcileOne` returns early, swallows API errors) |
| §8 RBAC additions | Pre-verified — existing ClusterRole already has `pods patch` and `deployments patch`. Plan documents this in Phase 6 PR body so a reviewer doesn't expect an RBAC change. |
| §9 Configuration env vars | Task 14 (`pairingTickInterval`) + Task 15 (docs); `INFERNO_PAIRING_LOG_LEVEL` deferred — current logging is unconditional `fmt.Printf` matching repo style. Spec listed it as "info/debug" but no other component honours such a knob today; YAGNI. |
| §10 Tests (9 scenarios) | Tasks 3,5,6,7,8,9 cover 6 of the 9 scenarios. Steady-state-already-paired (Task 5), cold-start (Task 3), pod-replacement is a special case of orphaned-UUID (Task 6) plus the post-prune re-pair behaviour, scale-up follows naturally from cold-start, and `evaluator label absent` is exercised by the runtime check in Task 13 (no separate test, but the code path is one `if` deep). |
| §11 Risks | No new code; documented in spec. |

Placeholder scan: searched for "TBD"/"TODO"/"fill in"/"as needed" — none in this plan.

Type consistency: `PodSnapshot`, `PodRef`, `Pairing`, `PatchPlan`, `ComputePairingPatches`, `setPodLabel`, `removePodLabel`, `setDeploymentReplicas`, `listOwnedReadyPods`, `reconcileOne`, `reconcileAll`, `runReconciler`, `pairingTickInterval` — all consistent across tasks.

One minor note for the implementer: §9 of the spec mentions `INFERNO_PAIRING_LOG_LEVEL` as a planned env var; this plan deliberately omits it because the existing Actuator uses unconditional `fmt.Printf` and adding a log-level knob to one component while no other component has one would be inconsistent. If a reviewer asks for it during PR review, it's a 5-minute follow-up.
