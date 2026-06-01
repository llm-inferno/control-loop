# Experiment Report: Run 13 — Backend-Aware Saturation Pass-Through, vllm-server with Real CPU vLLM

**Date**: 2026-05-31
**Cluster**: kind (`kind-cluster`) on Docker Desktop, macOS arm64
**Workload**: `vllm-qwen` (qwen_0_5b / cpu / Bronze) paired with a CPU-only vLLM Deployment
**Branch**: `fix/collector-saturation-real-backend` (issue #21)
**Deploy script**: `scripts/kind-deploy-vllm.sh`

## Overview

Follow-up to run 12. This run validates two changes to address the central
issue raised in `experiments/run12/key-points-run12.md`:

1. **Backend-aware saturation handling** (`pkg/collector/handlers.go`). When the
   evaluator backend is `vllm-server`, the Collector no longer re-simulates a
   saturated pod at lower load — that second `/simulate` call would fail on a
   real vLLM. Instead the saturated reading is passed through unchanged into
   both the deployment-level aggregate and `ReplicaSpecs`, propagating the
   overload signal to both the tuner and the optimizer. For `queue-analysis` /
   `blis` / unset, the existing 3-attempt re-simulation is preserved.
2. **Sustained 60 RPM hold**. Phase 3 of `configmap-load-phases-vllm.yaml`
   changed from `ratio: 1.0` (which produced a decaying 60 → 30 RPM ramp via
   linear interpolation between phase endpoints) to `ratio: 2.0`, so phase 3
   is now a true 20-minute hold at 60 RPM.
3. **Load noise reverted to 0.1** (`INFERNO_LOAD_ALPHA`), matching runs 6–11.

The run produced **the first scale-out event in the project's history** (cycle
10, replicas 1 → 2 at observed RPM = 105.6) and validated that saturated
cycles now propagate cleanly instead of being dropped. Cycles 6–9 — which
under run-12's policy would have been dropped with `curAlloc = 0` — flowed
through with real RPM/TTFT/ITL readings and provided the Tuner with
observations during the saturated regime. Two issues remain: (a) the
optimizer was slow to react to in-band saturation (cycles with TTFT 0.9 –
3.0 s but ρ-as-modeled below the scale trigger), and (b) the EKF absorbed
saturated observations in a way that materially shifted β (1.99 → 2.68).

## Configuration

| Setting | Value | vs run 12 |
|---|---|---|
| `INFERNO_CONTROL_PERIOD` | 150s | unchanged |
| `INFERNO_WARM_UP_TIMEOUT` | 0 | unchanged |
| `INFERNO_LOAD_INTERVAL` | 120s | unchanged |
| `INFERNO_LOAD_ALPHA` | **0.1** | reverted from 0.2 |
| `INFERNO_LOAD_THETA` | 0.9 | unchanged |
| `INFERNO_LOAD_SKEW` | 0.0 | unchanged |
| `TUNER_INIT_OBS` | 3 | unchanged |
| `TUNER_WARM_UP_CYCLES` | 3 | unchanged |
| Service class SLO | ITL ≤ 100 ms, TTFT ≤ 500 ms (Bronze) | unchanged |
| `cpu` accelerator capacity | 2 | unchanged |
| Saturation policy on vllm-server | **pass-through** | re-simulate (broken) |
| Phase 3 ratio | **2.0** (sustained 60 RPM hold) | 1.0 (60→30 ramp) |

## Cycle Timeline

| Cycle | Time (UTC) | RPM | Throughput | TTFT (ms) | ITL (ms) | Replicas | α | β | γ |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 22:17:49 | 27.2 | 27.2 | 208.1 | 73.8 | 1 | 62.5 | 1.99 | 8.54e-06 |
| 2 | 22:20:19 | 30.2 | 18.0 | 234.7 | 67.8 | 1 | 62.2 | 1.85 | 8.67e-06 |
| 3 | 22:22:49 | 29.3 | 29.3 | 237.2 | 80.7 | 1 | 63.3 | 1.78 | 8.69e-06 |
| 4 | 22:25:19 | 27.9 | 27.9 | 246.4 | 74.5 | 1 | 63.4 | 1.73 | 8.80e-06 |
| 5 | 22:27:49 | 32.3 | 32.3 | 257.7 | 80.9 | 1 | 63.7 | 1.73 | 8.98e-06 |
| **6** | **22:30:19** | **67.9** | **54.0** | **423.8** | 84.9 | 1 | 62.9 | 1.85 | 9.22e-06 |
| 7 | 22:32:50 | 69.6 | 54.0 | **3036.3** | 92.7 | 1 | 64.1 | 1.72 | 9.85e-06 |
| 8 | 22:35:19 | 77.2 | 77.2 | 881.5 | 90.8 | 1 | 62.1 | 1.85 | 1.08e-05 |
| 9 | 22:37:49 | 101.8 | 90.0 | 2192.4 | 95.6 | 1 | 61.7 | 1.90 | 1.11e-05 |
| **10** | **22:40:29** | **105.6** | 0.0 | 0.0 | 0.0 | **2** | 61.7 | 1.90 | 1.11e-05 |
| 11 | 23:05:54 | 99.1 | 48.0 | 747.2 | 89.1 | 2 | 58.4 | 2.19 | 1.13e-05 |
| 12 | 23:08:24 | 107.4 | 53.7 | 209.6 | 81.3 | **1** | 58.6 | 2.18 | 1.20e-05 |
| 13 | 23:10:54 | 109.4 | 102.0 | 542.6 | 100.1 | **2** | 54.1 | 2.57 | 1.28e-05 |
| 14 | 23:13:24 | 58.1 | 29.1 | 310.9 | 85.8 | 1 | 53.0 | 2.68 | 1.33e-05 |
| 15 | 23:15:54 | 60.1 | 54.0 | 282.1 | 87.0 | 1 | 51.5 | 2.81 | 1.40e-05 |
| 16 | 23:18:24 | 51.3 | 51.3 | 317.7 | 90.3 | 1 | 51.0 | 2.81 | 1.45e-05 |
| 17 | 23:54:40 | 54.6 | 54.6 | 1571.4 | 95.2 | 1 | 51.6 | 2.80 | 1.51e-05 |
| 18 | 23:57:10 | 59.8 | 59.8 | 2469.1 | 96.2 | 1 | 51.6 | 2.80 | 1.60e-05 |
| 19 | 23:59:41 | 64.6 | 64.6 | 1185.4 | 106.7 | 1 | 55.2 | 2.68 | 1.67e-05 |

Bold entries: c6 (first saturation pass-through), c10 (first scale-out),
c12/c13 (replica oscillation under permanent second-replica failure).

## What worked

- **Saturation pass-through.** Cycle 6 (TTFT 423.8 ms, evaluator reported
  `saturation = "overloaded"`) flowed through cleanly with the new code path
  (`pod ...: saturated (overloaded); vllm-server backend, passing through`).
  Under run 12's policy the cycle would have been dropped with `curAlloc = 0`.
  Cycles 7–9 likewise produced real RPM/TTFT/ITL readings (TTFT spiked to
  3.0 s on c7), giving the Tuner observations during the saturated regime
  it had been blind to in run 12.

- **First scale-out event.** Cycle 10 at 22:40:29 — observed RPM 105.6 from
  Prometheus, optimizer recommended `replicas = 2`, Actuator scaled both the
  managed Deployment and the paired vLLM Deployment. Cycle 10 itself
  experienced a 30 s collector timeout polling per-pod `/simulate` (the
  `context deadline exceeded` failure mode tracked in issue #19), so
  `curAlloc.itl/ttft = 0` for that record — the optimizer scaled on the
  Prometheus arrival rate plus prior EKF α/β/γ, not on a real saturated
  observation.

- **EKF responsiveness.** β shifted from 1.99 (c1, lightly loaded) through
  1.72–1.90 (steady) to 2.57–2.81 (c13–c18), tracking the saturated regime.
  γ rose monotonically from 8.5e-6 to 1.7e-5. The EKF is no longer "trapped"
  by the run-12 policy that hid saturation from it.

## What didn't work / open issues

- **Optimizer slow to react to in-band saturation.** Cycles 7, 9, 17, 18, 19
  all crossed the 500 ms TTFT SLO (3036, 2192, 1571, 2469, 1185 ms
  respectively) while replicas remained at 1. The optimizer's per-replica
  `maxRPM` derived from the EKF α/β/γ remains ~2× the real saturation point
  (the same finding from run 12 — see `key-points-run12.md`). The pass-through
  fix removes the silence; it does not fix the model-vs-reality gap.

- **Replica oscillation.** Cycles 11–13: 2 → 1 → 2 across three consecutive
  cycles. The second managed pod (`-lxp5s`) was paired to a vLLM pod that
  could not schedule on the single-node kind cluster (`Insufficient cpu /
  memory`), so its `/simulate` returned 503 permanently. The optimizer
  oscillated as the aggregated curAlloc fluctuated between "1 healthy
  replica" and "2 replicas where one is broken". Not directly caused by the
  fix, but exposed by it.

- **EKF parameter contamination.** β increased from 1.99 (pre-saturation) to
  2.81 (post-saturation cycles in phase 3), and γ doubled. These parameters
  are now influenced by saturated observations that, while not degenerate,
  do reflect a regime where the underlying queueing model assumptions
  (M/M/c-style) may not hold. Whether this contamination is benign or
  problematic for steady-state operation needs another run after the load
  drops to assess recovery.

- **Throughput under-counting.** A separate measurement-methodology issue
  was identified during the run and noted in memory: the evaluator's
  `/simulate` returns throughput counted over a 13–16 s window that includes
  in-flight requests at the window edge as missing completions. At 30 RPM
  with ~3 s end-to-end latency, this systematically under-counts throughput
  by ~15–40 %. Becomes more pronounced under saturation as latency grows.
  Tracked separately for future work.

## Comparison with run 12

| Metric | Run 12 | Run 13 |
|---|---|---|
| Cycles dropped on saturation | 8 of 20 (40 %) | 0 (pass-through) |
| Peak observed RPM | 74.8 | 109.4 |
| First scale-out event | never | c10 (22:40:29) |
| Final replica count | 1 | 1 (after phase 4 descale) |
| EKF β range | ~1.85 (stable) | 1.72 – 2.81 (tracking) |

## Forcing function notes

The phase 3 fix (`ratio: 1.0 → 2.0`) was essential. Without it, the load
profile returns to run 12's decaying ramp and the saturation regime is too
brief to exercise the controller's response.

## Next investigations

1. **`maxRPM` calibration.** The optimizer's per-replica capacity prediction
   from EKF α/β/γ is ~2× the real value for CPU vLLM. Decide whether to
   adjust the queueing model, add a service-class-level saturation safety
   factor, or feed the optimizer an empirical `maxRPM` from observed
   saturation events.

2. **Issue #19 — per-pod `/simulate` timeout.** Cycle 10 hit the 30 s
   default. Make this configurable (already filed) and tune to the
   evaluator's expected window length.

3. **EKF saturation gating.** Consider whether the tuner should down-weight
   or skip observations marked `Saturation != ""` when fitting α/β/γ — the
   contamination pattern in c11–c19 suggests the EKF may converge faster
   and more accurately if it ignores saturated samples (while the optimizer
   still uses them).

4. **Throughput-window correction in evaluator.** Lengthen the
   `/simulate` window, subtract last-dispatch tail from the denominator, or
   replace the evaluator's count with a Prometheus query.

## Pointers

- Cycle log: `experiments/run13/inferno-cycles.jsonl` (19 cycles)
- Container logs: `experiments/run13/logs/inferno-{controller,collector,optimizer,actuator,tuner}.log`
- Code change: `pkg/collector/handlers.go` — backend-aware gate around the re-simulation block
- Companion notes: `experiments/run13/key-points-run13.md`
