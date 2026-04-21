# Experiment Report: Run 9 — Sliding-Window Nelder-Mead Estimator (fixed)

**Date**: 2026-04-21  
**Cluster**: kind (`kind-cluster`) on Docker Desktop, macOS arm64  
**Workloads**: `qa-granite-8b` (granite_8b/H100/Premium) + `qa-llama-13b` (llama_13b/H100/Bronze)  
**Deploy script**: `scripts/kind-deploy-qa.sh`

## Background: Run 8 → Run 9

Run 8 used the new `TUNER_ESTIMATOR_MODE=sliding-window` feature (PR #9 in model-tuner) and produced catastrophically bad behaviour: parameters oscillated wildly (granite α swung 8→0→20→0→25 across cycles), the optimizer scaled granite down from 2→1 replica at cycle 2, and ITL jumped to 109ms (3.5× the 30ms SLO) and never recovered.

Root-cause analysis identified three bugs in the `SlidingWindowEstimator`:

1. **No warm-start**: `Fit()` restarted Nelder-Mead from `guessInitState(last_obs)` every cycle — a noisy single-observation analytic estimate that changes each cycle — instead of the previous fit result. Nelder-Mead converged to different degenerate local minima each cycle.
2. **Bad first-fit seed**: On the first `Fit()` call, `lastFit` was nil so `guessInitState` was used, which could return a degenerate solution. The warm-start then locked in that bad starting point permanently.
3. **Unnecessary filling wait**: `IsReady()` required `len(window) >= windowSize` (10), forcing 7 extra cycles of filling after the 3-cycle init phase before any estimates were produced. `windowSize` controls buffer capacity, not the minimum observations needed to fit.

All three fixes are on model-tuner PR #11 (`fix/sliding-window-warm-start` branch). Run 9 validates these fixes.

## Changes vs Run 8

| Item | Run 8 | Run 9 |
|---|---|---|
| model-tuner branch | `main` (PR #9 merged) | `fix/sliding-window-warm-start` |
| Warm-start from previous fit | No | Yes (`lastFit` field) |
| First-fit seed from InitEstimator | No | Yes (`SeedLastFit`) |
| `IsReady()` threshold | `windowSize` (10) | `minObs` = `initObs` (3) |
| First estimate available | Cycle 10 | Cycle 1 |
| Workload config | `kind-deploy.sh` (wrong) | `kind-deploy-qa.sh` (correct) |

## Configuration

| Setting | Value |
|---|---|
| `INFERNO_CONTROL_PERIOD` | 30s |
| `INFERNO_WARM_UP_TIMEOUT` | 0 (disabled) |
| `TUNER_ESTIMATOR_MODE` | `sliding-window` |
| `TUNER_INIT_OBS` | 3 |
| `TUNER_WARM_UP_CYCLES` | 3 |
| `TUNER_WINDOW_SIZE` | 10 (default) |
| `TUNER_RESIDUAL_THRESHOLD` | 0.5 (default) |
| `INFERNO_STARTUP_DELAY` | 20s (collector) / 60s (load emulator) |
| `INFERNO_LOAD_THETA` | 0.8 |
| `INFERNO_LOAD_SKEW` | 0.05 |
| `INFERNO_LOAD_ALPHA` | 0.05 |

## Target Parameters (queue-analysis evaluator)

| Model | Acc | α (ms) | β (ms/tok) | γ (ms/tok²) |
|---|---|---|---|---|
| granite_8b | H100 | 8.0 | 0.016 | 0.0005 |
| llama_13b  | H100 | 12.0 | 0.024 | 0.00075 |

## Warm-up

| Phase | Cycles | Behavior |
|---|---|---|
| Collection | 2 | InitEstimator accumulates obs 1/3 → 2/3; controller holds back |
| Init fit + SWE seed | 1 | Nelder-Mead fit on 3 obs; SWE seeded + `lastFit` set; first estimate produced |
| Normal | onwards | SWE warm-starts from previous fit each cycle; window fills to 10 |

InitEstimator Fit results (tuner log at 11:36:03 UTC):
- granite_8b: α=8.445ms (target 8.0, within 5.6%), funcValue=0.000122
- llama_13b: α=12.347ms (target 12.0, within 2.9%), funcValue=0.000335

First SWE estimates (cycle 1, 11:36:04 UTC):
- granite_8b: α=8.445ms, β=0.01617, γ=0.000479
- llama_13b: α=12.347ms, β=0.02453, γ=0.000670

## Load Profile

Phases from `configmap-load-phases.yaml`:
1. 6 min hold at 1× (granite=60 RPM, llama=30 RPM) — entered 11:33:34 UTC
2. 5 min ramp to 5× (granite=300 RPM, llama=150 RPM) — entered 11:39:37 UTC
3. 5 min hold at 5× — entered 11:44:40 UTC
4. 5 min ramp down to 1× — entered 11:49:40 UTC
5. Hold forever at 1× — entered 11:54:43 UTC

## Autoscaling Results

Cycle log covers 11:36:04–11:58:03 UTC (45 post-warm-up cycles, 30s period).

| Phase | Cycles | granite replicas | llama replicas | granite RPM | Notes |
|---|---|---|---|---|---|
| Phase 1 (1× baseline) | 1–9 | 2–3 | 1 | 55–67 | Stable; small transient spikes |
| Phase 2 (5× ramp) | 10–16 | 4–11 | 1–2 | 90–274 | Scale-out tracks rising RPM |
| Phase 3 (5× hold) | 17–29 | 10–13 | 2–3 | 285–315 | Peak 13 replicas |
| Phase 4 (ramp down) | 30–38 | 3–11 | 1–3 | 65–285 | Rapid scale-in mirrors RPM drop |
| Phase 5 (1× forever) | 39+ | 2–3 | 1 | 55–65 | Fully restored |

## Parameter Stability

| Model | α range | β range | γ range | Notes |
|---|---|---|---|---|
| granite_8b | 7.60–8.45 | 0.01570–0.01695 | 0.000479–0.000515 | Initial α=8.44 converges to 8.0 within 5 cycles; slight drift during peak load; fully recovered at baseline |
| llama_13b | 11.13–12.35 | 0.02323–0.03694 | 0.000670–0.000766 | β shows wider variation during peak (ramp-up of queuing pressure); α stable near target |

**Key contrast with run 8**: Parameters converge to target from cycle 1 and remain stable throughout all 45 cycles. No degenerate collapses (α→0, β→0.5, γ→0). The warm-start fix prevents Nelder-Mead from escaping the correct basin.

## Cycle Timing

Timing from controller stdout (45 cycles):

| Phase | collect (ms) | tune (ms) | Notes |
|---|---|---|---|
| Cycle 1 (first) | 816 | 52 | Elevated: pod startup |
| Baseline (2–3 pods, no saturation) | 56–76 | 30–65 | Typical |
| Phase 2 ramp (increasing saturation) | 63–3617 | 57–112 | Grow as pods saturate |
| Phase 3 hold (10–13 pods, full saturation) | 3608–5617 | 65–116 | Peak 5617ms at cycle 19 |
| Phase 4 ramp-down | 1607–4417 | 49–67 | Decreasing |
| Phase 5 restored baseline | 57–76 | 47–60 | Fully recovered |

Notable: tune time is 30–116ms (Nelder-Mead on 3–10 observations), compared to 2–3ms for EKF in run 7b. The sliding-window approach is ~20–40× more expensive per cycle due to running Nelder-Mead each time.

## Key Findings

1. **All three fixes validated**: Parameters converge to target from cycle 1 for both models across all 45 cycles and 5 load phases. No degenerate solutions observed.
2. **First estimate available immediately after init phase**: With `minObs=initObs=3`, the SWE produces its first estimate at cycle 1 (11:36:04 UTC), 7 cycles earlier than run 8 (which waited for `windowSize=10`).
3. **Warm-start ensures cycle-to-cycle continuity**: α moves smoothly from 8.44→8.05 over the first 5 cycles rather than jumping to degenerate values. During peak load α drifts slightly (granite to 7.60, llama β to 0.037) then recovers — same qualitative behaviour as EKF in run 7b.
4. **Tune time is 20–40× higher than EKF**: SWE runs Nelder-Mead (500 evaluations × queue analyzer per observation) each cycle. At window=10 this is ~5000 queue analyzer calls vs. one EKF update step. This is acceptable for a 30s control period but worth noting for tighter periods.
5. **Identifiability caveat (from earlier debugging)**: The sliding-window approach is susceptible to degenerate solutions when observations span a narrow operating range AND the InitEstimator's Fit also fails (seen with `kind-deploy.sh` workload at low RPM). The qa workload provides sufficient operating-point diversity for both models. The EKF remains more robust for ill-conditioned cases due to its implicit prior regularization.
6. **Max replicas**: granite peaked at 13 (vs. 12–18 in run 7b). llama peaked at 3 (vs. 3–4 in run 7b). Scaling behaviour is comparable.

## Comparison with Run 7b (EKF)

| Metric | Run 7b (EKF) | Run 9 (SWE fixed) | Notes |
|---|---|---|---|
| Estimator | EKF | Sliding-window Nelder-Mead | — |
| First estimate cycle | 1 | 1 | Same (both use initObs=3) |
| granite α range | 5.78–8.46 | 7.60–8.45 | SWE tighter range |
| llama α range | 9.58–12.64 | 11.13–12.35 | SWE tighter range |
| Degenerate collapses | 0 | 0 | Same |
| Peak granite replicas | 12–18 | 10–13 | SWE slightly more conservative |
| Peak collect time | 4011–4024ms | 3608–5617ms | SWE wider range (saturation timing) |
| Tune time | 2–3ms | 30–116ms | EKF 20–40× faster |
| NIS rejections | 0 | N/A (no NIS gate) | — |

## Cycle Log

- Records: 45 (post-warm-up; warm-up cycles excluded)
- Time span: 2026-04-21T11:36:04Z – 2026-04-21T11:58:03Z
- Cycle log stored: `inferno-cycles.jsonl`
- Figures: `figs/run9_*.png` (generated by `gen_report_figs_run9.py`)

## Open Issues / Next Steps

1. **Merge PR #11**: Run 9 validates all three fixes. Ready to merge.
2. **Tune time overhead**: 30–116ms/cycle for SWE vs. 2–3ms for EKF. Acceptable at 30s period; profile if period is reduced.
3. **Identifiability at low utilisation**: SWE can still converge to wrong solution if InitEstimator's Fit fails (funcValue >> 1). Could add a funcValue threshold to fall back to EKF when the initial fit is poor.
4. **β drift during peak load (llama)**: llama β rose from 0.024 to 0.037 during phases 2–3, recovering afterward. The sliding window evicts old low-load observations and temporarily fits only high-load ones, biasing β. EKF handles this more smoothly via the covariance. Larger window size (e.g., 20) might reduce this effect.
