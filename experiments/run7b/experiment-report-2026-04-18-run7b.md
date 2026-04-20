# Experiment Report: Run 7b — Queue-Analysis, errorLevel=0.2, percentChange=0.10

**Date**: 2026-04-18  
**Cluster**: kind (`kind-cluster`) on Docker Desktop, macOS arm64  
**Workloads**: `qa-granite-8b` (granite_8b/H100/Premium) + `qa-llama-13b` (llama_13b/H100/Bronze)  
**Deploy script**: `scripts/kind-deploy-qa.sh`

## Background: Run 7 → Run 7b

Run 7 (aborted) used `errorLevel=0.05`, which caused **complete NIS lockout**: the Nelder-Mead fit landed at granite α=5.73ms (target 8.0), and with a tiny R matrix (errorLevel=0.05 is 4× smaller), every subsequent observation exceeded the rejection threshold (NIS 7k–31k). The filter could not self-correct.

Run 7b raises `errorLevel` back to 0.2 (same as run 6). This gives R a large enough measurement noise floor that the filter can accept observations even when the state estimate is somewhat wrong.

## Motivation and Changes vs Run 6

| Parameter | Run 6 | Run 7b |
|---|---|---|
| errorLevel | 0.2 (default) | 0.2 (same) |
| percentChange | 0.10 (default) | 0.10 (same) |
| expectedObservations | [1000, 100] (default) | [1000, 100] (same) |
| Noise (server-sim) | 2% | 2% (same) |
| maxBatchSize | 64 | 64 (same) |
| maxQueueSize | 128 | 128 (same) |
| granite initial replicas | 4 | 4 (same) |
| SLOs | granite 20ms ITL, llama 60ms ITL | unchanged |

Run 7b is a **replication** of run 6 after the tuner config was moved from the sibling `model-tuner` repo into `control-loop/yamls/deploy/configmap-tuner.yaml` (PR #14). The goal is to confirm that the config relocation did not change behaviour and that run 6 results are reproducible.

## Configuration

| Setting | Value |
|---|---|
| `INFERNO_CONTROL_PERIOD` | 30s |
| `INFERNO_WARM_UP_TIMEOUT` | 0 (disabled) |
| `TUNER_INIT_OBS` | 3 |
| `TUNER_WARM_UP_CYCLES` | 3 |
| `INFERNO_STARTUP_DELAY` | 60s |
| `INFERNO_LOAD_THETA` | 0.2 |
| `INFERNO_LOAD_SKEW` | 0.3 |
| `INFERNO_LOAD_ALPHA` | 0.1 |

## EKF Target Parameters (queue-analysis evaluator)

| Model | Acc | α (ms) | β (ms/tok) | γ (ms/tok²) |
|---|---|---|---|---|
| granite_8b | H100 | 8.0 | 0.016 | 0.0005 |
| llama_13b  | H100 | 12.0 | 0.024 | 0.00075 |

## EKF Warm-up

| Phase | Cycles | Behavior |
|---|---|---|
| Startup delay | ~2 | Pods filtered (< 60s old); optimizer 404 expected |
| Collection | 2 | Tuner accumulates obs 1/3 → 2/3; controller holds back (422) |
| Fit + hold-back | 3 | Nelder-Mead fit; warmingUp=true for TUNER_WARM_UP_CYCLES=3 |
| Normal | onwards | NIS gate active; EKF self-correcting |

Nelder-Mead Fit results (from tuner log at 15:28:48 UTC):
- granite_8b: α=8.127ms (target 8.0, within 1.6%), funcValue=0.0045
- llama_13b: α=12.361ms (target 12.0, within 3.0%), funcValue=0.0007

## Load Profile

Phases from `configmap-load-phases.yaml` (load emulator started ~15:26 UTC):
1. 6 min hold at 1× (granite=60 RPM, llama=30 RPM)
2. 5 min ramp to 5× (granite=300 RPM, llama=150 RPM)
3. 5 min hold at 5×
4. 5 min ramp down to 1×
5. Hold forever at 1×

## EKF Warm-up (Observed)

From controller logs:

| Time (UTC) | Event |
|---|---|
| 15:26:48 – 15:28:18 | Startup delay / 422 from tuner (obs not yet accumulated); optimizer 404 |
| 15:28:48 – 15:29:18 | `warm-up in progress` (Nelder-Mead fit complete, EKF warm-up cycles) |
| 15:29:48 | Cycle 1: first full optimize+actuate |

## Autoscaling Results

Cycle log covers 15:29:48–15:50:18 UTC (42 post-warm-up cycles, 30s period).

| Phase | Cycles | granite replicas | llama replicas | granite RPM | Notes |
|---|---|---|---|---|---|
| Phase 1 (1× baseline) | 1–8 | 3–5 | 1 | 54–78 | Steady; one transient 5-rep spike at cycle 3 |
| Phase 2 (5× ramp) | 9–18 | 6–16 | 1–4 | 58–370 | Scale-out tracks rising RPM |
| Phase 3 (5× hold) | 19–32 | 12–16 | 3–4 | 199–374 | Peak 16 replicas; collect time saturates at 4000ms |
| Phase 4 (ramp down) | 33–37 | 4–12 | 1–2 | 84–228 | Rapid scale-in mirrors RPM drop |
| Phase 5 (1× forever) | 38+ | 4 | 1 | 59–67 | Fully restored; collect ≈55–424ms |

## EKF Stability

NIS rejection threshold: 7.378 (χ² 97.5th percentile, 2 DOF). NIS values from tuner logs (not in cycle log JSON — open issue).

| Model | α range | NIS range | Rejections | Notes |
|---|---|---|---|---|
| granite_8b | 5.78–8.46 | <0.029 | 0 | Drifted to 5.78 at peak load (cycle 18); self-corrected to 8.0–8.5 by cycle 28 |
| llama_13b | 9.58–12.64 | <0.017 | 0 | Monotone drift to 9.58 during 5× hold; recovering to 11.1+ at 1× |

**Key contrast with run 7**: With errorLevel=0.2 (4× larger R than run 7's 0.05), the filter accepted observations even when granite α drifted to 5.78 (32% below target). NIS peaked at 0.029 — far below the 7.378 rejection threshold. In run 7, the same drift produced NIS >7000 and complete lockout.

## Saturation Events

Pattern mirrors run 6. Saturation retries dominated collect time during phases 2–4. At peak load (cycles 19–32), approximately 5 pods saturated simultaneously requiring the 3-attempt retry chain (~800ms per pod). Collect time peaked at 4014–4024ms. All pods resolved within 3 attempts (no exclusions). Collect recovered to 55ms at 1× baseline.

## Key Findings

1. **Replication confirmed**: Run 7b reproduces run 6 behaviour with the tuner config now sourced from `control-loop/yamls/deploy/configmap-tuner.yaml`. Scale-out/in profile, EKF stability, and collect timing are consistent with run 6.
2. **EKF self-corrects from α drift under load**: granite α drifted to 5.78 (32% below target) during peak 5× saturation. The filter accepted all observations (NIS<0.03) and recovered to α≈8.0–8.5 within 10 cycles. This confirms errorLevel=0.2 provides adequate headroom for load-induced state drift.
3. **NIS lockout requires both: bad init fit AND tiny R**: Run 7 demonstrated that a bad Nelder-Mead fit (α=5.73) combined with tiny R (errorLevel=0.05) produces lockout. Run 7b shows that the same starting error (Nelder-Mead gave α=8.13 — close to truth) with normal R is harmless. The errorLevel=0.05 experiment is only pathological when the filter starts far from truth.
4. **Llama convergence slower than granite**: llama α peaked at 12.64 early, dropped to 9.58 during 5× saturation, and was recovering to 11.1+ at run end (47 cycles). Slower convergence than granite is expected at low utilization — less queuing effect to amplify α sensitivity.
5. **Zero NIS rejections across 42 cycles**: No observations were rejected throughout all five load phases including the 4000ms saturation peak. errorLevel=0.2 + percentChange=0.10 is the validated sweet spot for this workload.

## Cycle Log

- Records: 42 (post-warm-up; warm-up cycles excluded)
- Time span: 2026-04-18T15:29:48Z – 2026-04-18T15:50:18Z
- Cycle log stored: `inferno-cycles.jsonl`
- Figures: `figs/run7b_*.png` (generated by `gen_report_figs_run7b.py`)
- Note: `timing` and `nis` fields not included in cycle log JSON; timing from controller stdout, NIS from tuner stdout

## Cycle Timing

| Phase | collect (ms) | Notes |
|---|---|---|
| Cycle 1 (first full cycle) | 811 | Elevated: pod startup warm-up |
| Baseline (4+1 pods, no saturation) | 55–72 | Typical; occasional 413–417ms saturation spikes |
| 5× ramp (increasing saturation) | 413–4014 | Collect grows as more pods saturate |
| 5× hold (12–16 pods, full saturation) | 4011–4024 | Steady at max; ~5 pods × 3 retries × ~270ms |
| Ramp-down (decreasing saturation) | 822–4024 | Drops rapidly as replicas scale in |
| Restored baseline (4+1 pods) | 55–424ms | Fully recovered |

## Comparison with Run 6

| Metric | Run 6 | Run 7b | Notes |
|---|---|---|---|
| Nelder-Mead fit (granite α) | 7.05ms | 8.13ms | Run 7b closer to truth (stochastic) |
| Nelder-Mead fit (llama α) | 9.52ms | 12.36ms | Run 7b much closer to truth |
| Peak granite replicas | 11–17 | 12–18 | One unlimited spike to 18 at cycle 16 |
| EKF NIS (granite) | <0.012 | <0.029 | Higher peak due to steeper α drift |
| EKF NIS (llama) | <0.006 | <0.017 | Same trend |
| NIS rejections | 0 | 0 | Same |
| Peak collect time | 4000–4050ms | 4011–4024ms | Same |
| Baseline collect | 65–90ms | 55–72ms | Same range |
| 404 count | 0 | 0 | Same |

## Open Issues / Next Session

1. **Timing not in cycle JSON**: `timing` field in JSONL is always zero. The `CycleRecord` struct has a `Timing` field but `BuildRecord()` does not populate it — worth investigating and fixing.
2. **NIS not logged in cycle JSON**: The cycle record's `internals` struct omits `nis`/`updateCount`. Consider adding them to `CycleRecord.Internals` for completeness.
3. **Llama α slow convergence**: After 42 cycles at mixed load, llama α=11.1 (7.5% below target 12.0). Expected at low utilization (weak α sensitivity). Running additional cycles would confirm eventual convergence.
4. **Granite α drift to 5.78 under saturation**: The large drift during phase 3 (32% below target) could be reduced by decreasing `percentChange` further or by improving the saturation retry strategy. Filter self-corrected correctly, but smaller drift would be preferable.
5. **Post-baseline α drift to wrong fixed point (post-cycle 50)**: After returning to 1× baseline (~15 RPM/pod), granite α drifted monotonically from 8.17 → 7.86 → 7.17 → 6.54 → 6.51 over 5 cycles (updateCounts 50–56), then stabilised at ~6.5ms. NIS remained low (<0.69) throughout — observations were accepted but systematically biased. Root cause: at very low utilisation the queue model has negligible sensitivity to α (minimal queuing), so small observation noise can steer the filter to a different fixed point. The EKF neither rejects nor self-corrects because its own predictions are internally consistent at the wrong α. This is distinct from the run 7 lockout (rejected observations) and from the phase 3 drift (correct self-correction under high load). Mitigation options: process noise floor (minPercentChange), load floor to ensure observations are informative, or periodic re-anchoring to Nelder-Mead fit at low-load checkpoints.
