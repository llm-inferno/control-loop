# Continuous Traffic Generator + Non-Blocking Collect

**Date:** 2026-06-18
**Status:** Design — pending implementation plan
**Scope:** `server-sim` (background loop, `GET /latest`, self-describing results) and `control-loop` (Collector simplification, Actuator pod-label write). Spans two repos.

## Problem

The way traffic is generated and collected today is both convoluted and inaccurate for the real-vLLM (`vllm-server`) backend.

**Current flow:**

1. The **Load Emulator** writes per-pod load labels (`rpm`, `intokens`, `outtokens`) on its own ~20s cadence, independently of control cycles.
2. Each control cycle the **Collector** reads those labels, builds a `simRequest`, and `POST /simulate`s to the server-sim sidecar via the k8s API-server proxy.
3. server-sim is already async (`pkg/job` + `GET /simulate/:id`): it creates a job, spawns a goroutine, and returns a `jobID` — but the **Collector blocks**, polling `/simulate/:id` until the job completes (`pkg/collector/serversim.go:simulatePod`).
4. The goroutine calls the evaluator's `/solve`. For `vllm-server`, `solveHandler` runs `runWindow` — a Poisson traffic burst against the real vLLM for `warmup + window` (90–330s) — then aggregates ITL/TTFT/throughput/saturation.

**Consequences:**

- **vLLM is idle between cycles, then bursts.** Each cycle re-primes a fresh warmup and measures a cold-ish ramp. This is intermittent load synchronized to the control cadence, not steady-state load. This is the core inaccuracy for "run a particular load and collect data."
- **Control period is hostage to window length.** `INFERNO_SIMULATE_TIMEOUT_SEC` must exceed the window; the period is forced to ≥120s.
- **Input/output drift.** The Collector reports the *current* label as the offered load, but the window measured whatever the label was at kick time — they can already diverge, and the saturation-retry path makes the actual load differ from the label by design.

## Goals

1. Traffic generation runs continuously in a background thread, **one job at a time**, not kicked by the Collector and not blocking server-sim.
2. The Collector **reads already-available results** — it never waits for a job to run.
3. **Uniform handling across all evaluators** (`queue-analysis`, `blis`, `vllm-server`) — no per-backend special cases in the Collector.
4. Keep **dynamic concurrency control** (the optimizer's per-cycle `M*`) reaching the generator.
5. Results are **self-describing**: the operating point that produced a measurement travels with the measurement.

## Non-goals

- No change to the optimizer, tuner, or the load-scenario phase logic. The Load Emulator keeps writing load labels exactly as it does today.
- No change to the analytical evaluators' math (`queue-analysis`, `blis` `Solve` implementations are untouched).
- The continuous *aggregation* model (sliding window) is explicitly **not** chosen — see Decisions.

## Design

### Data flow (new)

```
Load Emulator ──writes load labels──▶ pod
Actuator ──────writes M* label────▶ pod   (running pods, each cycle)
                                     │
                                     ▼ (downward-API labels volume, live)
server-sim background loop (one job at a time)
    read effective input ─▶ job.Manager.Create ─▶ evaluator.Solve ─▶ store {input, result, completedAt}
                                     │
Collector ──GET /latest──────────────┘  (non-blocking, branchless)
    aggregate per-pod results ─▶ ServerCollectorInfo
```

### server-sim — continuous mode

- A background **ticker loop**, started for every backend (uniform). One iteration at a time; "one job at a time" falls out of the single-threaded loop, and the evaluator's `vllmMu` already serializes `/solve`.
- Each iteration:
  1. Read the **effective input** from the downward-API labels volume: `rps` (from `rpm`), `avgInTokens`, `avgOutTokens`, and `concurrency` (from the `maxbatchsize` label — see Dynamic concurrency).
  2. `job.Manager.Create()` → call `evalCli.Solve(pd)` (today's `handleSimulate` goroutine body, driven by a ticker instead of an HTTP POST).
  3. **Saturation policy** (moved here from the Collector): if the result is saturated and policy is `retry-at-lower-load`, re-run at `0.95 / 0.90 / 0.85 × MaxRPS` (the existing `overloadTargetUtilization` / `overloadRetryStep` / `overloadMaxRetries` constants) until unsaturated or attempts exhausted. If policy is `pass-through` (vllm-server), publish the saturated reading as-is. Policy is a **per-backend config field**, not a code branch.
  4. Store the completed job carrying the **effective input actually run** (post-retry-adjustment), the `AnalysisData`, and a completion timestamp.
- **Allocation edge-detection (causal gating).** The loop reads the `maxbatchsize` (`M*`) label at each window start. If it changed since the in-flight window started, **abandon that window and start a fresh one** under the new allocation, so a post-decision measurement becomes available within the cycle rather than one cycle later. Only *allocation* changes gate the window — see Temporal coupling.
- A **min tick interval** bounds wasted recompute for instantaneous backends (qa recomputing every tick when nothing changed is trivial CPU, but cap it).
- `GET /simulate/:id` and `POST /simulate` remain for debugging.

### server-sim — `GET /latest`

New handler returning the most-recent **completed** job, as a self-describing envelope:

```jsonc
{
  "effectiveInput": { "rps": ..., "avgInTokens": ..., "avgOutTokens": ..., "concurrency": ... },
  "result":         { /* AnalysisData: ITL, TTFT, throughput, maxRPS, saturation */ },
  "completedAt":    "<RFC3339 timestamp>"
}
```

- `effectiveInput` is what was **actually run**, including any saturation-driven load reduction — never the raw label.
- `AnalysisData` stays a pure measurement type; inputs live in the envelope, not inside it.
- **Cold-start contract:** before the first window completes, `GET /latest` returns `404` ("no result yet"). The Collector treats this exactly like a failed sim today — pod excluded from `ReplicaSpecs`, no contribution to `curAlloc`.
- **TTL:** `job.Manager` evicts completed jobs after `JobTTL`. The "latest completed" must survive between Collector reads — `JobTTL` must comfortably exceed the control period (document and/or enforce).

### Load parameters — downward-API labels volume

- The pod's own `inferno.server.load.*` labels are projected into a mounted file via the downward API. The loop re-reads the file each iteration; the kubelet refreshes it when the Load Emulator changes labels, so updates are picked up live.
- **Env vars are not usable** — they don't update at runtime. A downward-API *volume* does.
- No k8s RBAC for reads, no API round-trip per window.

### Dynamic concurrency (`M*`) — same channel as load params

- The Actuator already computes `M*` and writes it to the **deployment** `maxbatchsize` label each cycle (`pkg/actuator/handlers.go:66`).
- The Actuator **already patches running pod labels** — the pairing reconciler writes `pair-id` onto a running pod via `addPodLabel` (`pkg/actuator/pairing_kube.go:93`), and RBAC already grants `pods … patch, update` (`manifests/common/deploy-loop.yaml:17`).
- **Change:** extend the Actuator to also patch the **running pods'** `maxbatchsize` label each cycle, reusing `addPodLabel`. The generator reads it from the same downward-API volume as the load params and feeds it to `runWindow.Concurrency`.
- Single uniform config channel (pod labels), live updates, no new HTTP push path, no RBAC change.
- **Inherent one-cycle lag:** the generator runs at the *currently-actuated* `M*` (optimizer decides → Actuator writes → generator picks it up on its next window). This is correct semantics and the same lag the system already lives with.

### control-loop — Collector simplification

The Collector's per-pod path collapses to a single **branchless** operation:

- For each running, ready pod owned by the deployment: `GET /latest` via the k8s proxy (non-blocking).
- On `404`/no-result: skip the pod (same as a failed sim today).
- Otherwise: build the replicaSpec from the envelope — `ITLAverage`/`TTFTAverage` from `result`, and `ArrivalRate`/`Throughput`/`AvgInTokens`/`AvgOutTokens` from `effectiveInput` (the load actually run, guaranteeing a coherent `(load, perf)` pair for the EKF).
- **Coherence check (causal gating):** compare the envelope's `effectiveInput.concurrency` against the `M*` the Collector/Actuator put in force at the previous decision. **Match** → the observation ran under the current allocation; feed it to the tuner/optimizer. **Mismatch** → the window did not complete under the current allocation in time; treat the pod's observation as **stale** (hold tuning for it / reuse the prior fit) rather than silently feeding a straddled observation to the EKF. Staleness is logged, not silent.
- Aggregate per-pod into deployment-level `curAlloc` as today (weighted by throughput).

**Removed from the Collector:**

- The `simulatePod` POST + poll loop (`pkg/collector/serversim.go`).
- Building `simRequest` from pod labels (the loop reads labels itself).
- The per-backend saturation re-simulation branch (`pkg/collector/handlers.go:199-235`) — moved into the server-sim loop as policy.
- Passing `maxConcurrency` (the Collector no longer kicks sims).

Deployment-level Prometheus queries (arrival rate, token rates) are a separate concern and stay.

## Temporal coupling: causal gating (not blocking synchrony)

The control process and the evaluation process run on their own clocks (preserving the non-blocking goals), but the system requires a **causal chain**: the observation consumed at cycle N+1 must reflect the allocation actuated at cycle N (`decision → observation → decision`). Free-running independent tickers break this — a window can straddle an actuation boundary or sit in a stale epoch, so the EKF/optimizer fit/decide on an observation that does not correspond to the allocation in force.

The fix is **causal gating, not blocking synchrony**. The controller never waits on the evaluator and the evaluator never waits on the controller; the decision merely *delimits* the evaluation over the channel that already exists (the Actuator's allocation-label write):

1. **Decision write is the trigger.** The Actuator already writes `M*` to the pod labels; the generator reads them via the downward API. The generator acts on the *edge*: when `M*` changes it abandons the in-flight window and starts fresh under the new allocation.
2. **Results are tagged with the allocation they ran under** — `effectiveInput.concurrency`. This *is* the epoch key (implicit flavor); no separate epoch counter.
3. **The Collector confirms the chain** by matching `effectiveInput.concurrency` to the in-force `M*`, and detects staleness on mismatch (see Collector coherence check).

**The subtlety that bites — gate on allocation, never on load.** The Load Emulator perturbs `rpm`/tokens continuously (OU noise every ~20s). If the generator restarted on *any* label change, no window would ever complete. So **only the Actuator's allocation labels gate the window** (`maxbatchsize`; replica-count changes manifest as pod lifecycle / cold-start 404). Load is *expected* to vary within a window — that is the realistic-load model — and the average actually offered is reported via `effectiveInput`. Coherence means "ran under one allocation," not "ran under one frozen operating point."

**Window upper-bound invariant.** For the post-decision window to complete within the cycle, `warmup + window ≤ controlPeriod − (collect + decide + actuate slack)`. This is a **hard config invariant**, validated at config load. The Collector's mismatch check is the safety valve when it is violated, but the loop is only well-behaved when the window fits.

**Rejected alternative — explicit epoch counter.** The Actuator could bump a monotonic epoch label each cycle, which would additionally flag "fresh post-decision" even when the allocation is unchanged. Rejected: an unchanged allocation makes a prior-window observation still valid, so the extra bookkeeping buys no coherence the concurrency match doesn't already give.

## Decisions (from brainstorming)

| Fork | Decision | Rationale |
|---|---|---|
| Result transport | `GET /latest` on the sidecar | No label-as-data, no self-patch, no RBAC. `GET /simulate/:id` still exists. |
| Load param source | Downward-API labels volume | Live updates, no RBAC, no per-window API call. Env vars don't update at runtime. |
| Window model | Discrete back-to-back windows | Smallest change; reuses `runWindow` unchanged. Mild boundary burstiness accepted. |
| Scope | All backends, uniform | qa works (instant recompute), blis benefits (slow sim no longer blocks), vllm-server is the motivation. Saturation handling becomes config, not a code branch. |
| Concurrency channel | Actuator writes `M*` to running pod labels | Actuator already patches pod labels; RBAC already present; same downward-API channel as load params. |
| Input/output coherence | Self-describing result envelope | Required in the decoupled model — a result without its effective input is ambiguous; guarantees EKF coherence; simplifies the Collector to a single source. |
| Temporal coupling | Causal gating, implicit (concurrency-match) | Restores `decision → observation → decision` without blocking. Generator edge-detects allocation changes; Collector matches `effectiveInput.concurrency`. No new label, no epoch counter. |

## Benefits

- vLLM sees continuous back-to-back load instead of one burst per cycle (the core accuracy fix).
- Control period decoupled from window length; `INFERNO_SIMULATE_TIMEOUT_SEC` becomes irrelevant for vllm-server; period can drop well below the window.
- Collector reduced to a branchless `GET /latest` + aggregate.
- Saturation and simulation concerns owned by the component that runs simulations.
- Coherent, unambiguous `(load, perf)` observations for the tuner.
- Restored `decision → observation → decision` causal chain via causal gating, with staleness detectable rather than silent — without reintroducing a blocking trigger.

## Risks / open implementation details

- **Cross-repo change.** server-sim (loop, `/latest`, envelope, saturation policy) and control-loop (Collector, Actuator) move together; the `/latest` envelope schema is the contract between them. Stage the server-sim side first.
- **TTL vs control period.** Must enforce/document `JobTTL` > control period so the latest completed job is never swept before the Collector reads it.
- **Downward-API label projection.** Confirm the exact projected keys and file format; the loop parses `key="value"` lines. The `maxbatchsize` label must be present on the *pod* (Actuator change) for the downward API to project it.
- **Min tick interval.** Choose a sane floor for instantaneous backends so qa doesn't busy-recompute.
- **Window upper-bound invariant.** Enforce `warmup + window ≤ controlPeriod − slack` at config load (see Temporal coupling). Decide the failure mode when violated (reject config vs. warn + rely on the Collector's mismatch check).
- **Allocation vs load label classification.** The generator must restart only on allocation-label (`maxbatchsize`) changes, not on Load-Emulator jitter. The set of "allocation" labels must be explicit and stable; misclassifying a continuously-changing label as an allocation label starves window completion.
- **Manifests.** Add the downward-API volume + mount to the workload pod templates that run server-sim.
```
