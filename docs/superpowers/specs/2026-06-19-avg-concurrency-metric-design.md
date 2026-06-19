# avgConcurrency (in-service occupancy) metric — design

**Date:** 2026-06-19
**Status:** approved (pending spec review)

## Goal

Log the **average in-service concurrency** (batch occupancy) per managed server, every
control cycle, on both the human-readable Collector log and the structured cycle JSONL.
Today this value is *estimated* during reporting (as it was in run15); the goal is to emit
the real number so it can be plotted and reported directly, alongside the existing perf
metrics (ITL, TTFT, throughput).

"In-service" = average number of requests actually being processed (the running batch),
directly comparable to the per-replica `maxBatchSize` / `M*` ceiling — i.e. "how full is
the batch".

## Key decision: compute uniformly in the Collector via Little's Law

The three evaluator backends expose occupancy inconsistently:

| Evaluator | Native occupancy | Notes |
|---|---|---|
| queue-analysis | `AvgNumInServ` (exact, MM1 state probabilities) | server-sim's qa handler **drops** it when mapping to `evaluator.AnalysisData` |
| blis | raw `NumRunningBatchRequests []int` only | needs `mean()` post-processing |
| vllm-server | **none** | only scrapes queue_time + inference_time histograms |

Rather than thread three different native values (different definitions ⇒ not
comparable across runs) through the `evaluator.AnalysisData` wire contract and three
backends, we compute occupancy **uniformly in the Collector** using **Little's Law**
(exact in steady state):

```
in-service occupancy  L = X · (W_resp − W_wait)
```

where `X` = throughput, `W_resp` = mean response time, `W_wait` = mean queue wait.
All three inputs are **already present** in every `/latest` envelope. This means:

- **Zero server-sim / evaluator changes.** `simResult` (`pkg/collector/serversim.go`)
  already decodes `AvgRespTime` and `AvgWaitTime` ("for wire-contract completeness",
  currently unused).
- **One definition** for all three backends ⇒ qa/blis/vllm curves are directly comparable.
- Native values (qa `AvgNumInServ`, blis `mean(NumRunningBatchRequests)`) are **not logged**
  but remain a free cross-check during cluster validation.

## Data flow

```
/latest envelope (Throughput, AvgRespTime, AvgWaitTime)         ← already decoded
  → buildReplicaSpec: occ_i = Thr[req/s] · max(0, resp−wait)[s]   [collector/replicaspec.go]
  → handlers.go aggregation loop: sumOcc, perReplica = sumOcc / #contributing pods
  → config.AllocationData.AvgConcurrency  (new field)            [optimizer-light]
  → BuildRecord → ServerRecord.{OccPerReplica, OccTotal}         [monitor]
  → inferno-cycles.jsonl  +  Collector stdout lines
```

## Edits

### 1. `../optimizer-light/pkg/config/types.go` — `AllocationData`
Add one field beside the existing attained-observability fields `ITLAverage`/`TTFTAverage`:

```go
AvgConcurrency float32 `json:"avgConcurrency"` // average in-service occupancy (Little's Law: throughput × in-service time)
```

The optimizer ignores it; it rides along in `SystemData` harmlessly, mirroring the
existing `ITLAverage`/`TTFTAverage` precedent. This is the only sibling-repo change.

### 2. `pkg/collector/replicaspec.go` — `buildReplicaSpec`
Compute per-pod occupancy from the **raw** envelope values (req/s and ms — **not** the
`×60` req/min value stored into `Load.Throughput`):

```go
// in-service occupancy via Little's Law: throughput(req/s) × in-service time(s).
// resp−wait can go slightly negative under measurement noise (vllm: resp and wait
// come from different request populations) → clamp to 0.
inServiceMs := env.Result.AvgRespTime - env.Result.AvgWaitTime
if inServiceMs < 0 {
    inServiceMs = 0
}
occ := env.Result.Throughput * inServiceMs / 1000
```

Set `CurrentAlloc.AvgConcurrency: occ` in the returned per-pod `ServerSpec`.

### 3. `pkg/collector/handlers.go` — aggregation loop + log lines
In the existing per-pod loop (around line 168-196):
- accumulate `sumOcc += float64(spec.CurrentAlloc.AvgConcurrency)`
- extend the per-pod log line (line 190) with `occ=%.2f` (= `spec.CurrentAlloc.AvgConcurrency`)

After the loop, set the deployment roll-up (mean over **contributing** pods — robust to
cold-start/stale skips):

```go
if len(replicaSpecs) > 0 {
    curAlloc.AvgConcurrency = float32(sumOcc / float64(len(replicaSpecs)))
}
```

Extend the per-deployment `curAlloc[...]` log line (line 220) with
`occPerReplica=%.2f occTotal=%.2f`, where
`occTotal = curAlloc.AvgConcurrency × curAlloc.NumReplicas`.

Extend the per-replica `replicaAlloc[...]` log line (line 236) with `occ=%.2f`
(= `r.CurrentAlloc.AvgConcurrency`).

### 4. `pkg/monitor/record.go` — `ServerRecord`
Add to the Performance block, next to `ITL`/`TTFT`:

```go
OccPerReplica float32 `json:"occPerReplica"` // avg in-service occupancy per replica (batch fill)
OccTotal      float32 `json:"occTotal"`      // total in-service occupancy across replicas
```

### 5. `pkg/monitor/builder.go` — `BuildRecord`
In the per-server assembly (around line 29-39):

```go
OccPerReplica: s.CurrentAlloc.AvgConcurrency,
OccTotal:      s.CurrentAlloc.AvgConcurrency * float32(s.CurrentAlloc.NumReplicas),
```

Use `s.CurrentAlloc.NumReplicas` (the **observed** current replica count the occupancy
was measured over), **not** the optimizer's decided `sr.NumReplicas`.

## Aggregation semantics

- **Per-replica average** = `sumOcc / len(replicaSpecs)` — mean batch fill over the pods
  that reported this cycle. Comparable to the per-replica `M*` on the same line.
- **Total** = `perReplica × NumReplicas`. When all pods report, this equals the observed
  sum; under partial skips it extrapolates the missing replicas to the mean (a better
  total estimate than undercounting the sum).

## Units & edge cases

- **Units**: raw `env.Result.Throughput` is **req/s**; `AvgRespTime`/`AvgWaitTime` are
  **ms**. `occ = Thr · (resp−wait)/1000` is a dimensionless count. The one easy mistake is
  using the `×60` req/min `Load.Throughput` value — don't.
- **Negative clamp**: vllm's `AvgWaitTime` (queue-time histogram delta over the window)
  and `AvgRespTime` (mean over completed samples) are different populations ⇒ `resp−wait`
  can occasionally be negative → clamp to 0.
- **No completions** (`Throughput == 0`) ⇒ `occ = 0` naturally.
- **Skipped pods** (cold-start 404, stale result, saturated-and-held) contribute nothing —
  identical to how ITL/TTFT already treat them.

### 6. `dashboard/dashboard.py` — new dedicated Occupancy panel
`servers_df` is auto-normalized from the `servers[]` array via `pd.json_normalize`, so the
new `occPerReplica` / `occTotal` JSON fields appear as DataFrame columns **automatically** —
no `load_data` logic changes.

- Add `fig_occupancy(df)` (mirrors the `fig_controls` pattern), 2 stacked subplots:
  - **Top — "Batch fill vs M*":** `occPerReplica` per server (lines+markers), with each
    server's `maxBatch` (M*) drawn as a **dashed ceiling line** in the same colour. This is
    the primary plot: in-service occupancy against the per-replica concurrency ceiling.
  - **Bottom — "Total in-flight":** `occTotal` per server.
- Wire it in: add `dcc.Graph(id="occupancy-panel")` to `app.layout` (placed after the
  Controls panel), add the `Output("occupancy-panel", "figure")` to the `update` callback,
  and return `fig_occupancy(servers_df)`.
- Update the header docstring "five panels" → "six panels" and add `occPerReplica`,
  `occTotal` to the `load_data` `servers_df` column list.

No new dependencies; no `load_data` parsing changes.

## Cross-check (validation, not code)

During the cluster test:
- qa: `AvgNumInServ` (in-*system*) should ≈ `Throughput × AvgRespTime` (in-system Little's Law).
- blis: `mean(NumRunningBatchRequests)` should ≈ our logged in-service value.
- Eyeball the new Occupancy panel: `occPerReplica` should track toward `maxBatch` (M*)
  under load and sit well below it when under-utilised.

Wild disagreement is a real signal. Native values are not logged.

## Out of scope (YAGNI)

- No new `evaluator.AnalysisData` field; no re-adding qa's dropped `AvgNumInServ`.

## Files touched

- `../optimizer-light/pkg/config/types.go` (sibling repo)
- `pkg/collector/replicaspec.go`
- `pkg/collector/handlers.go`
- `pkg/monitor/record.go`
- `pkg/monitor/builder.go`
- `dashboard/dashboard.py`
- docs: `CLAUDE.md` visualization section + this spec
