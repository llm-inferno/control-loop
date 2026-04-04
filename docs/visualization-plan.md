# Plan: JSON Cycle Logging + Python Dash Visualization

## Context

The llm-inferno control loop is a research/experimental system. We need visualization of system dynamics across four metric groups: workload, performance, controls, and EKF internals. The approach is a structured JSON line log (JSONL) emitted by the Go controller each cycle, plus a standalone Python Dash dashboard that reads and plots it. This keeps monitoring fully separated from functional logic and avoids heavy infrastructure (no Grafana, no Prometheus instrumentation).

## Part 1: Go Package `pkg/monitor/`

Three new files, zero modifications to existing logic.

### `pkg/monitor/record.go` — JSON schema types

```go
CycleRecord {
    Timestamp    string          // RFC3339Nano
    Cycle        int64           // monotonic counter
    Servers      []ServerRecord  // per-deployment metrics
    Internals    []ModelParms    // EKF alpha/beta/gamma
    TotalCost    float32         // sum of all server costs
    Timing       TimingRecord    // collect/tune/optimize/actuate/total ms
}

ServerRecord {
    Name, Class, Model           // identity
    ArrivalRate, Throughput      // workload (RPM)
    AvgInTokens, AvgOutTokens   // workload (tokens)
    ITL, TTFT                    // observed attained performance (from collector CurrentAlloc)
    SLO_ITL, SLO_TTFT           // SLO targets (from ServiceClassData)
    Accelerator                  // decided accelerator (from optimizer)
    NumReplicas                  // decided replicas (from optimizer)
    Cost                         // decided cost (from optimizer)
}

ModelParms { Model, Acc, Alpha, Beta, Gamma }
TimingRecord { CollectMs, TuneMs, OptimizeMs, ActuateMs, TotalMs }
```

**Key design choice**: `ITL`/`TTFT` come from the collector's `CurrentAlloc` (what the system is *actually doing*), while `Replicas`/`Cost`/`Accelerator` come from the optimizer's solution (what was *decided*). This gives the dashboard both observed performance and control decisions.

### `pkg/monitor/builder.go` — BuildRecord() function

Single function that assembles a `CycleRecord` from the controller's existing variables:
- Iterates `servers []config.ServerSpec` for observed metrics (CurrentAlloc)
- Looks up SLO targets by matching `server.Class` → `ServiceClassSpec.Name`, then `server.Model` → `ModelTarget.Model`
- Gets decided replicas/cost/accelerator from `solution map[string]config.AllocationData`
- Extracts alpha/beta/gamma from `modelData.PerfData[]`

### `pkg/monitor/monitor.go` — CycleRecorder

- `NewCycleRecorder()`: reads `INFERNO_CYCLE_LOG` env var (default: `inferno-cycles.jsonl`), opens file in append mode, returns `*CycleRecorder`. Nil receiver pattern: if env var is `""` or file open fails, all methods are no-ops.
- `Record(rec *CycleRecord) error`: JSON-encodes one line, mutex-protected.
- `Close() error`: closes the file.

## Part 2: Controller Integration

**File**: `pkg/controller/controller.go`

Four minimal, additive changes:

1. **Import** `pkg/monitor` (line ~3-13)
2. **Add fields** to `Controller` struct (line ~29): `recorder *monitor.CycleRecorder` and `cycleNum int64`
3. **Init recorder** at end of `Init()` (after line 91, before `return nil`):
   ```go
   if rec, err := monitor.NewCycleRecorder(); err != nil {
       fmt.Printf("warning: cycle logging disabled: %s\n", err)
   } else {
       a.recorder = rec
   }
   ```
4. **Emit record** at end of `Optimize()` (after line 276, before `return nil`):
   ```go
   a.cycleNum++
   rec := monitor.BuildRecord(a.cycleNum, a.State.SystemData.Spec.Servers.Spec,
       allocSolution.Spec, a.State.SystemData.Spec.ServiceClasses.Spec,
       a.State.currentModelData,
       collectTime.Milliseconds(), tuneTime.Milliseconds(),
       optimizeTime.Milliseconds(), actuateTime.Milliseconds(), totalTime.Milliseconds())
   a.recorder.Record(rec)
   ```

Zero existing lines modified. The `fmt.Printf` timing line remains as-is.

## Part 3: Python Dashboard

### `dashboard/dashboard.py`

Single-file Dash app with four panels, auto-refreshing every 5 seconds:

| Panel | X-axis | Traces |
|-------|--------|--------|
| **Workload** | cycle | Per-server: arrival rate (RPM), throughput (RPM) |
| **Performance** | cycle | Per-server: attained ITL (solid) vs SLO ITL (dashed), attained TTFT (solid) vs SLO TTFT (dashed) |
| **Controls** | cycle | Per-server: replicas (left y-axis); total cost (right y-axis) |
| **Internals** | cycle | Per-model/acc: alpha, beta, gamma as subplots |

Data loading: read JSONL file, flatten `servers[]` and `internals[]` into DataFrames via `pd.json_normalize`.

### `dashboard/requirements.txt`

```
dash>=2.14
plotly>=5.18
pandas>=2.0
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `INFERNO_CYCLE_LOG` | `inferno-cycles.jsonl` | JSONL output path. `""` disables logging. |
| `INFERNO_DASH_REFRESH` | `5000` | Dashboard refresh interval (ms) |

## Files Summary

| Action | File |
|--------|------|
| Create | `pkg/monitor/record.go` |
| Create | `pkg/monitor/builder.go` |
| Create | `pkg/monitor/monitor.go` |
| Create | `dashboard/dashboard.py` |
| Create | `dashboard/requirements.txt` |
| Modify | `pkg/controller/controller.go` (4 insertions, 0 deletions) |

## Task Sequence

1. Create `pkg/monitor/record.go` — define all JSON schema structs
2. Create `pkg/monitor/builder.go` — BuildRecord() with SLO lookup logic
3. Create `pkg/monitor/monitor.go` — CycleRecorder with file I/O
4. Modify `pkg/controller/controller.go` — add import, fields, init, and emit call
5. Verify: `go build ./...`
6. Create `dashboard/requirements.txt`
7. Create `dashboard/dashboard.py` — four-panel Dash app
8. Verify: run controller locally, confirm JSONL output, run dashboard

## Verification

1. `go build ./...` — compiles cleanly
2. Run controller for a few cycles → `head -1 inferno-cycles.jsonl | python3 -m json.tool` — valid JSON with all fields populated
3. `cd dashboard && pip install -r requirements.txt && INFERNO_CYCLE_LOG=../inferno-cycles.jsonl python dashboard.py` — four panels render at localhost:8050
4. Set `INFERNO_CYCLE_LOG=""` → controller runs without crash, no file created
