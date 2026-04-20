# Optimizer perfParms Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add guards in `optimizer-light` so that (1) a model/accelerator combination is skipped when `perfParms` are all zero (missing/uninitialized), and (2) the optimizer returns an error when no feasible allocation exists for any server.

**Architecture:** Two targeted changes in `pkg/core/allocation.go` (guard + division-by-zero fix) and one in `pkg/solver/solver.go` (error return). These are the only files that need to change in `optimizer-light`. Two downstream `go.mod` bumps follow in the `optimizer` and `control-loop` repos.

**Tech Stack:** Go 1.24, `optimizer-light` repo at `../optimizer-light` relative to `control-loop`, `optimizer` repo at `../optimizer`.

---

## File Map

| File | Change |
|---|---|
| `../optimizer-light/pkg/core/allocation.go` | Add zero-perfParms guard (non-zero load path); fix division-by-zero in `zeroLoadAllocation` |
| `../optimizer-light/pkg/core/allocation_test.go` | New: three tests for the two allocation.go changes |
| `../optimizer-light/pkg/solver/solver.go` | Return error when any server has no allocation after solving |
| `../optimizer-light/pkg/solver/solver_test.go` | New: one test for the solver error return |
| `../optimizer/go.mod` | Bump `optimizer-light` to new local replace (then tagged version) |
| `go.mod` (control-loop) | Bump `optimizer-light` to new local replace (then tagged version) |

---

### Task 1: Test + guard zero perfParms in `CreateAllocation`

**Files:**
- Create: `../optimizer-light/pkg/core/allocation_test.go`
- Modify: `../optimizer-light/pkg/core/allocation.go` (after line 73, zero-load check)

The guard belongs in the **non-zero load** path: after the zero-load early-return on line 73, before building the queue analyzer on line 90. Zero-load servers legitimately bypass this guard since perfParms are not needed to keep a minimum replica count running.

- [ ] **Step 1: Write the failing tests**

Create `../optimizer-light/pkg/core/allocation_test.go`:

```go
package core

import (
	"testing"

	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// newTestSystem builds the minimal System for allocation tests:
// one accelerator (H100), one model (m1) with given perfParms,
// one service class (Premium) with ITL/TTFT targets for m1,
// one server (s1) with the given load.
func newTestSystem(perfParms config.PerfParms, arrivalRate float32, minReplicas int) {
	sys := NewSystem()
	TheSystem = sys

	sys.SetAcceleratorsFromSpec(&config.AcceleratorData{
		Spec: []config.AcceleratorSpec{
			{Name: "H100", Type: "H100", Multiplicity: 1, Cost: 75},
		},
	})
	sys.SetCapacityFromSpec(&config.CapacityData{
		Count: []config.AcceleratorCount{{Type: "H100", Count: 8}},
	})
	sys.SetModelsFromSpec(&config.ModelData{
		PerfData: []config.ModelAcceleratorPerfData{
			{Name: "m1", Acc: "H100", AccCount: 1, MaxBatchSize: 16, AtTokens: 512,
				PerfParms: perfParms},
		},
	})
	sys.SetServiceClassesFromSpec(&config.ServiceClassData{
		Spec: []config.ServiceClassSpec{
			{Name: "Premium", Priority: 1, ModelTargets: []config.ModelTarget{
				{Model: "m1", SLO_ITL: 100, SLO_TTFT: 2000},
			}},
		},
	})
	sys.SetServersFromSpec(&config.ServerData{
		Spec: []config.ServerSpec{
			{Name: "s1", Class: "Premium", Model: "m1",
				MinNumReplicas: minReplicas,
				CurrentAlloc: config.AllocationData{
					Load: config.ServerLoadSpec{
						ArrivalRate:  arrivalRate,
						AvgInTokens:  512,
						AvgOutTokens: 512,
					},
				},
			},
		},
	})
}

// Zero perfParms + non-zero load: CreateAllocation must return nil (guard fires).
func TestCreateAllocation_ZeroPerfParms_NonZeroLoad_ReturnsNil(t *testing.T) {
	newTestSystem(config.PerfParms{Alpha: 0, Beta: 0, Gamma: 0}, 60, 1)
	alloc := CreateAllocation("s1", "H100")
	if alloc != nil {
		t.Errorf("expected nil allocation for zero perfParms with non-zero load, got %v", alloc)
	}
}

// Zero perfParms + zero load: CreateAllocation must return non-nil (zeroLoadAllocation path,
// perfParms not needed).
func TestCreateAllocation_ZeroPerfParms_ZeroLoad_ReturnsNonNil(t *testing.T) {
	newTestSystem(config.PerfParms{Alpha: 0, Beta: 0, Gamma: 0}, 0, 0)
	alloc := CreateAllocation("s1", "H100")
	if alloc == nil {
		t.Error("expected non-nil allocation for zero load even with zero perfParms")
	}
}

// Zero perfParms + zero load + minReplicas > 0: zeroLoadAllocation must not produce +Inf
// MaxArrvRatePerReplica (division-by-zero when maxServTime == 0).
func TestCreateAllocation_ZeroPerfParms_ZeroLoad_NonZeroMinReplicas_NoInf(t *testing.T) {
	newTestSystem(config.PerfParms{Alpha: 0, Beta: 0, Gamma: 0}, 0, 2)
	alloc := CreateAllocation("s1", "H100")
	if alloc == nil {
		t.Fatal("expected non-nil allocation for zero load with minReplicas=2")
	}
	if alloc.MaxArrvRatePerReplica() != 0 {
		t.Errorf("expected MaxArrvRatePerReplica=0 for zero perfParms, got %v", alloc.MaxArrvRatePerReplica())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd ../optimizer-light && go test ./pkg/core/... -run TestCreateAllocation -v
```

Expected: `FAIL` — `TestCreateAllocation_ZeroPerfParms_NonZeroLoad_ReturnsNil` passes (no guard yet, actually may return nil already from queue analyzer error), while `TestCreateAllocation_ZeroPerfParms_ZeroLoad_NonZeroMinReplicas_NoInf` fails with `MaxArrvRatePerReplica` being `+Inf`.

Note: The non-zero-load test may already pass by accident (queue analyzer returns an error for zero parms which propagates as nil), but the guard must be explicit. Verify the zero-load/+Inf test fails before proceeding.

- [ ] **Step 3: Add perfParms guard to `CreateAllocation` (non-zero load path)**

In `../optimizer-light/pkg/core/allocation.go`, insert after line 73 (the zero-load return), before the `K := load.AvgOutTokens` line:

```go
	// handle zero traffic case
	if load.ArrivalRate == 0 || load.AvgOutTokens == 0 {
		return zeroLoadAllocation(server, model, acc, perf)
	}

	// guard: skip this model/accelerator combination if perfParms are uninitialized
	if perf.PerfParms.Alpha == 0 && perf.PerfParms.Beta == 0 && perf.PerfParms.Gamma == 0 {
		return nil
	}

	// calculate max batch size (N) based on average request length (K)
```

- [ ] **Step 4: Fix division-by-zero in `zeroLoadAllocation`**

In `../optimizer-light/pkg/core/allocation.go`, replace lines 277–278:

Old:
```go
	maxServTime := prefillTime + maxDecodeTime
	maxArrvRatePerReplica := float32(maxBatchSize) / maxServTime
```

New:
```go
	maxServTime := prefillTime + maxDecodeTime
	maxArrvRatePerReplica := float32(0)
	if maxServTime > 0 {
		maxArrvRatePerReplica = float32(maxBatchSize) / maxServTime
	}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd ../optimizer-light && go test ./pkg/core/... -run TestCreateAllocation -v
```

Expected:
```
--- PASS: TestCreateAllocation_ZeroPerfParms_NonZeroLoad_ReturnsNil
--- PASS: TestCreateAllocation_ZeroPerfParms_ZeroLoad_ReturnsNonNil
--- PASS: TestCreateAllocation_ZeroPerfParms_ZeroLoad_NonZeroMinReplicas_NoInf
```

- [ ] **Step 6: Run full test suite**

```bash
cd ../optimizer-light && go test ./...
```

Expected: all pass (or only pre-existing failures, if any).

- [ ] **Step 7: Commit**

```bash
cd ../optimizer-light
git add pkg/core/allocation.go pkg/core/allocation_test.go
git commit -m "feat: guard zero perfParms in CreateAllocation; fix div-by-zero in zeroLoadAllocation"
```

---

### Task 2: Test + error return in `Solver.Solve()` for unallocated servers

**Files:**
- Create: `../optimizer-light/pkg/solver/solver_test.go`
- Modify: `../optimizer-light/pkg/solver/solver.go` (after diff computation, before `return nil`)

Currently `Solve()` always returns `nil`. When a server ends up with no allocation (e.g., empty `allAllocations` due to zero perfParms, or capacity exhaustion), it is silently omitted from the solution. The fix: check after solving and return an error listing the unresolved servers.

- [ ] **Step 1: Write the failing test**

Create `../optimizer-light/pkg/solver/solver_test.go`:

```go
package solver_test

import (
	"testing"

	"github.com/llm-inferno/optimizer-light/pkg/config"
	"github.com/llm-inferno/optimizer-light/pkg/core"
	"github.com/llm-inferno/optimizer-light/pkg/solver"
)

// newSolverTestSystem builds a system identical to Task 1's newTestSystem,
// but exposed here since solver_test is an external test package.
func newSolverTestSystem(perfParms config.PerfParms, arrivalRate float32) {
	sys := core.NewSystem()
	core.TheSystem = sys

	sys.SetAcceleratorsFromSpec(&config.AcceleratorData{
		Spec: []config.AcceleratorSpec{
			{Name: "H100", Type: "H100", Multiplicity: 1, Cost: 75},
		},
	})
	sys.SetCapacityFromSpec(&config.CapacityData{
		Count: []config.AcceleratorCount{{Type: "H100", Count: 8}},
	})
	sys.SetModelsFromSpec(&config.ModelData{
		PerfData: []config.ModelAcceleratorPerfData{
			{Name: "m1", Acc: "H100", AccCount: 1, MaxBatchSize: 16, AtTokens: 512,
				PerfParms: perfParms},
		},
	})
	sys.SetServiceClassesFromSpec(&config.ServiceClassData{
		Spec: []config.ServiceClassSpec{
			{Name: "Premium", Priority: 1, ModelTargets: []config.ModelTarget{
				{Model: "m1", SLO_ITL: 100, SLO_TTFT: 2000},
			}},
		},
	})
	sys.SetServersFromSpec(&config.ServerData{
		Spec: []config.ServerSpec{
			{Name: "s1", Class: "Premium", Model: "m1",
				MinNumReplicas: 1,
				CurrentAlloc: config.AllocationData{
					Load: config.ServerLoadSpec{
						ArrivalRate:  arrivalRate,
						AvgInTokens:  512,
						AvgOutTokens: 512,
					},
				},
			},
		},
	})
	sys.Calculate()
}

// When a server has no valid allocations (zero perfParms + non-zero load),
// Solve() must return a non-nil error naming the unresolved server.
func TestSolve_ZeroPerfParms_ReturnsError(t *testing.T) {
	newSolverTestSystem(config.PerfParms{Alpha: 0, Beta: 0, Gamma: 0}, 60)
	s := solver.NewSolver(&config.OptimizerSpec{Unlimited: true})
	err := s.Solve()
	if err == nil {
		t.Error("expected error when server has no feasible allocation, got nil")
	}
}

// When a server has valid perfParms and load, Solve() must return nil (success).
func TestSolve_ValidPerfParms_ReturnsNil(t *testing.T) {
	newSolverTestSystem(config.PerfParms{Alpha: 1.5, Beta: 0.002, Gamma: 0.0001}, 60)
	s := solver.NewSolver(&config.OptimizerSpec{Unlimited: true})
	err := s.Solve()
	if err != nil {
		t.Errorf("expected nil error for valid perfParms, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd ../optimizer-light && go test ./pkg/solver/... -run TestSolve -v
```

Expected: `TestSolve_ZeroPerfParms_ReturnsError` FAILS (Solve returns nil today), `TestSolve_ValidPerfParms_ReturnsNil` PASSES.

- [ ] **Step 3: Add error return to `Solver.Solve()`**

In `../optimizer-light/pkg/solver/solver.go`, add `"fmt"` and `"sort"` to imports (they may already be present; add only what's missing):

```go
import (
	"bytes"
	"fmt"
	"sort"

	"github.com/llm-inferno/optimizer-light/pkg/config"
	"github.com/llm-inferno/optimizer-light/pkg/core"
)
```

Replace the final `return nil` in `Solve()` (currently at line 58) with:

```go
	s.diffAllocation = make(map[string]*core.AllocationDiff)
	for serverName, server := range core.GetServers() {
		curAlloc := s.currentAllocation[serverName]
		desiredAlloc := server.Allocation()
		if allocDiff := core.CreateAllocationDiff(curAlloc, desiredAlloc); allocDiff != nil {
			s.diffAllocation[serverName] = allocDiff
		}
	}

	// return error if any server could not be allocated
	var unresolved []string
	for serverName, server := range core.GetServers() {
		if server.Allocation() == nil {
			unresolved = append(unresolved, serverName)
		}
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return fmt.Errorf("no feasible allocation for servers: %v", unresolved)
	}
	return nil
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd ../optimizer-light && go test ./pkg/solver/... -run TestSolve -v
```

Expected:
```
--- PASS: TestSolve_ZeroPerfParms_ReturnsError
--- PASS: TestSolve_ValidPerfParms_ReturnsNil
```

- [ ] **Step 5: Run full test suite**

```bash
cd ../optimizer-light && go test ./...
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
cd ../optimizer-light
git add pkg/solver/solver.go pkg/solver/solver_test.go
git commit -m "feat: return error from Solve() when any server has no feasible allocation"
```

---

### Task 3: Wire up go.mod — local replace directives for development

**Files:**
- Modify: `../optimizer/go.mod`
- Modify: `go.mod` (control-loop, this repo)

Before tagging a new optimizer-light release, wire both consumers to the local path for development/testing.

- [ ] **Step 1: Add replace directive to `optimizer` repo**

In `../optimizer/go.mod`, add below the `require` block:

```
replace github.com/llm-inferno/optimizer-light => ../optimizer-light
```

Then tidy:
```bash
cd ../optimizer && go mod tidy
```

Expected: no errors; `go.sum` updated.

- [ ] **Step 2: Add replace directive to `control-loop`**

In `go.mod` (this repo), add below the `require` block:

```
replace github.com/llm-inferno/optimizer-light => ../optimizer-light
```

Then tidy:
```bash
go mod tidy
```

Expected: no errors.

- [ ] **Step 3: Build both consumers to verify compilation**

```bash
cd ../optimizer && go build ./...
cd - && go build ./...
```

Expected: both build cleanly.

- [ ] **Step 4: Commit go.mod changes**

```bash
# In optimizer repo
cd ../optimizer
git add go.mod go.sum
git commit -m "chore: use local optimizer-light for development"

# In control-loop repo
cd -
git add go.mod go.sum
git commit -m "chore: use local optimizer-light for development"
```

---

### Task 4: Tag new optimizer-light version and update go.mod to tagged version

Once the changes are validated end-to-end (see "Manual Validation" below), replace the local replace directives with a proper tagged version.

- [ ] **Step 1: Tag `optimizer-light`**

```bash
cd ../optimizer-light
git tag v0.7.4
git push origin v0.7.4
```

- [ ] **Step 2: Update `optimizer` go.mod to tagged version**

```bash
cd ../optimizer
# Remove the replace directive added in Task 3
# Edit go.mod: delete the "replace github.com/llm-inferno/optimizer-light => ../optimizer-light" line
go get github.com/llm-inferno/optimizer-light@v0.7.4
go mod tidy
git add go.mod go.sum
git commit -m "chore: bump optimizer-light to v0.7.4"
```

- [ ] **Step 3: Update `control-loop` go.mod to tagged version**

```bash
# Remove the replace directive added in Task 3
# Edit go.mod: delete the "replace github.com/llm-inferno/optimizer-light => ../optimizer-light" line
go get github.com/llm-inferno/optimizer-light@v0.7.4
go mod tidy
git add go.mod go.sum
git commit -m "chore: bump optimizer-light to v0.7.4"
```

---

## Manual Validation

After Task 2, run the optimizer-light demo to confirm real data still solves correctly:

```bash
cd ../optimizer-light
go run demos/main/main.go large
```

Expected: solution printed with allocations for all servers (no error), same as before.

To validate the guard fires: temporarily zero out a model's perfParms in `sample-data/large/model-data.json` and re-run. Expected: error printed for the affected server(s).

After deploying to kind (Task 3/4), delete `perfParms` from `inferno-data/model-data.json` and run `scripts/kind-deploy-blis.sh`. With `TUNER_INIT_HOLD_BACK=true`, the controller skips optimize+actuate during warm-up. After warm-up, the tuner merges real params and the optimizer proceeds normally. If warm-up fails for any reason, the controller will now get a clean error from the optimizer instead of a silent bad allocation.
