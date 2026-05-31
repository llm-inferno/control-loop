# Run 12 — Key Points

A short companion to `experiment-report-2026-05-31-run12.md`. These are the
items that bear directly on the follow-up investigation into the Controller's
saturation re-simulation policy on a real server.

## What worked

- **Pairing reconciler.** The Actuator bound the managed pod ↔ vLLM pod cleanly
  at run start (`pair-id 8cf9daa2-…`) and held the binding for the entire
  65-minute run. The evaluator sidecar resolved its `pair-id` via the downward
  API and forwarded all `/simulate` calls to the right vLLM pod.

- **Cold-start convergence.** With zero initial `perfParms`, the
  InitEstimator → SWE pipeline produced stable parameters within 3 cycles:
  α ≈ 62 ms, β ≈ 1.85 ms/tok, γ ≈ 1.6e-6 ms/tok². The InitEstimator's first
  γ fit (0.094 — orders of magnitude off) was discarded by the very first SWE
  refit and never reappeared.

## The central issue: saturation handling on a real server

- **40 % of cycles dropped.** 8 of 20 logged cycles (3, 6, 9, 10, 11, 13, 14,
  17) ended with the pod excluded from `ReplicaSpecs`. Cause: the Collector's
  saturation re-simulation policy (designed for the queue-analysis simulator)
  re-runs `/simulate` at progressively lower load when the evaluator reports
  overloaded. On a real vLLM, the second `/simulate` returns HTTP 500 — the
  evaluator cannot manufacture a lower-load measurement on demand — so the pod
  is dropped from the cycle entirely.

- **The overload signal is erased, not propagated.** In the dropped cycles,
  `curAlloc.itl/ttft/throughput = 0`, the tuner is skipped silently
  (`tune: 0 ms`), and the optimizer runs against the previous cycle's α/β/γ.
  The very cycles that *should* trigger scale-out are the ones where the
  controller has the *least* information.

## The mechanical reason no autoscaling occurred

- **Optimizer's per-replica `maxRPM` is ~2× too high.** The SWE's α/β/γ fit
  yields `maxRPM` ≈ 84–104. The real vLLM saturates somewhere closer to
  40–50 RPM (judging from cycles 5 and 7, where measured TTFT hit 2135 ms and
  1791 ms vs the 500 ms SLO).

- **The optimizer never sees ρ above ~0.03.** Even at the run's peak of
  74.8 RPM (cycle 11), the optimizer's view is rho = 0.033 — comfortably below
  any scaling trigger. ITL stayed under the 100 ms SLO throughout; only TTFT
  crossed the SLO, and the worst TTFT cycles were either dropped (saturation
  re-sim) or downweighted as outliers by the SWE.

- **Replicas held at 1 throughout.** The optimizer ran in 12 of 20 cycles and
  recommended `numRep=1` every time. Total cost stayed at 1.

## Candidate fixes (for next investigation)

1. **Backend-aware saturation policy.** Skip the re-simulation only when the
   evaluator backend is `vllm-server`; preserve current behaviour for
   `queue-analysis` / `blis`. Introspectable via the deployment label
   `inferno.server.evaluator`.

2. **Pass saturated measurements straight through.** Don't re-simulate.
   Record the evaluator's reported (saturated) ITL/TTFT/throughput as-is
   so the optimizer sees the degraded latency and reacts.

3. **Synthesize a high-rho placeholder.** When the evaluator reports
   overloaded, set `throughput = arrivalRate × 0.5` (or similar) and keep
   the elevated ITL/TTFT, so the optimizer at least computes ρ ≈ 1.0.

The user's note ("needs more investigation") flags option choice as the
design question to settle before the next run.

## Forcing function for the next run

The current `configmap-load-phases-vllm.yaml` phase 3 is a 20-minute
**decaying ramp** from 60 → 30 RPM (the load-emulator interpolates linearly
between phase endpoint ratios), not a sustained hold at 2× nominal. A
one-line change — replace phase 3's `ratio: 1.0` with `ratio: 2.0` — converts
it into a true 20-minute hold at 60 RPM, which (combined with whichever
saturation-policy fix is chosen) should produce the first scale-out event.

## Pointers

- Full numbers, tables, and figures: `experiment-report-2026-05-31-run12.md`
- Saturation logic to revisit: `pkg/collector/collector.go`
  (`overloadMaxRetries`, `overloadTargetUtilization`, `overloadRetryStep`)
- Phase config: `yamls/deploy/configmap-load-phases-vllm.yaml`
