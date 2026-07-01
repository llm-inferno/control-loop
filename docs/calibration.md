# Benchmarking-on-the-fly calibration

> Calibration is one layer of the tuner's operation. For how it fits into the broader tuner
> lifecycle (warm-up phases, EKF vs SWE backends, and backend switching) see
> [`model-tuner-usage.md`](model-tuner-usage.md).

## Why

The model tuner identifies three performance parameters — `α, β, γ` in
`iterationTime = α + β·computed_tokens + γ·transferred_tokens` — from only **two** observations
(`TTFT`, `ITL`) at **one operating point per (model, accelerator) per control cycle**. At a single
operating point the system is unidentifiable: `β` and `γ` trade off freely. The tuner's existing
guards (condition-number guard, init-fit threshold, EKF excursion, seed-anchored `GuessInitState`)
are compensatory — they keep a degenerate fit from doing harm, but they cannot manufacture the
operating-point diversity that identification requires.

Calibration injects that diversity deliberately. Like speaker room-correction playing a known
chirp rather than waiting for ambient sound to span every frequency, it sweeps a handful of load
points (arrival rate × input/output token mix × concurrency), measures `TTFT`/`ITL` at each, and
fits `α, β, γ` jointly over the batch — the **persistent-excitation** condition from system
identification. The sweep produces an identifiable seed; the EKF/sliding-window estimator then does
what it is good at, tracking slow drift from that seed.

## Mechanism vs trigger

The two are decoupled:

- **Mechanism** — a reusable sweep-and-fit path. The Collector drives the sweep (`GET /sweep`); the
  tuner fits the batch (`POST /calibrate`). The fit reuses the tuner's existing `InitEstimator`
  multi-point Nelder-Mead — `AddObservation` ×N then `Fit()` minimises summed squared error across
  all swept points — and the same condition-number guard.
- **Trigger** — a thin policy. Calibration fires only when the tuner reports a pair
  `NeedsCalibration` (`GET /calibration-status`): the pair collected its init observations, the
  resulting fit was **ill-conditioned** (condition number > `TUNER_MAX_CONDITION_NUMBER` — natural
  excitation was insufficient), and the pair has **not been calibrated yet**. If live load
  fluctuated enough during warm-up to make the fit well-conditioned, no sweep is paid for.

Calibration state is **in-memory**: it resets on tuner restart, so a pair is re-calibrated at its
next cold start after a restart. (Persisting it — so a pair calibrates once ever — is deferred; see
the calibration plan.)

## Flow (one control cycle, calibration enabled)

```
controller.Optimize():
  GET  /collect                      (collector: current state from labels/sim)
  POST /tune        → tuner          (collects this cycle's single operating point)
  GET  /calibration-status → tuner   (per-pair: storePresent, conditionNumber, needsCalibration)
    for each pair with needsCalibration:
      GET  /sweep?server=<name> → collector
             ├ find a running pod backing the server
             ├ build grid from nominal load labels (sub-nominal rate ramp + 2 token-ratio points
             │   at the lowest rate, so points stay unsaturated and separate beta/gamma)
             ├ for each point: POST /simulate + poll /simulate/:id (via pods/proxy)
             └ drop saturated / no-latency points; return []ServerSpec
      POST /calibrate (those points) → tuner
             ├ buildEnvironments + fresh InitEstimator + Fit (joint, identifiable)
             ├ reject if still ill-conditioned (sweep grid lacked spread)
             ├ ParameterStore.Set (graduated: UpdateCount = warmUpCycles)
             └ seed the per-pair estimators from the sweep (tracks drift, no re-warm-up)
  GET  /warmup      → tuner           (now clear for the calibrated pair)
  POST /merge       → tuner           (injects calibrated α/β/γ into optimizer model data)
  POST /optimizeOne → optimizer
  POST /update      → actuator
```

The sweep runs **inline** (the control mutex is held). For the blis simulator this is fast —
`/simulate` is an on-demand, stateless solve per point, no GPU and no settle window. A real-vLLM
backend would need async orchestration with settle windows and an isolated probe replica; that is
out of scope here.

## Load emulator interaction

The Load Emulator does **not** need to be stopped during calibration on the simulator backends.
The sweep drives `POST /simulate` with explicit per-point `ProblemData` (rate, tokens, concurrency
baked into each request) — it does not read the pod's load labels. The emulator writes labels and
the Collector's on-demand `/latest` reads them, but neither shares state with an in-flight
`/simulate` job (non-continuous mode has no background arrival loop). So calibration runs on the
side with zero impact on the served workload. Two invariants keep it clean: the sweep runs inside
`Optimize()` with the control mutex held (so its results never leak into that cycle's `/collect`,
and the in-force `maxbatchsize` — the sweep's concurrency — cannot change mid-sweep), and each
point carries its own parameters (so emulator jitter is irrelevant to the measurement).

This is **not** true for a real continuous-vllm-server backend, where `/solve` mutates the shared
live arrival loop: a sweep there would fight the emulator's setpoint and contaminate live SLO
traffic. That backend requires an isolated probe replica (out of rotation) or paused load — the
deferred async path.

## Endpoints added

| Service | Endpoint | Purpose |
|---|---|---|
| tuner | `POST /calibrate` | Body `[]ServerSpec` (sweep points). Joint fit per `(model, accelerator)`; stores graduated params; returns `ModelData`. `422` if no group could be calibrated (e.g. grid lacked spread). |
| tuner | `GET /calibration-status` | `{"statuses": [...]}` — per pair the trigger facts: `storePresent`, `calibrated`, `obsCount`, `obsTarget`, `conditionNumber`, `illConditioned`, `needsCalibration`. |
| collector | `GET /sweep?server=<name>` | Runs the load sweep against a backing pod, returns the measured points as `[]ServerSpec`. |

## Why blis is a valid (non-circular) test

The blis evaluator computes latency from its **trained-physics** `betaCoeffs`/`alphaCoeffs`, while
the tuner fits the **queue-analysis** `α/β/γ`. They are different models, so recovering good
parameters from a deliberately-wrong seed is a genuine test of the fit, not a tautology — and it
costs no GPU. See `scripts/blis/kind-deploy-qwen-calib.sh` and `inferno-data/blis/model-data-calib.json`.

## Verifying on the blis stand-in

```bash
scripts/blis/kind-deploy-qwen-calib.sh   # tuner ON, calibration ON, deliberately-wrong seed
```

Expect, over the first several cycles:

1. Collection cycles run the optimizer on the wrong static seed (`HOLD_BACK=false`) — over-scaled
   allocation in the cycle log.
2. Once init observations are collected, the single-operating-point fit is ill-conditioned →
   `GET /calibration-status` shows `needsCalibration=true`.
3. The controller logs a sweep + calibration; the tuner logs `calibrated parameters
   (benchmarking-on-the-fly)`.
4. `GET /getparams?model=qwen_2_5_14b&accelerator=H100` returns `α≈12, β≈0.011, γ≈1.5e-4` (the
   queue-analysis params that reproduce the blis backend — these differ from the run16 real-vLLM
   fit `β≈0.042`, which is expected, see
   [`experiments/calibration-blis/report-2026-06-30-calibration.md`](../experiments/calibration-blis/report-2026-06-30-calibration.md))
   and the allocation corrects itself.

Negative control: widen the load-phase ramp so natural excitation during collection spans operating
points → the fit is well-conditioned → no calibration is triggered.
