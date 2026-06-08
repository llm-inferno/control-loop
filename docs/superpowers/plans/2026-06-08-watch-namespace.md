# WATCH_NAMESPACE Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `WATCH_NAMESPACE` env var that scopes the Collector, Load Emulator, and Actuator pairing reconciler's managed-deployment lists to a single namespace; refactor the Actuator `/update` handler to drive its work from the Collector-built `serverMap` instead of an independent cluster-wide list. Empty/unset preserves current cluster-wide behavior.

**Architecture:** One small helper in `pkg/controller` reads `os.Getenv("WATCH_NAMESPACE")` and is called at three list-call sites. The Actuator `/update` handler drops its cluster-wide list entirely; its zero-replica logic is extracted into a pure function (`computeUpdates`) that operates on the `(allocMap, serverMap)` it already receives, mirroring the existing `pkg/actuator/pairing_logic.go` + `pairing_logic_test.go` convention.

**Tech Stack:** Go 1.22+, `client-go` (`k8s.io/client-go/kubernetes`), `optimizer-light/pkg/config` types.

**Spec:** [`docs/superpowers/specs/2026-06-08-watch-namespace-design.md`](../specs/2026-06-08-watch-namespace-design.md). Issues: [#33](https://github.com/llm-inferno/control-loop/issues/33), follow-up [#34](https://github.com/llm-inferno/control-loop/issues/34).

**Branch:** `feat/watch-namespace-filter` (already pushed; spec committed).

---

## File Structure

| File | Purpose | Change |
|---|---|---|
| `pkg/controller/defaults.go` | env-var name constants | **Modify**: add `WatchNamespaceEnvName` |
| `pkg/controller/utils.go` | helper functions | **Modify**: add `WatchNamespace()` |
| `pkg/collector/handlers.go` | `/collect` handler | **Modify**: scope `Deployments("")` list |
| `pkg/loademulator/loademulator.go` | load-emulator main loop | **Modify**: scope `Deployments("")` list |
| `pkg/actuator/pairing.go` | pairing reconciler tick | **Modify**: scope `Deployments("")` list |
| `pkg/actuator/update_logic.go` | pure update-selection logic | **Create** |
| `pkg/actuator/update_logic_test.go` | unit test for `computeUpdates` | **Create** |
| `pkg/actuator/handlers.go` | `/update` handler | **Modify**: replace cluster list with `computeUpdates` |
| `CLAUDE.md` | env-var table | **Modify**: add `WATCH_NAMESPACE` row |

---

## Task 1: Add env-var name and helper

**Files:**
- Modify: `pkg/controller/defaults.go:21-22` (adjacent to other env-var consts)
- Modify: `pkg/controller/utils.go` (add at end of file before `IsPodReady`)

- [ ] **Step 1: Add the env-var name constant**

In `pkg/controller/defaults.go`, find the block that ends at line 22:

```go
	TunerHostEnvName = "TUNER_HOST"
	TunerPortEnvName = "TUNER_PORT"
```

Add the new const immediately after, separated by a blank line, before `DataPathEnvName`:

```go
	TunerHostEnvName = "TUNER_HOST"
	TunerPortEnvName = "TUNER_PORT"

	// WatchNamespaceEnvName scopes the managed-deployment watch to a single
	// namespace. Empty/unset means cluster-wide (default; backwards compatible).
	WatchNamespaceEnvName = "WATCH_NAMESPACE"

	DataPathEnvName          = "INFERNO_DATA_PATH"
```

- [ ] **Step 2: Add the helper function**

In `pkg/controller/utils.go`, add this function above `IsPodReady` (line 234):

```go
// WatchNamespace returns the namespace inferno should watch for managed
// Deployments. Empty string means cluster-wide (default; backwards compatible).
func WatchNamespace() string {
	return os.Getenv(WatchNamespaceEnvName)
}
```

`os` is already imported in this file (line 9), so no import change is needed.

- [ ] **Step 3: Verify the build**

Run: `go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 4: Commit**

```bash
git add pkg/controller/defaults.go pkg/controller/utils.go
git commit -m "$(cat <<'EOF'
feat(watch-namespace): add WATCH_NAMESPACE env var and helper

WatchNamespace() returns os.Getenv(WatchNamespaceEnvName); empty string
means cluster-wide (current behaviour). Call sites land in subsequent
commits.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Scope Collector `/collect` to `WATCH_NAMESPACE`

**Files:**
- Modify: `pkg/collector/handlers.go:25-28`

- [ ] **Step 1: Replace the cluster-wide list**

The current code at `pkg/collector/handlers.go:25-28`:

```go
	// get managed deployments
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector})
```

Replace with:

```go
	// get managed deployments (scoped to WATCH_NAMESPACE if set; empty means cluster-wide)
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments(ctrl.WatchNamespace()).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector})
```

- [ ] **Step 2: Verify the build**

Run: `go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 3: Verify no other cluster-wide list crept in**

Run: `grep -n 'Deployments("")' pkg/collector/`
Expected: no output (we changed the only one).

- [ ] **Step 4: Commit**

```bash
git add pkg/collector/handlers.go
git commit -m "$(cat <<'EOF'
feat(collector): scope managed-deployment list to WATCH_NAMESPACE

Replaces the cluster-wide Deployments("") list with the helper-driven
namespace argument. Empty WATCH_NAMESPACE preserves current behaviour.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Scope Load Emulator to `WATCH_NAMESPACE`

**Files:**
- Modify: `pkg/loademulator/loademulator.go:66-69`

- [ ] **Step 1: Replace the cluster-wide list**

The current code at `pkg/loademulator/loademulator.go:66-69`:

```go
		// get deployments
		labelSelector := ctrl.KeyManaged + "=true"
		deps, err := lg.kubeClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector})
```

Replace with:

```go
		// get deployments (scoped to WATCH_NAMESPACE if set; empty means cluster-wide)
		labelSelector := ctrl.KeyManaged + "=true"
		deps, err := lg.kubeClient.AppsV1().Deployments(ctrl.WatchNamespace()).List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector})
```

The downstream `ReplicaSets(namespace)` and `Pods(namespace)` calls (lines 139, 155) are already scoped to `d.Namespace` and require no change.

- [ ] **Step 2: Verify the build**

Run: `go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 3: Verify no other cluster-wide list crept in**

Run: `grep -n 'Deployments("")' pkg/loademulator/`
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add pkg/loademulator/loademulator.go
git commit -m "$(cat <<'EOF'
feat(loademulator): scope managed-deployment list to WATCH_NAMESPACE

Replaces the cluster-wide Deployments("") list with the helper-driven
namespace argument. Empty WATCH_NAMESPACE preserves current behaviour.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Scope Actuator pairing reconciler to `WATCH_NAMESPACE`

**Files:**
- Modify: `pkg/actuator/pairing.go:131-134`

- [ ] **Step 1: Replace the cluster-wide list**

The current code at `pkg/actuator/pairing.go:131-134`:

```go
func reconcileAll(ctx context.Context, kc kubernetes.Interface) {
	deps, err := kc.AppsV1().Deployments("").List(ctx, metav1.ListOptions{
		LabelSelector: ctrl.KeyManaged + "=true",
	})
```

Replace with:

```go
func reconcileAll(ctx context.Context, kc kubernetes.Interface) {
	// scoped to WATCH_NAMESPACE if set; empty means cluster-wide
	deps, err := kc.AppsV1().Deployments(ctrl.WatchNamespace()).List(ctx, metav1.ListOptions{
		LabelSelector: ctrl.KeyManaged + "=true",
	})
```

- [ ] **Step 2: Verify the build (and that pairing tests still pass)**

Run: `go test ./pkg/actuator/...`
Expected: PASS (pairing_logic_test.go and pairing_kube_test.go run; no failures).

- [ ] **Step 3: Verify no other cluster-wide list crept in**

Run: `grep -n 'Deployments("")' pkg/actuator/`
Expected: still hits `pkg/actuator/handlers.go` (we'll fix that in Task 6 via refactor).

- [ ] **Step 4: Commit**

```bash
git add pkg/actuator/pairing.go
git commit -m "$(cat <<'EOF'
feat(actuator): scope pairing reconciler list to WATCH_NAMESPACE

Replaces the cluster-wide Deployments("") list in reconcileAll with the
helper-driven namespace argument. Empty WATCH_NAMESPACE preserves
current behaviour. /update handler refactored separately.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Extract pure `computeUpdates` logic with unit tests

The Actuator `/update` refactor is split in two: this task adds a pure function and tests; Task 6 wires it in. Mirrors the `pairing_logic.go` + `pairing_logic_test.go` pattern already in the package.

**Files:**
- Create: `pkg/actuator/update_logic.go`
- Create: `pkg/actuator/update_logic_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/actuator/update_logic_test.go` with:

```go
package actuator

import (
	"sort"
	"testing"

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

	byName := map[string]DeploymentUpdate{}
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
```

- [ ] **Step 2: Run the test to verify it fails (no impl yet)**

Run: `go test ./pkg/actuator/ -run TestComputeUpdates -v`
Expected: FAIL — `undefined: ComputeUpdates` and `undefined: DeploymentUpdate`.

- [ ] **Step 3: Write the minimal implementation**

Create `pkg/actuator/update_logic.go` with:

```go
package actuator

import (
	"sort"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// DeploymentUpdate is the resolved patch target for a single managed server.
// The Actuator translates each entry into a JSON-patch on the matching
// Deployment.
type DeploymentUpdate struct {
	ServerName string
	UID        string
	DeployName string
	Namespace  string
	Allocation config.AllocationData
}

// ComputeUpdates produces one DeploymentUpdate per serverMap entry, applying
// the optimizer's allocation when present and the zero allocation otherwise.
//
// The set of updates is exactly serverMap (the Collector's view); allocations
// for server names not in serverMap are dropped because the Actuator has no
// Kube reference for them. Output is sorted by ServerName for stable logging.
func ComputeUpdates(
	allocMap map[string]config.AllocationData,
	serverMap map[string]ctrl.ServerKubeInfo,
) []DeploymentUpdate {
	names := make([]string, 0, len(serverMap))
	for name := range serverMap {
		names = append(names, name)
	}
	sort.Strings(names)

	updates := make([]DeploymentUpdate, 0, len(names))
	for _, name := range names {
		info := serverMap[name]
		alloc, ok := allocMap[name]
		if !ok {
			alloc = config.AllocationData{} // zero value: replicas=0, accelerator="", load=0
		}
		updates = append(updates, DeploymentUpdate{
			ServerName: name,
			UID:        info.UID,
			DeployName: info.Name,
			Namespace:  info.Space,
			Allocation: alloc,
		})
	}
	return updates
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/actuator/ -run TestComputeUpdates -v`
Expected: PASS for all 5 sub-tests.

- [ ] **Step 5: Run the full actuator test suite to confirm no regression**

Run: `go test ./pkg/actuator/...`
Expected: PASS (existing pairing tests still pass).

- [ ] **Step 6: Commit**

```bash
git add pkg/actuator/update_logic.go pkg/actuator/update_logic_test.go
git commit -m "$(cat <<'EOF'
feat(actuator): extract ComputeUpdates pure logic with unit tests

Mirrors the existing pairing_logic.go + pairing_logic_test.go pattern.
Output set is driven by serverMap; allocations not in serverMap are
dropped (no Kube ref to patch). Stable lexical ordering for log
determinism. Wired into /update handler in the next commit.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Refactor Actuator `/update` to use `ComputeUpdates`

This drops the cluster-wide `Deployments("").List` call from the handler and drives both loops from the pure function.

**Files:**
- Modify: `pkg/actuator/handlers.go:1-127` (full rewrite of the file)

- [ ] **Step 1: Replace the file contents**

Overwrite `pkg/actuator/handlers.go` with:

```go
package actuator

import (
	"context"
	"fmt"
	"net/http"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Handlers for REST API calls

func update(c *gin.Context) {
	var info ctrl.ServerActuatorInfo
	if err := c.BindJSON(&info); err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "binding error: " + err.Error()})
		return
	}

	// Drive updates from the Collector-built serverMap. The set zeroed out is
	// {serverMap - allocMap}: any managed deployment the Collector saw for
	// which the Optimizer did not return an allocation gets replicas=0.
	updates := ComputeUpdates(info.Spec, info.KubeResource)

	for _, u := range updates {
		if err := patchDeployment(u.ServerName, u.DeployName, u.Namespace, &u.Allocation); err != nil {
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "kube client: " + err.Error()})
			return
		}
	}

	c.IndentedJSON(http.StatusOK, "Done")
}

// patchDeployment applies the optimizer's allocation (or the zero allocation
// for "no feasible solution") to a single managed Deployment. The Deployment
// is identified by name + namespace; no full v1.Deployment lookup is needed.
func patchDeployment(serverName, deployName, nameSpace string, allocData *config.AllocationData) error {
	acceleratorName := allocData.Accelerator
	numReplicas := int32(allocData.NumReplicas)
	maxBatchSize := allocData.MaxBatch

	patchAcc := fmt.Sprintf(`{"op": "replace", "path": "/metadata/labels/%s", "value": "%s"}`, ctrl.KeyAccelerator, acceleratorName)
	patchBatch := fmt.Sprintf(`{"op": "replace", "path": "/metadata/labels/%s", "value": "%d"}`, ctrl.KeyMaxBatchSize, maxBatchSize)
	patchRep := fmt.Sprintf(`{"op": "replace", "path": "/spec/replicas", "value": %d}`, numReplicas)
	patchAll := []byte(`[` + patchAcc + `,` + patchBatch + `,` + patchRep + `]`)

	arrivalRateRPM := allocData.Load.ArrivalRate
	curInTokens := allocData.Load.AvgInTokens
	curOutTokens := allocData.Load.AvgOutTokens
	fmt.Printf("srv=[%s/%s]: arrivalRateRPM=%.2f; inTok=%d; outTok=%d; acc=%s; num=%d; batch=%d \n",
		serverName, nameSpace,
		arrivalRateRPM, curInTokens, curOutTokens,
		acceleratorName, numReplicas, maxBatchSize)

	if _, err := KubeClient.AppsV1().Deployments(nameSpace).Patch(context.Background(), deployName,
		types.JSONPatchType, patchAll, metav1.PatchOptions{}); err != nil {
		return err
	}
	return nil
}
```

Notes on the rewrite:

- The `Deployments("").List(...)` call and both for-loops are gone.
- `patchDeployment` no longer takes a `v1.Deployment`. The previous Printf used `d.Labels[ctrl.KeyServerClass]`, `d.Labels[ctrl.KeyServerModel]`, `d.Labels[ctrl.KeyAccelerator]`, `d.Labels[ctrl.KeyMaxBatchSize]`, and `*d.Spec.Replicas` for "before" values. Those required the Deployment object. The replacement Printf reports the **new** values being patched plus identifying info (server name, namespace) — `kubectl get deployment <name>` shows the prior state if needed for debugging, and the Collector log shows it for live operation. Drops the dependency without losing observability.
- Imports dropped: `v1 "k8s.io/api/apps/v1"` (no longer used) and `"strconv"` (was only used to parse the prior `KeyMaxBatchSize` label for the "before" Printf, which is gone).

- [ ] **Step 2: Verify the build and tests**

Run: `go build ./...`
Expected: no output, exit code 0.

Run: `go test ./pkg/actuator/...`
Expected: PASS (ComputeUpdates tests + pairing tests).

- [ ] **Step 3: Verify no `Deployments("")` remain in the project**

Run: `grep -rn 'Deployments("")' pkg/ cmd/`
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add pkg/actuator/handlers.go
git commit -m "$(cat <<'EOF'
refactor(actuator): drop cluster-wide list in /update

The /update handler now drives both loops from info.KubeResource (the
Collector-built serverMap), so the set of zeroed deployments becomes
{serverMap - allocMap} instead of {clusterScan - allocMap}. The
Actuator no longer reads inferno.server.managed labels at all; the
Collector is the source of truth for "which deployments are managed".

Architectural correction surfaced during spec review on 2026-06-08.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Document `WATCH_NAMESPACE` in CLAUDE.md

**Files:**
- Modify: `CLAUDE.md` (env-var table, around line 142)

- [ ] **Step 1: Add the env-var row**

Edit `CLAUDE.md`. Find the line:

```
| `INFERNO_CYCLE_LOG` | `inferno-cycles.jsonl` | Path to JSONL cycle log written by the controller each cycle. Set to `-` to disable. |
| `KUBECONFIG` | `$HOME/.kube/config` | Kubernetes config path |
```

Insert a new row immediately before the `KUBECONFIG` row:

```
| `INFERNO_CYCLE_LOG` | `inferno-cycles.jsonl` | Path to JSONL cycle log written by the controller each cycle. Set to `-` to disable. |
| `WATCH_NAMESPACE` | unset (cluster-wide) | Namespace to scope managed-deployment watches to. Set on shared clusters where another inferno setup uses the same `inferno.server.*` labels in different namespaces. Applies to the Collector, Load Emulator, and Actuator pairing reconciler. The Actuator `/update` handler is implicitly scoped via the Collector-built `serverMap` it receives. |
| `KUBECONFIG` | `$HOME/.kube/config` | Kubernetes config path |
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(watch-namespace): add WATCH_NAMESPACE to env-var table

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Backwards-compat smoke test (kind, no `WATCH_NAMESPACE` set)

This is the verification step. The change is supposed to be a no-op when `WATCH_NAMESPACE` is unset. Run an existing kind scenario end-to-end and confirm the controller still scales the two managed deployments.

**Prerequisite:** sibling repos checked out under the same parent (`../optimizer-light`, `../model-tuner`, `../server-sim`), Docker daemon running, kind available.

- [ ] **Step 1: Build the inferno-loop image**

```bash
docker build -t quay.io/atantawi/inferno-loop:latest . --load
```

Expected: build succeeds.

- [ ] **Step 2: (Re)create kind cluster if needed and run the qa deploy**

```bash
kind get clusters | grep -q kind-cluster || kind create cluster --name kind-cluster
scripts/qa/kind-deploy.sh
```

Expected: pods come up; `kubectl get pods -A | grep -E 'inferno|infer'` shows `inferno` (1/1) and 2 managed deployment pods Running within ~2 minutes.

- [ ] **Step 3: Watch one control cycle**

```bash
kubectl logs -n inferno deployment/inferno -c controller --tail=50
```

Expected: at least one cycle log line of the form `collect: ...ms tune: ...ms optimize: ...ms actuate: ...ms total: ...ms`. The Actuator log lines (`srv=[...]: arrivalRateRPM=...`) appear once per managed deployment per cycle (2 per cycle for `qa`).

- [ ] **Step 4: Confirm the load emulator updates 2 deployments per tick (not 0, not 4)**

```bash
kubectl logs -n inferno pod/load-emulator --tail=20
```

Expected: lines reading `2 deployment(s) updated`.

- [ ] **Step 5: Trigger an explicit cycle and confirm patch happens**

```bash
kubectl exec -n inferno deployment/inferno -c controller -- wget -qO- http://localhost:3300/invoke
kubectl logs -n inferno deployment/inferno -c controller --tail=20
```

Expected: a fresh cycle line; Actuator log shows `srv=[dep-qa-granite/...]` and `srv=[dep-qa-llama/...]` Printf lines.

- [ ] **Step 6: Tear down**

```bash
scripts/common/kind-teardown.sh
```

Expected: cluster goes away cleanly.

- [ ] **Step 7: No commit needed for verification — work was already committed in Tasks 1–7.**

---

## Task 9: Open the pull request

- [ ] **Step 1: Push any outstanding commits**

```bash
git push
```

Expected: branch `feat/watch-namespace-filter` is up to date on origin.

- [ ] **Step 2: Open the PR against `main`**

```bash
gh pr create --base main --title "feat(watch-namespace): scope managed-deployment watches to a single namespace" --body "$(cat <<'EOF'
## Summary

- Adds `WATCH_NAMESPACE` env var (operator-sdk convention). Empty/unset preserves current cluster-wide behaviour (backwards compatible); non-empty scopes managed-deployment lists to that namespace.
- Wires the helper at three sites: Collector `/collect`, Load Emulator `Run`, Actuator pairing reconciler.
- Refactors Actuator `/update` to drop its independent cluster-wide list and drive both loops from `info.KubeResource` (the Collector-built `serverMap`). The set zeroed out becomes `{serverMap − allocMap}` instead of `{clusterScan − allocMap}` — architectural correction surfaced during spec review.
- Documents the new env var in `CLAUDE.md`.

Closes #33. Companion follow-up tracked as #34 (configurable managed-label key/value, the symmetric counterpart that protects against another team's cluster-wide watch).

Spec: [`docs/superpowers/specs/2026-06-08-watch-namespace-design.md`](docs/superpowers/specs/2026-06-08-watch-namespace-design.md)
Plan: [`docs/superpowers/plans/2026-06-08-watch-namespace.md`](docs/superpowers/plans/2026-06-08-watch-namespace.md)

## Test plan

- [x] `go build ./...` passes
- [x] `go test ./pkg/actuator/...` passes (5 new ComputeUpdates tests + existing pairing tests)
- [x] `grep -rn 'Deployments("")' pkg/ cmd/` returns no hits
- [x] kind `qa` scenario deploys end-to-end with `WATCH_NAMESPACE` unset; controller cycles, load emulator reports `2 deployment(s) updated`, Actuator patches both managed deployments per cycle
- [ ] (Out of scope here, follow-up on `feat/vllm-gpu-experiment`) Wire `WATCH_NAMESPACE=inferno-workload` into `scripts/vllm-gpu/oc-deploy.sh` and `manifests/vllm-gpu/load-emulator.yaml`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR URL printed.

- [ ] **Step 3: Return the PR URL**

Print the PR URL so the user can review.

---

## Self-Review Notes (writer's checklist, completed)

- **Spec coverage:** Each spec section (env var, helper, call-site changes, actuator refactor, manifests, docs, testing, limitation, risks) has a corresponding task or explicit non-task entry. Manifest/script changes are explicitly out-of-scope per the spec ("This branch ships the code change and CLAUDE.md doc only").
- **Placeholders:** None. Each step shows full code or full command.
- **Type consistency:** `ComputeUpdates` returns `[]DeploymentUpdate`; the type's fields (`ServerName`, `UID`, `DeployName`, `Namespace`, `Allocation`) are used identically across `update_logic.go`, the test file, and the refactored `handlers.go`.
- **Backwards compat:** Tasks 2–4 are one-line argument swaps preserving the empty-string semantic; Task 6 verified by Task 5's tests + Task 8's kind run; CLAUDE.md row reflects the actual scope (three sites + implicit `/update` scoping).
