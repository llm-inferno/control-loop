# Partial-Reporting Arrival Under-Count Fix — Design

**Date:** 2026-06-28
**Branch:** `feat/run19-blis-autoscaling` (this fix is a prerequisite for the clean run19 ramp)
**Scope:** control-loop only — `pkg/collector/handlers.go`. No server-sim change, no server-sim image rebuild.

## Problem

At a sustained peak load hold, the run19 blis-qwen autoscaler oscillates **5↔3 replicas** even though offered load is held constant. Root trigger: the deployment-level offered `ArrivalRate` under-counts whenever some pods are coherence-gated.

The deployment offered is computed as **Σ of per-pod offered over the *reporting* pods only** (`totalOfferedRPM`, control-loop #55). On every scale-out, each fresh replica is coherence-gated for ~1 cycle — its on-demand `/latest` is solved against the pod-template's hardcoded `inferno.server.allocation.maxbatchsize: "128"` before the Actuator patches it to the in-force M\*, so `effectiveConcurrency=128 ≠ inForce=N` and the collector drops it from the sum.

Observed mechanism (optimizer log, NO_TUNER run 2026-06-27):

1. At 5 replicas the scale-out's fresh pods are gated → excluded from the Σ → arrival reads **672** while load is held at **1250**.
2. Optimizer provisions for 672 → scales **down to 3**.
3. 3 replicas saturate (TTFT ~42 s).
4. Next cycle all 3 report → arrival reads full **~1352** → scale **up to 5**.
5. Fresh pods gated again → under-count → back down. Repeat.

Confirmed live: optimizer `rate=1409→5, 672→3, 1352→5`. The offered-load under-count from gated pods is the trigger.

## Current code (`pkg/collector/handlers.go:236-241`)

```go
arrivalRateRPM := arrvRate              // Prometheus completion-rate proxy, or load.rpm label if Prom down
if numReporting > 0 {
    arrivalRateRPM = totalOfferedRPM    // Σ offered over REPORTING pods only  ← under-counts
} else if setpoint, perr := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 64); perr == nil && setpoint > 0 {
    arrivalRateRPM = setpoint           // load.rpm offered setpoint label (offered-meaning)
}
```

The setpoint-label fallback only fires when `numReporting == 0`. The moment **one** pod reports, the code switches to `totalOfferedRPM`, which is short by every gated/missing pod's offered share. Both `numReporting` and `numReplicas` (= `*d.Spec.Replicas`) are already in scope, so the partial case is detectable with no new data.

With the tuner OFF (run19), `ArrivalRate` is the *only* input that drives the optimizer's replica decision — throughput feeds the tuner/observability, not provisioning — so correcting arrival directly stabilizes the replica count.

## Decision: Option A, universal

Prefer the offered setpoint label (`inferno.server.load.rpm`, `ctrl.KeyArrivalRate`) whenever reporting is **partial** (`numReporting < numReplicas`), not only when zero pods report. Keep the measured Σ (`totalOfferedRPM`) only when **all** replicas report (`numReporting == numReplicas`).

Rationale:
- The setpoint label is the true, gating-independent deployment offered — exactly the quantity that should drive provisioning. Using it during partial reporting removes the under-count trigger directly.
- It mirrors and extends the existing `numReporting == 0` fallback (same label, same offered meaning), rather than introducing a new mechanism.
- Minimal, low-risk, single-file change. Sufficient on its own to stop the 5↔3 oscillation under NO_TUNER, because provisioning follows arrival and arrival becomes the held setpoint instead of the partial sum.

Applied **universally** (all backends), matching the scope of the existing zero-report fallback. Not scoped to a backend or policy — the collector does not (and should not need to) know the server-sim saturation policy.

### New selection logic

```go
arrivalRateRPM := arrvRate
setpoint, sperr := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 64)
hasSetpoint := sperr == nil && setpoint > 0

switch {
case numReporting > 0 && numReporting == numReplicas:
    // full reporting: measured Σ-over-pods offered (the #55 same-source pairing)
    arrivalRateRPM = totalOfferedRPM
case hasSetpoint:
    // partial reporting (some pods gated/not-yet-ready) OR zero reporting:
    // the measured Σ under-counts by the missing pods' offered share, so use the
    // gating-independent deployment offered setpoint label instead.
    arrivalRateRPM = setpoint
case numReporting > 0:
    // partial reporting but no setpoint label available: fall back to the partial
    // measured Σ rather than the Prometheus completion-rate proxy.
    arrivalRateRPM = totalOfferedRPM
// else: numReporting == 0 and no setpoint label → arrvRate (Prometheus proxy / static label backup)
}
```

Behavior summary:

| Condition | ArrivalRate source |
|---|---|
| `numReporting == numReplicas` (all report) | `totalOfferedRPM` (measured Σ) |
| `0 < numReporting < numReplicas` **and** setpoint label present | `setpoint` (offered label) — **the fix** |
| `numReporting == 0` **and** setpoint label present | `setpoint` (offered label) — unchanged |
| partial/zero reporting **and** no setpoint label | partial `totalOfferedRPM` if any report, else `arrvRate` |

The only behavioral change vs. today is the second row: partial reporting with a setpoint label now uses the label instead of the partial sum. Full-reporting and the existing zero-report paths are unchanged.

## Saturation-policy interaction (known wrinkle, documented not fixed)

The gating *trigger* is policy-independent, but the saturation policy changes whether the full↔partial switch is seamless:

- **`pass-through`** (run19's choice): the saturated result is propagated as-is, so per-pod `effectiveInput.RPS` is the **true offered** load even under saturation. Measured Σ and the setpoint label are both offered quantities and agree (modulo missing pods). The switch is seamless — no discontinuity. For run19 this fix is clean.
- **`retry-at-lower-load`** (default for qa/blis): the server-sim loop retries at `0.95→0.90→0.85×MaxRPS`, so per-pod `effectiveInput.RPS` is the **retry-reduced** load, below the true offered. Measured Σ then sits below the setpoint label under saturation, and swapping to the label on partial reporting is a one-cycle **upward jump** in arrival.

This wrinkle is accepted, not fixed, because:
1. It is **not a new class** of inconsistency — the existing `numReporting == 0` fallback already swaps to the full setpoint label and already diverges from a measured Σ under retry. This change only extends that same swap to the partial case.
2. It bites only during a transient partial-reporting cycle on a saturated retry-policy backend.
3. Scoping the swap to `pass-through` would require the collector to know the server-sim env-var policy — added coupling for a transient edge case.

## Out of scope

- **Root-cause label-skew gating fix** (Actuator patches fresh-pod `maxbatchsize` at readiness, or collector grace for pods younger than ~1 control period). This is the "proper" long-term fix and also removes the one-cycle scale-out relief delay, but it is a larger, separate change touching the Actuator/coherence-gate. Tracked as follow-up; not blocking the clean run19.
- **Zero ITL/TTFT under saturation** (blis pre-check returns zeros → dashboard plots 0 ms). Separate dashboard/record-schema issue, tracked separately.
- Any server-sim change. The saturation policy and `/latest` envelope are untouched.

## Testing

- **Unit:** the collector arrival-selection is currently inline in `handleCollect`. Extract the selection into a small pure helper `selectArrivalRate(numReporting, numReplicas int, totalOfferedRPM, setpoint, arvRate float64, hasSetpoint bool) float64` and table-test the four rows above:
  1. full reporting → `totalOfferedRPM`
  2. partial reporting + setpoint → `setpoint` (the new behavior; assert it is NOT the partial sum)
  3. zero reporting + setpoint → `setpoint` (regression guard, unchanged)
  4. partial reporting, no setpoint → partial `totalOfferedRPM`; zero reporting, no setpoint → `arvRate`
- **Manual / cluster:** rerun run19 (NO_TUNER, pass-through, on-demand `/latest`, capacity cap H100=8, M\* search ON, 120 s period). Acceptance: at the sustained peak hold the replica count holds steady (no 5↔3 oscillation); optimizer log shows arrival tracking the held setpoint (~1250) through scale-out transients instead of dropping to the partial sum.

## Files

- `pkg/collector/handlers.go` — replace the `arrivalRateRPM` selection block; extract `selectArrivalRate` helper.
- `pkg/collector/handlers_test.go` (new or existing) — table tests for `selectArrivalRate`.
- `CLAUDE.md` — update the architecture paragraph describing the deployment offered-load fallback (currently "When no pod reports a usable `/latest` this cycle … `ArrivalRate` falls back to the Load Emulator's offered setpoint label") to state the fallback also fires on **partial** reporting (`numReporting < numReplicas`), with the saturation-policy note.
- `docs/operational-notes.md` — add a short note on the partial-reporting fallback and the retry-at-lower-load discontinuity wrinkle.
