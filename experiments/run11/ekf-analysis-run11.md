# EKF Tuner Analysis — Run 11

**Date:** 2026-05-05  
**Model under investigation:** `granite_8b / H100`  
**Estimator:** EKF (Extended Kalman Filter) with NM-based init estimator  
**Run summary:** 5-phase load experiment. Replica counts tracked SLO targets correctly throughout, but fitted EKF parameters diverged from static model-data values.

---

## Issue 1 — Poor Initial NM Fit (Cycle 3, 21:03:15)

### Observation

```
InitEstimator: Fit complete alpha=5.104 beta=0.01606 gamma=0.000697
  observations=3 funcValue=0.0007
```

Static model-data value for alpha is ~8. The NM fit landed at alpha≈5.1.

### Root Cause

`GuessInitState` with `baseFactor=0.9` computed the NM starting point from the first accumulated observation (ITL=15.47ms, TTFT=60.94ms, tokens=1970/1023):

```
alpha₀ = 0.9 × 15.47 = 13.92   (static truth ≈ 8)
gamma₀ = (ITL - alpha₀ - sumBG) / denom = 0.000614
beta₀  = 0.02326
```

The guess itself is valid (all positive), so `GuessInitState` did not return nil. The NM started from `[13.9, 0.023, 0.00061]` and converged to a **different local minimum** at `[5.1, 0.016, 0.0007]` with funcValue≈0.0007 (excellent fit).

The underlying problem is **near-degeneracy**: all 3 initial observations were at very low load (λ≈14–16 RPM), where the queueing component is negligible. The model equations reduce to approximately:

```
TTFT ≈ alpha + (beta+gamma) × inputTokens
ITL  ≈ alpha
```

Multiple `(alpha, beta, gamma)` triplets fit equally well. There is no unique minimum at low loads — the NM converges to whichever basin the starting point falls into.

### Assessment: Lowering `baseFactor` to 0.5

With `baseFactor=0.5`:

```
alpha₀ = 0.5 × 15.47 = 7.74   (closer to truth ≈ 8)
gamma₀ = (15.47 - 7.74 - sumBG) / denom = 0.003107   (5× too high)
beta₀  = 0.02390
```

**Drawbacks:**

1. **gamma₀ blows up 5×** (0.00061 → 0.0031). The `GuessInitState` linear equations are underdetermined: reducing alpha₀ always inflates gamma₀ proportionally. There is no `baseFactor` value that simultaneously improves both.

2. **NM simplex is scaled by the initial guess.** With gamma₀=0.003, the NM explores gamma in ~[0.001, 0.01]. The true value (~0.0006) sits near the lower boundary of this range, making convergence there harder rather than easier.

3. **The degeneracy problem is unchanged.** At λ≈15 RPM the parameter manifold is near-flat regardless of starting point. A different starting guess moves you between equivalent local minima; it does not fix identifiability.

**Net verdict:** Lowering `baseFactor` from 0.9 to 0.5 trades a better alpha starting point for a significantly worse gamma starting point. The improvement is unlikely to be meaningful; it could easily make the fit converge to yet another wrong local minimum.

**Real fix:** Ensure at least one of the `TUNER_INIT_OBS` observations is collected at high load (≥50% of MaxRPS) so all three parameters are separately identifiable. Alternatively, seed the NM starting point from the static `model-data.json` values (the physically known truth) rather than from `GuessInitState`.

---

## Issue 2 — Sudden Alpha Drop at Cycle 17 (21:11:17)

### Observation

```
// Before: alpha≈6.48 (updateCount=16)
// Cycle 17 replicas (6 pods, all λ≈30 RPM, tokens=2375/1175):
replica 0: TTFT=1437ms  ITL=116ms  → NIS=56.9  REJECTED
replica 1: TTFT=205ms   ITL=80ms   → NIS=43.4  REJECTED
replica 2: TTFT=1483ms  ITL=116ms  → NIS=38.6  REJECTED
replica 3: TTFT=1439ms  ITL=120ms  → NIS=7.85  REJECTED  (threshold=7.378)
replica 4: TTFT=16222ms ITL=158ms  → NIS=39.5  REJECTED
replica 5: TTFT=1448ms  ITL=119ms  → NIS=4.607 ACCEPTED

// After: alpha=2.224 beta=0.01390 gamma=0.000557
```

Alpha dropped 6.48 → 2.22 (−66%) from a single accepted update. Beta and gamma shifted to partly compensate.

### Root Cause: Negative Numerical Jacobian at the Saturation Boundary

The measurement function `h([α,β,γ]) = [TTFT_model, ITL_model]` is evaluated via `queue-analysis.Analyze()`. On error (e.g., arrival rate exceeds model throughput), the function returns a **zero vector sentinel** `[0, 0]`:

```go
// pkg/core/functions.go
zero := mat.NewVecDense(2, nil)
...
if err != nil {
    return zero   // ← root cause
}
```

The EKF Jacobian is computed by `NumericalJacobian` using **centered differences** with `delta=0.01`:

```
epsilon = 0.01 × alpha = 0.01 × 6.48 = 0.065 ms
fxFwd   = h(alpha + 0.065)   // alpha=6.545
fxBwd   = h(alpha − 0.065)   // alpha=6.415
J_alpha = (fxFwd − fxBwd) / (2 × 0.065)
```

At λ=30 RPM with x_pred=[6.48,...], the queue model is near/at its saturation boundary. Since **higher alpha → slower service → lower throughput → system more saturated**:

- `h(alpha=6.545)` → `Analyze()` fails → returns `[0, 0]`
- `h(alpha=6.415)` → `Analyze()` succeeds → returns `[TTFT_valid, ITL_valid]`

Therefore:

```
J_alpha = ([0, 0] − [TTFT_valid, ITL_valid]) / (2 × 0.065)  =  NEGATIVE
```

This is physically wrong: `dTTFT/dα` and `dITL/dα` should be positive (larger α → slower service → higher TTFT and ITL). The zero-sentinel return flips the sign.

**Kalman gain consequence:**

```
K = P · Hᵀ · S⁻¹
```

With `H_alpha < 0`, `P_alpha > 0`, `S` positive-definite → `K_alpha < 0`.

The innovation for ITL is large and positive: `y_ITL = 119 − ITL_pred > 0`. The state update:

```
x_alpha_new = x_pred_alpha + K_alpha × y_positive < x_pred_alpha
```

**Alpha is driven downward by a large positive ITL/TTFT observation because the sign of the Kalman gain is wrong.**

The magnitude of the drop (−4.26ms) is consistent with a modest K_alpha (≈−0.004 ms per ms of innovation) acting on a large innovation vector.

### Why the NIS Gate Did Not Prevent This

The NIS threshold (`defaultMaxNIS=7.378`, chi-squared 2-DOF at 97.5%) rejected five replicas but passed replica 5 with NIS=4.607. The innovation covariance S is inflated by the corrupted Jacobian H near the saturation boundary, which changes the S matrix and thus the NIS scaling. Counterintuitively, the corruption that causes the wrong update direction also makes the NIS appear acceptable.

### Remedies

#### Fix A — Return `nil` instead of `zero` on queue model error (targeted, recommended)

In `pkg/core/functions.go`, change all error-path returns from `return zero` to `return nil`:

```go
// Before
if err != nil {
    return zero
}

// After
if err != nil {
    return nil
}
```

`NumericalJacobian` already handles nil:
```go
if fxBwd == nil || fxFwd == nil {
    continue  // zero-out this Jacobian column
}
```

Additionally, the `Update` step checks:
```go
hx := ekf.h(ekf.X)
if hx == nil {
    return errors.New("measurement function returned nil")
}
```

So if `h(x_pred)` itself is infeasible (model fails at current state), `Update` returns an error and `RunWithValidation` skips the replica via the `runErr` path — cleanly excluding observations the model cannot evaluate.

**Effect:** Replicas operating above the model's saturation boundary are silently skipped rather than corrupting the Jacobian with a sign-flipped derivative. This is the correct behavior.

#### Fix B — Bound per-cycle alpha change (defense-in-depth)

In `service.go/tuneGroup`, after selecting the accepted result, sanity-check the relative change:

```go
if existing := ts.paramStore.Get(model, accelerator); existing != nil {
    relChange := math.Abs(float64(accepted.ServiceParms.Alpha-existing.Alpha) / float64(existing.Alpha))
    if relChange > 0.30 {
        slog.Warn("excessive alpha change, skipping EKF update", "model", model, "change", relChange)
        return fmt.Errorf("alpha change %.1f%% exceeds 30%% limit", relChange*100)
    }
}
```

This is a backstop only: Fix A eliminates the root cause; Fix B catches any future saturation-boundary escape that gets through.

---

## Summary Table

| Issue | Root Cause | Recommended Fix |
|---|---|---|
| NM init → wrong alpha (5.1 vs ~8) | Near-degenerate observations at low load; baseFactor=0.9 gives high alpha₀ but changing it trades one inaccuracy for another | Collect ≥1 init obs at high load, or seed NM from static model-data |
| Alpha drop 6.48 → 2.22 | `h()` returns zero sentinel on queue model error; centered-difference Jacobian gets wrong sign at saturation boundary; K_alpha < 0 | Return `nil` (not `zero`) from `functions.go` error paths so Jacobian skips infeasible columns |
