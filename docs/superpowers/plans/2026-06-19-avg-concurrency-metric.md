# avgConcurrency (In-Service Occupancy) Metric Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Log average in-service concurrency (batch occupancy) per managed server every control cycle — on the Collector's console lines, in the JSONL cycle record, and on a new dashboard panel.

**Architecture:** Compute occupancy uniformly in the Collector via Little's Law `L = Throughput × (AvgRespTime − AvgWaitTime)` from fields already present in each `/latest` envelope (zero server-sim/evaluator changes). The value rides through `config.AllocationData.AvgConcurrency` (a new field in the `optimizer-light` sibling module) on both per-pod and deployment specs, into `monitor.ServerRecord`, and onto a dedicated Plotly panel.

**Tech Stack:** Go 1.24 (control-loop + optimizer-light modules), Python/Dash/Plotly (dashboard).

## Global Constraints

- **Occupancy formula (in-service):** `occ = Throughput[req/s] × max(0, AvgRespTime − AvgWaitTime)[ms] / 1000`. Dimensionless request count.
- **Units:** use the **raw** `env.Result.Throughput` (req/**s**) and ms latencies — **never** the `×60` req/min value stored into `Load.Throughput`.
- **Negative clamp:** `AvgRespTime − AvgWaitTime` may be negative under measurement noise → clamp to 0.
- **Deployment per-replica roll-up:** `sumOcc / len(replicaSpecs)` (mean over pods that reported this cycle).
- **Deployment total roll-up:** `OccPerReplica × CurrentAlloc.NumReplicas` (the **observed** current replica count, NOT the optimizer's decided `NumReplicas`).
- **JSON field names (exact):** `avgConcurrency` (optimizer-light), `occPerReplica`, `occTotal` (ServerRecord).
- **No new dependencies** anywhere.
- Follow existing code style: Go tests use plain `testing` with `t.Fatalf`; the dashboard uses `make_subplots` + `go.Scatter` per `fig_controls`.

---

### Task 1: Add `AvgConcurrency` to optimizer-light and consume it in control-loop

This is a cross-repo release: the field lives in the `optimizer-light` module (pinned at `v0.8.0`, no replace directive), so control-loop only sees it after a new tag is published and go.mod is bumped. The Docker build does `COPY . . && go build`, pulling the module from the proxy — a local `replace` would break the image build, so we release-then-consume.

**Files:**
- Modify: `../optimizer-light/pkg/config/types.go` (`AllocationData`, after `TTFTAverage` ~line 133)
- Modify: `go.mod` (control-loop, the `optimizer-light` require line)

**Interfaces:**
- Produces: `config.AllocationData.AvgConcurrency float32` (JSON tag `avgConcurrency`) — consumed by Tasks 2, 3, 4.

- [ ] **Step 1: Add the field in optimizer-light**

In `../optimizer-light/pkg/config/types.go`, inside `type AllocationData struct`, add the field between `TTFTAverage` and `Load`:

```go
	ITLAverage     float32        `json:"itlAverage"`     // average ITL
	TTFTAverage    float32        `json:"ttftAverage"`    // average TTFT
	AvgConcurrency float32        `json:"avgConcurrency"` // average in-service occupancy (Little's Law: throughput × in-service time)
	Load           ServerLoadSpec `json:"load"`           // server load statistics
```

(Re-align the existing fields' struct tags to gofmt; `go build` / `gofmt` will confirm.)

- [ ] **Step 2: Verify optimizer-light builds**

Run: `cd ../optimizer-light && gofmt -w pkg/config/types.go && go build ./...`
Expected: no output (success).

- [ ] **Step 3: Commit, tag, and push optimizer-light**

> ⚠️ Pushing a tag is outward-facing and hard to reverse. Confirm with the user before pushing, and confirm the target branch (assumed `main`) and version (`v0.8.1`, a backward-compatible additive field).

```bash
cd ../optimizer-light
git add pkg/config/types.go
git commit -m "feat(config): add AvgConcurrency to AllocationData

Carries per-pod / per-deployment average in-service occupancy
(Little's Law) for observability. Optimizer ignores it.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
git tag v0.8.1
git push origin HEAD
git push origin v0.8.1
```

- [ ] **Step 4: Bump control-loop to the new version**

Run:
```bash
cd ../control-loop
go get github.com/llm-inferno/optimizer-light@v0.8.1
go mod tidy
```
Expected: `go.mod` now shows `github.com/llm-inferno/optimizer-light v0.8.1`.

- [ ] **Step 5: Verify control-loop still builds against the new field**

Run: `go build ./...`
Expected: no output (success). The field is unused so far — that's fine.

- [ ] **Step 6: Commit the go.mod bump**

```bash
git add go.mod go.sum
git commit -m "chore(deps): bump optimizer-light to v0.8.1 (AvgConcurrency field)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

> **Local-iteration note (optional):** to develop Tasks 2–4 before pushing the tag, temporarily add `replace github.com/llm-inferno/optimizer-light => ../optimizer-light` to control-loop `go.mod`. You MUST remove it and complete Steps 3–6 before building any Docker image.

---

### Task 2: Compute per-pod occupancy in `buildReplicaSpec`

**Files:**
- Modify: `pkg/collector/replicaspec.go:13-36` (`buildReplicaSpec`)
- Test: `pkg/collector/replicaspec_test.go` (add cases)

**Interfaces:**
- Consumes: `config.AllocationData.AvgConcurrency` (Task 1); `simResult.Throughput`, `simResult.AvgRespTime`, `simResult.AvgWaitTime` (already decoded in `pkg/collector/serversim.go`).
- Produces: per-pod `ServerSpec.CurrentAlloc.AvgConcurrency` populated — consumed by Task 3's aggregation loop.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/collector/replicaspec_test.go`:

```go
func TestBuildReplicaSpecOccupancy(t *testing.T) {
	// Throughput 4 req/s, in-service time = 600-100 = 500 ms = 0.5 s → occ = 4*0.5 = 2.0
	env := &latestEnvelope{
		EffectiveInput: simRequest{MaxConcurrency: 32},
		Result:         simResult{Throughput: 4, AvgRespTime: 600, AvgWaitTime: 100},
	}
	spec, ok := buildReplicaSpec("srv", "pod-1", "c", "m", 64, 32, "H100", env)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if got := spec.CurrentAlloc.AvgConcurrency; got != 2.0 {
		t.Fatalf("AvgConcurrency = %v, want 2.0", got)
	}
}

func TestBuildReplicaSpecOccupancyNegativeClamps(t *testing.T) {
	// wait (300) > resp (100) under noise → in-service time clamps to 0 → occ = 0
	env := &latestEnvelope{
		EffectiveInput: simRequest{MaxConcurrency: 32},
		Result:         simResult{Throughput: 4, AvgRespTime: 100, AvgWaitTime: 300},
	}
	spec, ok := buildReplicaSpec("srv", "pod-1", "c", "m", 64, 32, "H100", env)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if got := spec.CurrentAlloc.AvgConcurrency; got != 0 {
		t.Fatalf("AvgConcurrency = %v, want 0 (clamped)", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/collector/ -run TestBuildReplicaSpecOccupancy -v`
Expected: FAIL — `AvgConcurrency = 0, want 2.0` (field exists but is never set).

- [ ] **Step 3: Implement the computation**

In `pkg/collector/replicaspec.go`, replace the `return` block (lines 17-35) so it computes occupancy before constructing the spec:

```go
	// in-service occupancy via Little's Law: throughput(req/s) × in-service time(s).
	// resp−wait can go slightly negative under measurement noise (e.g. vllm: resp and
	// wait come from different request populations) → clamp to 0.
	inServiceMs := env.Result.AvgRespTime - env.Result.AvgWaitTime
	if inServiceMs < 0 {
		inServiceMs = 0
	}
	occ := env.Result.Throughput * inServiceMs / 1000

	return config.ServerSpec{
		Name:         serverName + ctrl.ReplicaNameSeparator + podName,
		Class:        class,
		Model:        model,
		MaxQueueSize: maxQueueSize,
		CurrentAlloc: config.AllocationData{
			Accelerator:    accelerator,
			MaxBatch:       inForceMaxBatch,
			NumReplicas:    1,
			ITLAverage:     env.Result.AvgITL,
			TTFTAverage:    env.Result.AvgTTFT,
			AvgConcurrency: occ,
			Load: config.ServerLoadSpec{
				ArrivalRate:  env.EffectiveInput.RPS * 60,
				Throughput:   env.Result.Throughput * 60,
				AvgInTokens:  int(env.EffectiveInput.AvgInputTokens),
				AvgOutTokens: int(env.EffectiveInput.AvgOutputTokens),
			},
		},
	}, true
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/collector/ -run TestBuildReplicaSpec -v`
Expected: PASS for all `TestBuildReplicaSpec*` (existing coherent/stale/nil/zero cases plus the two new occupancy cases). The existing `TestBuildReplicaSpecCoherent` leaves resp/wait at 0, so its `AvgConcurrency` is 0 — it asserts nothing about occupancy, so it still passes.

- [ ] **Step 5: Commit**

```bash
git add pkg/collector/replicaspec.go pkg/collector/replicaspec_test.go
git commit -m "feat(collector): compute per-pod in-service occupancy (Little's Law)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Aggregate occupancy and extend the three Collector console log lines

**Files:**
- Modify: `pkg/collector/handlers.go` — aggregation loop (~168-200), `curAlloc` assembly (~204-218), and three `fmt.Printf` lines (190, 220, 236)

**Interfaces:**
- Consumes: per-pod `spec.CurrentAlloc.AvgConcurrency` (Task 2).
- Produces: deployment `curAlloc.AvgConcurrency` populated (mean over reporting pods) — consumed by Task 4 via `collectorInfo.Spec`. The per-pod field is already populated for Task 4's per-replica needs.

> `handlers.go` is a large k8s-dependent handler with no existing unit tests; verification here is `go build` + `go vet` + the Task 2 unit test for the arithmetic. Integration validation happens on-cluster (Task 6 cross-check).

- [ ] **Step 1: Declare the occupancy accumulator**

In `pkg/collector/handlers.go`, find the aggregation setup at line 167:

```go
				// aggregate
				var weightedITL, weightedTTFT float64
```
Change to add the accumulator:
```go
				// aggregate
				var weightedITL, weightedTTFT, sumOcc float64
```

- [ ] **Step 2: Accumulate per-pod occupancy and extend the per-pod log line**

Replace the per-pod log + accumulation block (lines 189-195):

```go
					w := float64(spec.CurrentAlloc.Load.Throughput)
					fmt.Printf("pod %s: TTFT=%.1fms ITL=%.1fms throughputRPM=%.2f\n",
						p.Name, spec.CurrentAlloc.TTFTAverage, spec.CurrentAlloc.ITLAverage, w)
					weightedITL += float64(spec.CurrentAlloc.ITLAverage) * w
					weightedTTFT += float64(spec.CurrentAlloc.TTFTAverage) * w
					totalThroughputRPM += w
					replicaSpecs = append(replicaSpecs, spec)
```
with:
```go
					w := float64(spec.CurrentAlloc.Load.Throughput)
					fmt.Printf("pod %s: TTFT=%.1fms ITL=%.1fms throughputRPM=%.2f occ=%.2f\n",
						p.Name, spec.CurrentAlloc.TTFTAverage, spec.CurrentAlloc.ITLAverage, w,
						spec.CurrentAlloc.AvgConcurrency)
					weightedITL += float64(spec.CurrentAlloc.ITLAverage) * w
					weightedTTFT += float64(spec.CurrentAlloc.TTFTAverage) * w
					sumOcc += float64(spec.CurrentAlloc.AvgConcurrency)
					totalThroughputRPM += w
					replicaSpecs = append(replicaSpecs, spec)
```

- [ ] **Step 3: Set the deployment per-replica roll-up on `curAlloc`**

Add the `AvgConcurrency` field to the `curAlloc` literal (after `TTFTAverage: ttftAvg,` at line 209):

```go
		curAlloc := config.AllocationData{
			Accelerator: d.Labels[ctrl.KeyAccelerator],
			NumReplicas: int(numReplicas),
			MaxBatch:    maxBatchSize,
			ITLAverage:  itlAvg,
			TTFTAverage: ttftAvg,
			Load: config.ServerLoadSpec{
```
Insert `AvgConcurrency` immediately after `TTFTAverage: ttftAvg,`:
```go
			TTFTAverage: ttftAvg,
			AvgConcurrency: occAvg,
```
First, declare `occAvg` in the outer per-deployment scope so it is visible both inside the aggregation `else` block and at the `curAlloc` literal. At `handlers.go:104`, change:
```go
		var itlAvg, ttftAvg float32
```
to:
```go
		var itlAvg, ttftAvg, occAvg float32
```

Then compute the per-replica mean. Right after the `if totalThroughputRPM > 0 { ... }` block closes at line 200 (still inside the `else` aggregation block, where `sumOcc` and `replicaSpecs` are in scope), add:

```go
				if len(replicaSpecs) > 0 {
					occAvg = float32(sumOcc / float64(len(replicaSpecs)))
				}
```

- [ ] **Step 4: Extend the per-deployment `curAlloc` log line**

Replace the `curAlloc[...]` Printf (lines 220-223):

```go
		fmt.Printf("curAlloc[%s]: replicas=%d acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d\n",
			serverName, curAlloc.NumReplicas, curAlloc.Accelerator, curAlloc.MaxBatch,
			curAlloc.ITLAverage, curAlloc.TTFTAverage,
			curAlloc.Load.ArrivalRate, curAlloc.Load.Throughput, curAlloc.Load.AvgInTokens, curAlloc.Load.AvgOutTokens)
```
with (add `occPerReplica`/`occTotal`; total = perReplica × replicas):
```go
		fmt.Printf("curAlloc[%s]: replicas=%d acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d occPerReplica=%.2f occTotal=%.2f\n",
			serverName, curAlloc.NumReplicas, curAlloc.Accelerator, curAlloc.MaxBatch,
			curAlloc.ITLAverage, curAlloc.TTFTAverage,
			curAlloc.Load.ArrivalRate, curAlloc.Load.Throughput, curAlloc.Load.AvgInTokens, curAlloc.Load.AvgOutTokens,
			curAlloc.AvgConcurrency, curAlloc.AvgConcurrency*float32(curAlloc.NumReplicas))
```

- [ ] **Step 5: Extend the per-replica `replicaAlloc` log line**

Replace the `replicaAlloc[...]` Printf (lines 236-239):

```go
		fmt.Printf("replicaAlloc[%s]: acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d\n",
			r.Name, r.CurrentAlloc.Accelerator, r.CurrentAlloc.MaxBatch,
			r.CurrentAlloc.ITLAverage, r.CurrentAlloc.TTFTAverage,
			r.CurrentAlloc.Load.ArrivalRate, r.CurrentAlloc.Load.Throughput, r.CurrentAlloc.Load.AvgInTokens, r.CurrentAlloc.Load.AvgOutTokens)
```
with:
```go
		fmt.Printf("replicaAlloc[%s]: acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d occ=%.2f\n",
			r.Name, r.CurrentAlloc.Accelerator, r.CurrentAlloc.MaxBatch,
			r.CurrentAlloc.ITLAverage, r.CurrentAlloc.TTFTAverage,
			r.CurrentAlloc.Load.ArrivalRate, r.CurrentAlloc.Load.Throughput, r.CurrentAlloc.Load.AvgInTokens, r.CurrentAlloc.Load.AvgOutTokens,
			r.CurrentAlloc.AvgConcurrency)
```

- [ ] **Step 6: Build and vet**

Run: `go build ./... && go vet ./pkg/collector/`
Expected: no output (success). If `occAvg declared and not used` or scope errors appear, fix the `var occAvg float32` placement per Step 3.

- [ ] **Step 7: Run the collector tests**

Run: `go test ./pkg/collector/ -v`
Expected: PASS (no regressions; Task 2 occupancy tests still green).

- [ ] **Step 8: Commit**

```bash
git add pkg/collector/handlers.go
git commit -m "feat(collector): aggregate occupancy + log occ on all three lines

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Add `OccPerReplica`/`OccTotal` to the JSONL cycle record

**Files:**
- Modify: `pkg/monitor/record.go` (`ServerRecord`, Performance block ~line 28-32)
- Modify: `pkg/monitor/builder.go:29-39` (`BuildRecord` per-server assembly)
- Test: `pkg/monitor/builder_test.go` (new file)

**Interfaces:**
- Consumes: `config.ServerSpec.CurrentAlloc.AvgConcurrency` and `.NumReplicas` (Tasks 1, 3).
- Produces: `ServerRecord.OccPerReplica`, `ServerRecord.OccTotal` in the JSONL (consumed by Task 5 dashboard).

- [ ] **Step 1: Write the failing test**

Create `pkg/monitor/builder_test.go`:

```go
package monitor

import (
	"testing"

	"github.com/llm-inferno/optimizer-light/pkg/config"
)

func TestBuildRecordOccupancy(t *testing.T) {
	servers := []config.ServerSpec{{
		Name:  "srv",
		Class: "Bronze",
		Model: "m",
		CurrentAlloc: config.AllocationData{
			NumReplicas:    3,
			AvgConcurrency: 2.5, // per-replica
		},
	}}
	rec := BuildRecord(1, servers, nil, nil, config.ModelData{}, config.CapacityData{}, 0, 0, 0, 0, 0)
	if len(rec.Servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(rec.Servers))
	}
	sr := rec.Servers[0]
	if sr.OccPerReplica != 2.5 {
		t.Fatalf("OccPerReplica = %v, want 2.5", sr.OccPerReplica)
	}
	if sr.OccTotal != 7.5 { // 2.5 * 3 replicas (observed current count)
		t.Fatalf("OccTotal = %v, want 7.5", sr.OccTotal)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/monitor/ -run TestBuildRecordOccupancy -v`
Expected: FAIL — compile error `sr.OccPerReplica undefined` (fields not added yet).

- [ ] **Step 3: Add the fields to `ServerRecord`**

In `pkg/monitor/record.go`, in the Performance block of `ServerRecord`, after the `SLO_TTFT` line, add:

```go
	// Performance: observed attained values vs SLO targets
	ITL      float32 `json:"itl"`      // attained average ITL (ms)
	TTFT     float32 `json:"ttft"`     // attained average TTFT (ms)
	SLO_ITL  float32 `json:"sloItl"`   // SLO target ITL (ms)
	SLO_TTFT float32 `json:"sloTtft"`  // SLO target TTFT (ms)

	// Occupancy: average in-service concurrency (Little's Law)
	OccPerReplica float32 `json:"occPerReplica"` // avg in-service occupancy per replica (batch fill)
	OccTotal      float32 `json:"occTotal"`      // total in-service occupancy across replicas
```

- [ ] **Step 4: Map the fields in `BuildRecord`**

In `pkg/monitor/builder.go`, in the `sr := ServerRecord{...}` literal (lines 29-39), add after `TTFT: s.CurrentAlloc.TTFTAverage,`:

```go
			ITL:           s.CurrentAlloc.ITLAverage,
			TTFT:          s.CurrentAlloc.TTFTAverage,
			OccPerReplica: s.CurrentAlloc.AvgConcurrency,
			OccTotal:      s.CurrentAlloc.AvgConcurrency * float32(s.CurrentAlloc.NumReplicas),
```

(`s.CurrentAlloc.NumReplicas` is the observed current count — do NOT use the optimizer's `alloc.NumReplicas` set later in the function.)

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./pkg/monitor/ -run TestBuildRecordOccupancy -v`
Expected: PASS.

- [ ] **Step 6: Build everything**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 7: Commit**

```bash
git add pkg/monitor/record.go pkg/monitor/builder.go pkg/monitor/builder_test.go
git commit -m "feat(monitor): log occPerReplica/occTotal in cycle record

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Add the dedicated Occupancy dashboard panel

**Files:**
- Modify: `dashboard/dashboard.py` — add palette constant, `fig_occupancy`, layout `dcc.Graph`, callback `Output` + return, docstring

**Interfaces:**
- Consumes: `servers_df` columns `occPerReplica`, `occTotal`, `maxBatch`, `name`, `cycle` (auto-normalized from JSONL — Task 4 emits the first two).
- Produces: a new `occupancy-panel` graph.

> `servers_df` is built via `pd.json_normalize(record_path="servers")`, so the new JSON fields appear as columns automatically — no `load_data` logic change.

- [ ] **Step 1: Add a palette constant**

In `dashboard/dashboard.py`, near the top after the imports (e.g. just before `def _sync_pod_log():` at line 49), add:

```python
# Color cycle so each server's occupancy line and its M* ceiling share a color.
_PALETTE = ["#636EFA", "#EF553B", "#00CC96", "#AB63FA", "#FFA15A", "#19D3F3", "#FF6692", "#B6E880"]
```

- [ ] **Step 2: Add `fig_occupancy`**

Insert this function immediately after `fig_controls` (after line 309, before `fig_capacity`):

```python
def fig_occupancy(df):
    title = "Occupancy: In-Service Concurrency (batch fill)"
    if df.empty or "occPerReplica" not in df.columns:
        return _empty(title)

    fig = make_subplots(
        rows=2, cols=1, shared_xaxes=True,
        subplot_titles=("Per-Replica Occupancy vs M* (maxBatch)", "Total In-Flight"),
        vertical_spacing=0.12,
    )

    for i, server in enumerate(df["name"].unique()):
        color = _PALETTE[i % len(_PALETTE)]
        s = df[df["name"] == server].sort_values("cycle")
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["occPerReplica"],
            mode="lines+markers", name=f"{server}", line=dict(color=color),
        ), row=1, col=1)
        if "maxBatch" in s.columns:
            fig.add_trace(go.Scatter(
                x=s["cycle"], y=s["maxBatch"],
                mode="lines", name=f"{server} M*",
                line=dict(color=color, dash="dash"), showlegend=False,
            ), row=1, col=1)
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["occTotal"],
            mode="lines+markers", name=f"{server}", line=dict(color=color),
            showlegend=False,
        ), row=2, col=1)

    fig.update_layout(
        title=title, xaxis2_title="Cycle",
        template="plotly_dark", paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e",
        legend=dict(orientation="h", yanchor="bottom", y=1.02),
    )
    fig.update_yaxes(title_text="Requests", row=1, col=1)
    fig.update_yaxes(title_text="Requests", row=2, col=1)
    return fig
```

- [ ] **Step 3: Add the graph to the layout**

In `app.layout`, after the `controls-panel` graph (line 420), add:

```python
        dcc.Graph(id="controls-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="occupancy-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="capacity-panel", style={"marginBottom": "8px"}),
```

- [ ] **Step 4: Wire the callback Output and return**

In the `@app.callback` Output list (lines 428-434), add after the controls-panel Output:

```python
        Output("controls-panel", "figure"),
        Output("occupancy-panel", "figure"),
        Output("capacity-panel", "figure"),
```

In `update()`'s return tuple (lines 439-445), add `fig_occupancy(servers_df)` in the matching position:

```python
    return (
        fig_workload(servers_df),
        fig_performance(servers_df),
        fig_controls(servers_df),
        fig_occupancy(servers_df),
        fig_capacity(capacity_df),
        fig_internals(internals_df, servers_df),
    )
```

- [ ] **Step 5: Update the docstrings**

In the module docstring (line 4) change `displays five panels:` → `displays six panels:`. In `load_data`'s docstring (lines 94-97), add `occPerReplica`, `occTotal` to the `servers_df columns:` list after `itl, ttft, sloItl, sloTtft,`.

- [ ] **Step 6: Verify the file parses**

Run: `cd dashboard && python3 -m py_compile dashboard.py && echo OK`
Expected: `OK` (no syntax errors). The Output count (6) must equal the return-tuple length (6) — Dash raises at runtime otherwise; py_compile won't catch a mismatch, so double-check they match.

- [ ] **Step 7: Commit**

```bash
cd /Users/tantawi/Projects/llm-inferno/control-loop
git add dashboard/dashboard.py
git commit -m "feat(dashboard): add Occupancy panel (in-service concurrency vs M*)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Update CLAUDE.md and verify the full build

**Files:**
- Modify: `CLAUDE.md` (Visualization section)

- [ ] **Step 1: Document the new metric**

In `CLAUDE.md`, in the Visualization section, update the cycle-record contents sentence to include occupancy. Find:

> Each record contains: timestamp, cycle counter, per-server workload (RPM, tokens), per-server attained ITL/TTFT with SLO targets, per-server allocation (replicas, cost, accelerator), total cost, EKF model parameters (alpha/beta/gamma), and cycle phase timings.

Add `, per-server in-service occupancy (occPerReplica/occTotal, Little's-Law: throughput × in-service time)` after the ITL/TTFT clause. Then update the dashboard panel list:

> displays four auto-refreshing panels: Workload, Performance, Controls, and EKF Internals.

→ `displays auto-refreshing panels: Workload, Performance, Controls, Occupancy, Capacity, and EKF Internals.`

- [ ] **Step 2: Full build + test sweep**

Run: `go build ./... && go test ./pkg/collector/ ./pkg/monitor/ -v`
Expected: build clean; all tests PASS.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: note occPerReplica/occTotal and Occupancy panel in CLAUDE.md

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## On-cluster validation (post-implementation, during the issue #47 / #22 cluster test)

Not a code task — fold into the planned cluster run:

- Confirm the Collector console shows `occ=…` on pod/replicaAlloc lines and `occPerReplica=… occTotal=…` on curAlloc lines.
- Confirm `inferno-cycles.jsonl` records carry `occPerReplica`/`occTotal`, and the dashboard Occupancy panel renders with the M* ceiling overlay.
- Cross-check (sanity, expect rough agreement):
  - qa: native `AvgNumInServ` (in-*system*) ≈ `throughput × AvgRespTime`.
  - blis: `mean(NumRunningBatchRequests)` ≈ logged in-service value.
  - All: `occPerReplica` should rise toward `maxBatch` (M*) under load, sit well below when under-utilised, and never exceed it by much.
