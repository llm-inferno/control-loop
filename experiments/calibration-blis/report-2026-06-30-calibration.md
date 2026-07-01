# Experiment Report: Benchmarking-on-the-fly calibration — blis stand-in validation

**Date**: 2026-06-30
**Cluster**: kind (`kind-cluster`) on Podman Desktop, macOS arm64
**Workload**: `blis-qwen` (`qwen_2_5_14b` / H100 / Bronze), single managed Deployment
**Evaluator backend**: blis `trained-physics` (server-sim), on-demand `GET /latest` + on-demand `/simulate` for sweeps
**Deploy script**: `scripts/blis/kind-deploy-qwen-calib.sh`
**Feature**: see [`docs/calibration.md`](../../docs/calibration.md)

## Goal

Validate **benchmarking-on-the-fly calibration** end-to-end on a GPU-free stand-in: the controller
should detect when the tuner cannot identify a model's performance parameters from natural load
(an ill-conditioned fit), drive a short deliberate load sweep, fit `α/β/γ` jointly, and feed the
result back so the optimizer becomes feasible — and it should **not** fire when natural load already
identifies the parameters. The tuner runs with a **deliberately-wrong seed**
(`model-data-calib.json`: `α=20, β=0.08, γ=3e-4`) so the pre-calibration optimizer is infeasible and
the before/after is visible.

This is a non-circular test: blis computes latency from its trained-physics `betaCoeffs`, while the
tuner fits the queue-analysis `α/β/γ` — different models.

## Result: both trigger arms behave as designed

| Arm | Load profile | Fit condition number κ | `needsCalibration` | Outcome |
|---|---|---|---|---|
| **Negative** | natural jitter (`SKEW=0.05`, `ALPHA=0.1`) + 5× ramp | **31.8** | `false` | No calibration. Tuner converged on natural excitation; optimizer feasible by cycle 5 (`optimize: 3ms actuate: 49ms`). |
| **Positive** | flat (`SKEW=0`, `ALPHA=0`, no phases), tuner reset | **1.24e18** | `true` | Calibration fired at cycle 3. |

### Positive-case sequence (flat load)

```
cycle 1 (20:41): tune obs 1/3; optimize 404 (wrong seed infeasible)
cycle 2 (20:43): tune obs 2/3; optimize 404
cycle 3 (20:45): tune obs 3/3 → fit ill-conditioned (κ=1.24e18)
                 → calibration: qwen_2_5_14b/H100 — sweeping server blis-qwen
                 → sweep 3 usable points → POST /calibrate → fit κ=8.85 (well-conditioned)
                 → optimize: 2ms  actuate: 57ms   ← 404 RESOLVED, 2 replicas @ M*=48
cycle 4 (20:47): collect 261  tune 51  optimize 1  actuate 31  total 346ms   (steady, no re-trigger)
```

Post-calibration `GET /calibration-status`: `storePresent=true, calibrated=true,
conditionNumber=8.85, illConditioned=false, needsCalibration=false` — idempotent.

### Unit-level corroboration

`model-tuner/pkg/service/calibrate_test.go`: a 7-point synthetic sweep recovers `α/β/γ` essentially
exactly (κ≈147); three identical operating points are rejected as ill-conditioned (κ≈9.75e15).

## Calibrated parameters

Two sweeps were run. The first used a grid centered at/above nominal
(`0.5…3.0×`); on this high-nominal workload (qwen nominal=250 rpm, already near the single-replica
knee) the `≥1.5×` points saturated and were dropped, leaving only **3** usable points. The grid was
then **skewed below nominal** (`INFERNO_CALIB_RPM_FACTORS=0.25,0.5,0.75,1.0`, with the two
token-ratio points pinned to the lowest rate at a 4× swing) and the run repeated:

| Param | Wrong seed | 3-point (saturated grid) | **5-point (skewed grid)** | run16 *real-vLLM* fit |
|---|---|---|---|---|
| α | 20.0 | 11.62 | **12.02** | 10.65 |
| β | 0.08 | 0.0115 | **0.0112** | 0.0418 |
| γ | 3e-4 | 1.24e-4 | **1.48e-4** | 5.77e-5 |
| κ | (infeasible) | 8.85 | 8.95 | — |
| usable points | — | 3 | 5 | — |

Skewing the grid recovered **5/6** points — the sub-nominal ramp (62.5/125/187.5 rpm) plus **both**
token-ratio points (which now survive at the 62.5-rpm anchor); only the `1.0×`=250 point saturated.

**β/γ differ from the run16 reference — and that is correct, not a deficiency.** run16's values fit
the *real qwen vLLM*; blis uses different trained-physics, so the queue-analysis α/β/γ that reproduce
*blis* legitimately differ. The measured sweep proves it: doubling input tokens (1024→2048) raised
TTFT only ~5 ms (≈0.005 ms/input-token), so a β of 0.042 — which would imply ≈43 ms of prefill at
1024 tokens, more than the entire observed TTFT (~40 ms) — is impossible for this backend; β≈0.011
is the right value. The two independent sweeps landing at the same β≈0.011, γ≈1.3–1.5e-4 region
confirm **stable identification**, not an under-sampling artifact.

So the grid-skew change buys **robustness and confidence** (more surviving points, the token-ratio
points actually present, cross-sweep consistency) rather than a shift toward run16 — the right
takeaway is that calibration recovers the parameters that reproduce *the backend it sweeps*. For an
even more aggressive workload, adaptive knee-finding (cf. `server-sim/scripts/benchmark_curve.py`)
would place the ramp automatically.

## Operational note

The Load Emulator runs untouched during calibration on the simulator backends: the sweep drives
explicit-parameter `/simulate` jobs, independent of the label-driven load, and runs inside the
control mutex so its results never leak into that cycle's `/collect`. A real continuous-vllm-server
backend (where `/solve` mutates a shared live arrival loop) would instead need an isolated probe
replica — deferred.

## Reproduce

```bash
scripts/blis/kind-deploy-qwen-calib.sh          # tuner ON, calibration ON, wrong seed
# Negative case is the default jittered/ramp load; for the positive case, flatten the load
# (SKEW=0, ALPHA=0, no phases) and restart the inferno pod to reset the in-memory tuner.
kubectl logs -f -n inferno deployment/inferno -c controller | grep -i calib
kubectl exec -n inferno deployment/inferno -c controller -- \
  wget -qO- 'http://localhost:3304/getparams?model=qwen_2_5_14b&accelerator=H100'
```
