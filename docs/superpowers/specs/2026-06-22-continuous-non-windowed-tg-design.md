# Continuous (Non-Windowed) Traffic Generation + Trailing-Window Aggregation

**Date:** 2026-06-22
**Status:** Design / exploration — pending implementation plan
**Scope:** `server-sim` `vllm-server` evaluator (internal traffic model) and, minimally, the server-sim loop. The `control-loop` repo is **untouched**.
**Relationship to prior work:** Supersedes two decisions of [`2026-06-18-continuous-traffic-generator-design.md`](2026-06-18-continuous-traffic-generator-design.md) for the `vllm-server` backend — the **window model** (discrete back-to-back → continuous non-windowed) and **temporal coupling** (causal gating → accept blending). It is exactly the "continuous *aggregation* model (sliding window)" that the 2026-06-18 design **explicitly deferred** ("Window model: Discrete back-to-back windows … the continuous aggregation model is explicitly not chosen").

## Problem

The 2026-06-18 work decoupled the *Collector's polling* from traffic generation (background loop + `GET /latest`), which was the big win. But it kept the underlying traffic model **windowed**: for `vllm-server`, every iteration still runs a self-contained `runWindow` — a time-based **warmup prefix** (excluded from stats), a Poisson burst against vLLM bounded by a deadline, a **drain** (`wg.Wait()`), then aggregation of that one window's samples. Windows run back-to-back, but each is cold.

Consequences that remain:

- **Sawtooth load on vLLM.** Warmup-ramp → measure → drain → reconfigure → warmup-ramp again. vLLM partially drains between windows; you pay a warmup tax every cycle. This is *not* the steady-state continuous load a real deployment exhibits.
- **M\* changes are handled by abandon-and-restart.** `loop.go:watchAllocation` polls the labels file every 1s and **cancels the in-flight window** when `maxbatchsize` changes (`abandoning in-flight window`), precisely because the fixed-size channel semaphore (`sem := make(chan struct{}, N)`) cannot be resized live.
- **`/solve` blocks for the whole window.** server-sim's ticker (`SERVERSIM_TICK_SECONDS`, default 5s) fires, but `runOnce` blocks ~30–90s inside `/solve`; the real cadence is `window + ≤one tick`. The tick is just minimum window spacing, not a sampling rate.
- **A hard window-fits-in-period invariant.** `warmup + window ≤ controlPeriod − slack` had to be enforced at config load.

## Goal

Run the homegrown generator as a **persistent arrival loop that never stops** firing requests at the paired vLLM. `/solve` stops meaning "run a fresh bounded window" and becomes "**swap to these parameters and keep running; report what was observed over the last `n` seconds**." The measurement is a **trailing window of fixed width `n`**, not delimited by traffic or server changes.

## Key decisions (from brainstorming)

| Fork | Decision | Rationale |
|---|---|---|
| Aggregation model | **Trailing window of fixed width `n`**, not delimited by changes (option **(a)** "report as-is") | Simplest metric semantics; the blend across a change is accepted deliberately (see *Blending is fidelity*). |
| Concurrency (`M*`) | **Changes live, per control cycle** | Matches the optimizer's per-cycle M\* search. Forces a **resizable** limiter (atomic `inflight` vs `limit`), replacing the fixed channel semaphore. (a) does not remove this — it's an independent axis. |
| Reconfiguration latency | **Near-real-time is acceptable** | Realistic. Bounded by kubelet downward-API refresh + server-sim tick; not instant, and the same floor applies to both writers. |
| Rollout | **Add a new evaluator binary, then replace after one validation A/B** | Reuses the deploy-time backend-selection pattern; zero control-loop / server-sim changes to coexist; clean windowed-vs-continuous A/B (run16/run17 methodology); delete the windowed binary once validated. |

## Design

### The single-input-channel insight

From a TG's point of view there is **exactly one input channel: its own pod's labels.** Two independent writers populate it, on independent clocks:

- **Load Emulator** writes `inferno.server.load.{rpm,intokens,outtokens}` per pod on its ~20s clock (traffic changes).
- **Actuator** writes `inferno.server.allocation.maxbatchsize` (= M\*) per pod at the end of each control cycle (server changes; this is the per-pod PATCH from #50).

The TG does **not** distinguish "traffic change" from "server change" — it reconfigures whenever any of its label inputs move. This is what keeps the design decoupled: server-sim keeps reading the pod's labels each tick and pushing them via `/solve`; the evaluator swaps its live config. No new wiring.

**`numReplicas` reaches a TG only indirectly, via the Emulator.** The Actuator scaling 2→3 does not touch any TG's RPS. The **Load Emulator** notices the newly-Ready pod and re-splits the per-pod `rpm` labels (÷3 instead of ÷2). So replica count manifests at a TG purely as a changed per-pod RPS, authored by the Emulator. A per-pod TG never needs to know the replica count — it only ever cares about *its own* RPS share, token sizes, and M\*.

### The persistent loop (replaces `runWindow`)

Five mechanical changes, all inside the `vllm-server` evaluator process:

1. **One long-lived goroutine** started at evaluator startup, looping forever, reading its parameters from an **atomically-swappable config holder** (instead of a per-call `windowParams`). The loop is owned by the *process*, not by any `/solve` call — so a cancelled/served `/solve` never affects the traffic.
2. **`/solve` becomes "reconfigure + report":** atomically swap `{RPS, token samplers, concurrency-limit}` from the incoming `ProblemData`, scrape `/metrics`, return the trailing aggregate. It no longer blocks for a window → collect latency decouples entirely from window width.
3. **Resizable concurrency limiter:** replace `sem := make(chan struct{}, N)` (immutable) with an atomic `inflight` counter compared to an atomic `limit` (drop-if-full, exactly as today). M\* changes apply live; **no abandon-and-restart.**
4. **Ring buffer of timestamped completed samples** instead of a per-window slice; aggregate TTFT/ITL/throughput over samples completed in the last `n` seconds on demand. `throughput = completed_in_window / n`.
5. **`/metrics` deltas between consecutive `/solve` calls** (snapshot now, delta vs. the previous snapshot) instead of per-window start/end bookends — this is literally a Prometheus average over the inter-scrape interval. **Warmup collapses** to a single one-time loop start; the server is always warm afterward, so the per-cycle warmup tax disappears.

### server-sim loop simplification

- The `SERVERSIM_TICK_SECONDS` ticker finally becomes the *actual* reconfigure-and-sample cadence: every tick pushes the latest labels via a fast `/solve` and pulls the latest trailing slice into `/latest`.
- **`watchAllocation` abandon-and-restart is removed** — M\* is conveyed in the `ProblemData` payload of each `/solve` and applied live by the resizable limiter. (It can remain as a harmless no-op during the coexistence phase, then be deleted.)
- The **window-fits-in-period invariant dissolves**: `n` (trailing width) is independent of the control period. One fewer hard config constraint.

### Blending is fidelity, not a defect

Choosing a fixed trailing window not delimited by changes means a window straddling a reconfiguration **blends** pre- and post-change behavior. This **mimics a real environment** and is the strongest argument *for* this design:

- In production, traffic drifts on its own, the controller changes knobs on its own cadence with async take-effect, and observability (Prometheus) integrates over a window aligned to *neither*. A `rate(...[Ns])` / histogram query spanning a scale event genuinely blends — that is what monitoring *is*.
- The vLLM `/metrics` half (queue/inference time) **is** Prometheus counters; deltas between two scrapes `n` seconds apart **are** a Prometheus average over that window. The client-side ring buffer is the local equivalent for TTFT/ITL. Both halves reproduce Prometheus semantics.
- By contrast, **today's windowed model is the *less* realistic one** — warmup prefix + drain + inter-window quiescence inject idle periods a continuously-loaded server never sees, and artificially align each measurement to exactly one config.
- This is also a step *toward* the eventual real-Prometheus collection path (already a TODO): an evaluator that behaves like Prometheus makes that later swap smaller.

**Honest caveat — model identification during transients.** Blending is faithful for *observation*, but a straddling trailing window does not correspond to a single `(load, M*)` operating point, which can momentarily bias the tuner/EKF perf-param fit in the cycle right after a change. It is transient (one cycle), the EKF already tolerates non-stationary data, the tuner is often off in these runs, and a real system has the identical problem. Known wrinkle, not a blocker.

### Consequence: causal gating is traded away (reversal of 2026-06-18)

The 2026-06-18 design built **causal gating** to guarantee `decision → observation → decision`: the generator edge-detected M\* changes and abandoned straddling windows, and the **Collector coherence check** skipped any pod whose `effectiveInput.concurrency` ≠ the in-force `maxbatchsize` (rejecting a stale full window from the prior allocation).

Under continuous-(a), the live config is reconfigured to the new M\* as soon as the label lands, so `effectiveInput` matches the label and the Collector check **passes** — even though the trailing window still blends old- and new-M\* requests. That guard, designed for the windowed model, **largely becomes a no-op.** We knowingly trade it for the blended transient, consistent with "blending is fidelity."

**Fallback if it bites.** If empirically the EKF destabilizes on transients, option **(c) generation tagging** restores coherence: stamp each issued request with the config generation it ran under; the trailing aggregate counts only current-generation samples and reports once it has ≥ `MinSamples`. This re-grants the coherence check its meaning, at the cost of generation bookkeeping. Not chosen now; recorded as the escape hatch.

### Rollout: add now, replace after one A/B

The windowed-vs-continuous choice touches **only the `vllm-server` evaluator** — `queue-analysis` and `blis` are stateless analytical solvers with no traffic loop and no transient to blend; they already fit "call `/solve` every tick" unchanged.

1. **Add** `continuous-vllm-server-evaluator` as a sibling binary. Backends are selected at deploy time by which binary runs + `EVALUATOR_URL`, so this coexists with **zero** server-sim / control-loop changes. The persistent loop lives in the evaluator; `/solve` is a lightweight reconfigure+read.
2. **Validate** with one A/B run — back-to-back-windows arm vs. continuous-trailing arm on the same real-vLLM workload (run16/run17 methodology). This is the payoff for keeping both briefly: confirm "blending mimics reality" empirically.
3. **Replace** — delete the windowed `vllm-server-evaluator` once validated. No lasting reason to run two traffic models in the live loop.

**Residual value of the windowed path (for the record, not a reason to keep it in the loop):** offline **characterization** wants clean, isolated single-operating-point measurements (warmup + drain + one config per window) to fit perf params at a known `(load, M*, tokens)` grid — the opposite of what control wants. If that ever matters it belongs in a standalone benchmark tool, not the live control path.

## Benefits

- True steady-state continuous load on vLLM — eliminates the sawtooth and the per-cycle warmup tax.
- `/solve` returns fast → control period fully decoupled from window width; the window-fits-in-period invariant disappears.
- server-sim loop simplifies: `watchAllocation` abandon-and-restart removed; the tick becomes the real cadence.
- More faithful observation semantics (Prometheus-like trailing window); a step toward real-Prometheus collection.
- M\* applied live via a resizable limiter instead of abandon-and-restart.

## Risks / open implementation details

- **Resizable limiter correctness.** Atomic `inflight`/`limit` with drop-if-full must be race-free under the persistent loop + many request goroutines.
- **Ring buffer bounds.** Size/retention of the trailing sample buffer (cap by time `≥ n` and by count) to bound memory under high RPS.
- **Cold/ramping pod.** A freshly-loaded pod (post scale-up, before the Emulator assigns its share, or before `n` seconds of samples accrue) has too few samples — return the existing insufficient-samples signal so the Collector skips it (same as today). Decide: return stale-but-labeled vs. 404-equivalent during ramp.
- **`effectiveInput` vs. trailing blend.** Under (a), report the *current live* config as `effectiveInput` while the stats reflect a blend — a minor, accepted inconsistency. Document it.
- **`n` selection.** Choose the trailing width (and whether it's per-backend config). Must be ≥ enough to gather `MinSamples` at the lowest expected RPS.
- **Validation A/B harness.** Two arms differ by evaluator binary only; confirm the deploy scripts/manifests can select per-arm.
- **Generation-tagging escape hatch.** Keep the option (c) design ready in case transient EKF bias proves real.
