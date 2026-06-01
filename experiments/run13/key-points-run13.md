# Run 13 — Key Points

A short companion to `experiment-report-2026-05-31-run13.md`. Run 13 validates
the backend-aware saturation handling on `fix/collector-saturation-real-backend`
and surfaces the next set of issues to investigate.

## What worked

- **Saturation pass-through (issue #21).** Cycle 6 reported
  `Saturation = "overloaded"` with TTFT 423.8 ms; the new code path (`pkg/collector/handlers.go`)
  passed the reading through instead of dropping the cycle. Run 12 dropped
  40 % of cycles on this same condition; run 13 dropped 0.

- **First scale-out in project history.** Cycle 10 at 22:40:29 — observed
  RPM 105.6, optimizer recommended `replicas = 2`, actuator scaled both the
  managed and paired vLLM Deployments. The trigger was the deployment-level
  Prometheus rate plus prior EKF state — c10 itself hit the 30 s collector
  timeout (issue #19), so `curAlloc` was zeroed and the scale-out fired on
  load alone.

- **EKF tracked the saturated regime.** β rose from 1.99 → 2.81 across
  c1 → c18 as the tuner finally received observations during overload.
  γ doubled (8.5e-6 → 1.7e-5).

## What didn't work

- **Optimizer slow to react to in-band TTFT spikes.** Cycles 7, 9, 17, 18,
  19 had TTFT of 3036 / 2192 / 1571 / 2469 / 1185 ms (all ≥ 2× SLO) with
  replicas held at 1. The optimizer's per-replica `maxRPM` derived from
  α/β/γ is still ~2× the real saturation point — same root cause flagged
  in `key-points-run12.md`. The pass-through fix removed the silence; it
  did not fix the model-vs-reality gap.

- **Replica oscillation under second-replica failure.** c11=2, c12=1,
  c13=2. The second managed pod paired to a vLLM pod that couldn't schedule
  on the single-node kind cluster (insufficient CPU/memory), so its
  `/simulate` returned 503 permanently. Each cycle either weighted the
  broken replica into the aggregate (depressing apparent latency) or didn't
  (raising it), causing the optimizer to flip-flop. This is an environmental
  artifact of single-node kind, but the oscillation pattern itself is a
  controller behaviour worth noting.

- **EKF contamination by saturated samples.** β shifted from 1.99 (clean
  baseline) to 2.81 (post-saturation phase 3). γ doubled. Whether this is
  the EKF correctly absorbing real new information about CPU vLLM under
  load, or contamination by a regime where the queueing model's assumptions
  break down, is an open question.

## Candidate next steps (priority order)

1. **`maxRPM` calibration.** The biggest remaining gap. The optimizer's
   per-replica capacity prediction from α/β/γ is ~2× too high vs the real
   CPU vLLM. Options: (a) adjust the queueing model in `optimizer-light`;
   (b) introduce a service-class saturation safety factor; (c) feed the
   optimizer an empirical `maxRPM` derived from observed saturation events.

2. **EKF saturation gating.** Down-weight or skip observations marked
   `Saturation != ""` in the tuner's α/β/γ fit. The optimizer still gets
   the saturated reading via `curAlloc`; the EKF stays clean.

3. **Issue #19 — per-pod `/simulate` timeout.** Default 30 s blocked cycle
   10. Make configurable; align with the evaluator's measurement window.

4. **Evaluator throughput window bias** (separate memory note). Throughput
   under-counted by 15–40 % at low load, more under saturation. Three
   options documented in `project_evaluator_throughput_bias.md`.

## Pointers

- Full report: `experiment-report-2026-05-31-run13.md`
- Code change: `pkg/collector/handlers.go` (backend-aware gate around the
  re-simulation loop, lines ~192–234)
- Issue: #21
- Branch: `fix/collector-saturation-real-backend`
