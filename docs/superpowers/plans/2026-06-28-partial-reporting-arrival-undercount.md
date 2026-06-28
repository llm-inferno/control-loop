# Partial-Reporting Arrival Under-Count Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the deployment-level offered `ArrivalRate` from under-counting when some pods are coherence-gated, by preferring the offered setpoint label on partial reporting — eliminating the run19 5↔3 peak-hold oscillation.

**Architecture:** Extract the inline `arrivalRateRPM` selection in `pkg/collector/handlers.go` into a pure helper `selectArrivalRate`, change its policy so the offered setpoint label (`inferno.server.load.rpm`) is used whenever reporting is partial (`numReporting < numReplicas`), keep the measured Σ (`totalOfferedRPM`) only when all replicas report, and table-test the helper.

**Tech Stack:** Go, standard `testing` package. Control-loop only — no server-sim change.

## Global Constraints

- Change is confined to `pkg/collector/` (package `collector`). No server-sim change, no server-sim image rebuild.
- The setpoint label key is `ctrl.KeyArrivalRate` (= `inferno.server.load.rpm`), parsed as `float64`, treated as present only when parse succeeds and value `> 0`.
- "All replicas report" means strict equality `numReporting == numReplicas` (strict, not `>=` — a terminating pod during scale-in can make `numReporting > numReplicas`, and that case must fall through to the setpoint label, not the over-counting measured Σ).
- Behavior must be unchanged for: full reporting (→ measured Σ), and zero reporting (→ setpoint label, else Prometheus/static `arvRate`).
- TDD: write the failing test first, watch it fail, then implement.

---

### Task 1: Extract and fix the arrival-rate selection helper

**Files:**
- Modify: `pkg/collector/handlers.go` (replace the `arrivalRateRPM` block at lines ~236-241; add helper `selectArrivalRate`)
- Test: `pkg/collector/arrivalrate_test.go` (create)

**Interfaces:**
- Produces: `func selectArrivalRate(numReporting, numReplicas int, totalOfferedRPM, setpoint, arvRate float64, hasSetpoint bool) float64`
  - Returns `totalOfferedRPM` when `numReporting > 0 && numReporting == numReplicas`.
  - Else returns `setpoint` when `hasSetpoint`.
  - Else returns `totalOfferedRPM` when `numReporting > 0`.
  - Else returns `arvRate`.
- Consumes (in `collect`): existing locals `numReporting`, `numReplicas`, `totalOfferedRPM` (float64), `arvRate` (float64), and `d.Labels[ctrl.KeyArrivalRate]`.

- [ ] **Step 1: Write the failing test**

Create `pkg/collector/arrivalrate_test.go`:

```go
package collector

import "testing"

func TestSelectArrivalRate(t *testing.T) {
	const (
		offered  = 672.0  // partial measured Σ (under-count)
		setpoint = 1250.0 // true deployment offered (load.rpm label)
		prom     = 500.0  // Prometheus/static backup
	)
	tests := []struct {
		name          string
		numReporting  int
		numReplicas   int
		totalOffered  float64
		setpoint      float64
		arvRate       float64
		hasSetpoint   bool
		want          float64
	}{
		{"full reporting uses measured sum", 5, 5, offered, setpoint, prom, true, offered},
		{"partial reporting prefers setpoint label", 3, 5, offered, setpoint, prom, true, setpoint},
		{"zero reporting prefers setpoint label", 0, 5, 0, setpoint, prom, true, setpoint},
		{"partial reporting no label falls back to partial sum", 3, 5, offered, 0, prom, false, offered},
		{"zero reporting no label falls back to arvRate", 0, 5, 0, 0, prom, false, prom},
		{"scale-in over-report prefers setpoint label", 4, 3, offered, setpoint, prom, true, setpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectArrivalRate(tt.numReporting, tt.numReplicas,
				tt.totalOffered, tt.setpoint, tt.arvRate, tt.hasSetpoint)
			if got != tt.want {
				t.Fatalf("selectArrivalRate = %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/collector/ -run TestSelectArrivalRate -v`
Expected: FAIL — compile error `undefined: selectArrivalRate`.

- [ ] **Step 3: Add the helper to `handlers.go`**

At the end of `pkg/collector/handlers.go`, add:

```go
// selectArrivalRate chooses the deployment-level offered arrival rate (RPM).
//
// When every replica reports a coherent /latest (numReporting == numReplicas),
// the measured Σ-over-pods offered (totalOfferedRPM) is the consistent #55
// same-source pairing with Throughput. When reporting is partial — some pods
// coherence-gated (fresh-pod maxbatchsize label skew) or not yet ready — that
// sum under-counts by the missing pods' offered share and would make the
// optimizer scale down spuriously, so prefer the gating-independent deployment
// offered setpoint label (load.rpm). The setpoint label is also used on zero
// reporting (unchanged). Only when no setpoint label is available do we fall
// back to the partial measured sum (if any pod reported) or the Prometheus /
// static backup arvRate.
func selectArrivalRate(numReporting, numReplicas int, totalOfferedRPM, setpoint, arvRate float64, hasSetpoint bool) float64 {
	switch {
	case numReporting > 0 && numReporting == numReplicas:
		return totalOfferedRPM
	case hasSetpoint:
		return setpoint
	case numReporting > 0:
		return totalOfferedRPM
	default:
		return arvRate
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/collector/ -run TestSelectArrivalRate -v`
Expected: PASS (all 6 subtests).

- [ ] **Step 5: Wire the helper into `collect`**

In `pkg/collector/handlers.go`, replace the existing block (lines ~236-241):

```go
	arrivalRateRPM := arrvRate
	if numReporting > 0 {
		arrivalRateRPM = totalOfferedRPM
	} else if setpoint, perr := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 64); perr == nil && setpoint > 0 {
		arrivalRateRPM = setpoint
	}
```

with:

```go
	// Deployment-level offered arrival rate. Full reporting → measured Σ;
	// partial or zero reporting → offered setpoint label (gating-independent)
	// to avoid the under-count that drives spurious scale-down; see
	// selectArrivalRate and docs/superpowers/specs/2026-06-28-partial-reporting-arrival-undercount-design.md.
	setpoint, perr := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 64)
	hasSetpoint := perr == nil && setpoint > 0
	arrivalRateRPM := selectArrivalRate(numReporting, numReplicas, totalOfferedRPM, setpoint, arvRate, hasSetpoint)
```

Note: the surrounding code uses the variable `arrvRate` (declared near line 57). Confirm the exact spelling in context and pass that variable as the `arvRate` argument — do not introduce a new name.

- [ ] **Step 6: Verify the package builds and all collector tests pass**

Run: `go build ./... && go test ./pkg/collector/ -v`
Expected: build succeeds; `TestSelectArrivalRate`, `TestBuildReplicaSpecCoherent`, and existing serversim tests all PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/collector/handlers.go pkg/collector/arrivalrate_test.go
git commit -m "fix(collector): prefer offered setpoint label on partial reporting

Deployment ArrivalRate summed only over reporting pods under-counted
when fresh pods were coherence-gated, driving a 5<->3 autoscaling
oscillation at sustained peak load. Use the gating-independent
load.rpm offered setpoint whenever numReporting < numReplicas.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Update documentation

**Files:**
- Modify: `CLAUDE.md` (the architecture paragraph describing the offered-load fallback)
- Modify: `docs/operational-notes.md` (add partial-reporting fallback note)

**Interfaces:**
- Consumes: the `selectArrivalRate` behavior from Task 1. No code.

- [ ] **Step 1: Update `CLAUDE.md`**

Find the sentence in the architecture section beginning "When no pod reports a usable `/latest` this cycle (cold start, or all pods coherence-gated), `ArrivalRate` falls back to the Load Emulator's offered setpoint label (`inferno.server.load.rpm`)…". Edit it to state the fallback also fires on **partial** reporting. Replace the lead-in clause with:

> When reporting is partial or empty — some pods coherence-gated (fresh-pod `maxbatchsize` label skew) or none reporting (cold start) — i.e. `numReporting < numReplicas`, `ArrivalRate` falls back to the Load Emulator's offered setpoint label (`inferno.server.load.rpm`), an offered-meaning quantity, instead of the Σ-over-reporting-pods sum (which would under-count by the missing pods' offered share and drive a spurious scale-down). The measured Σ is used only when every replica reports (`numReporting == numReplicas`). The Prometheus completion-rate query is kept only as a last-resort backup when the label is absent.

Keep the existing trailing TODO sentence about `vllm:request_arrival_total`.

- [ ] **Step 2: Add a note to `docs/operational-notes.md`**

Append a subsection:

```markdown
## Partial-reporting arrival fallback (control-loop)

The deployment-level offered `ArrivalRate` is the Σ of per-pod offered over the
*reporting* (coherence-passing) pods. On a scale-out, each fresh replica is
coherence-gated for ~1 cycle (its on-demand `/latest` is solved against the
pod-template `maxbatchsize=128` until the Actuator patches the in-force M*), so
that sum under-counts and the optimizer would scale down spuriously — observed
as a 5↔3 oscillation at sustained peak load. The collector therefore prefers
the offered setpoint label (`inferno.server.load.rpm`) whenever reporting is
partial (`numReporting < numReplicas`), using the measured Σ only when every
replica reports. See `selectArrivalRate` in `pkg/collector/handlers.go`.

Saturation-policy interaction: under `pass-through` the per-pod offered is the
true offered, so the label and the measured Σ agree and the switch is seamless.
Under `retry-at-lower-load` the per-pod offered is the retry-reduced load, below
the true offered, so on a partial-reporting cycle the switch to the full setpoint
label is a one-cycle upward jump in arrival. This is accepted: it only extends
the pre-existing zero-reporting fallback's semantics to the partial case.
```

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md docs/operational-notes.md
git commit -m "docs: deployment ArrivalRate falls back to setpoint label on partial reporting

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage:**
- Bug / current code → Task 1 Step 5 replaces the exact block. ✓
- Option A, universal selection logic + behavior table (4 rows) → Task 1 helper + 6 test cases (4 rows + zero-no-label + scale-in over-report edge). ✓
- Saturation-policy wrinkle (documented, not fixed) → Task 2 operational-notes note. ✓
- Out of scope (label-skew root cause, zero-ITL dashboard, server-sim) → not touched; confirmed control-loop only. ✓
- Testing: pure helper + table tests + cluster acceptance → unit tests in Task 1; cluster rerun is the separate run19 execution (existing `scripts/blis/kind-deploy-qwen.sh`), not a code task. ✓
- Docs: `CLAUDE.md` + `docs/operational-notes.md` → Task 2. ✓

**2. Placeholder scan:** No TBD/TODO-style placeholders in steps; all code shown. The one TODO referenced in CLAUDE.md (`vllm:request_arrival_total`) is pre-existing prose to preserve, not a plan placeholder. ✓

**3. Type consistency:** `selectArrivalRate` signature is identical in the Interfaces block, the test call, the implementation, and the wiring call. Argument order `(numReporting, numReplicas, totalOfferedRPM, setpoint, arvRate, hasSetpoint)` matches everywhere. Note flagged that the surrounding variable is spelled `arrvRate` in `handlers.go` — passed positionally as the `arvRate` parameter. ✓
