# Dashboard: Capacity Panel + Pod Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Capacity panel to the dashboard (accelerators allocated vs available) and make the dashboard auto-fetch the JSONL log from the inferno pod via kubectl.

**Architecture:** Two independent changes. (1) Go side: extend `CycleRecord` with a `Capacity []AcceleratorCapacityRecord` field populated from the optimizer solution + capacity data, then write it to the JSONL log. (2) Python side: add `fig_capacity()` panel after Controls; add an optional background thread that runs `kubectl exec ... cat` periodically to refresh the local JSONL file automatically.

**Tech Stack:** Go 1.21 (pkg/monitor), Python 3 + Dash + Plotly (dashboard/), kubectl CLI

**Note:** There are no automated tests in this repo. Verification is done manually by running the dashboard against a live or recorded JSONL file.

---

### Task 1: Add AcceleratorCapacityRecord to CycleRecord

**Files:**
- Modify: `pkg/monitor/record.go`

Add a new struct `AcceleratorCapacityRecord` and a `Capacity` field to `CycleRecord`.

- [ ] **Step 1: Edit `pkg/monitor/record.go`**

Replace the file content with the following (adds `AcceleratorCapacityRecord` and `Capacity []AcceleratorCapacityRecord` to `CycleRecord`):

```go
package monitor

// CycleRecord is one JSON line written per control cycle.
type CycleRecord struct {
	Timestamp string                     `json:"ts"`        // RFC3339Nano
	Cycle     int64                      `json:"cycle"`     // monotonically increasing counter
	Servers   []ServerRecord             `json:"servers"`   // per-deployment metrics
	Internals []ModelParms               `json:"internals"` // EKF-tuned model parameters
	Capacity  []AcceleratorCapacityRecord `json:"capacity"`  // allocated vs available per accelerator type
	TotalCost float32                    `json:"totalCost"` // sum of all server costs
	Timing    TimingRecord               `json:"timing"`    // cycle phase durations in ms
}

// ServerRecord holds per-deployment metrics for one cycle.
type ServerRecord struct {
	// Identity
	Name  string `json:"name"`
	Class string `json:"class"`
	Model string `json:"model"`

	// Workload
	ArrivalRate  float32 `json:"rpm"`        // req/min (arrival rate)
	Throughput   float32 `json:"throughput"` // req/min (completed)
	AvgInTokens  int     `json:"avgInTok"`
	AvgOutTokens int     `json:"avgOutTok"`

	// Performance: observed attained values vs SLO targets
	ITL      float32 `json:"itl"`     // attained average ITL (ms)
	TTFT     float32 `json:"ttft"`    // attained average TTFT (ms)
	SLO_ITL  float32 `json:"sloItl"`  // SLO target ITL (ms)
	SLO_TTFT float32 `json:"sloTtft"` // SLO target TTFT (ms)

	// Controls: optimizer decisions
	Accelerator string  `json:"accelerator"`
	NumReplicas int     `json:"replicas"`
	Cost        float32 `json:"cost"`
}

// AcceleratorCapacityRecord holds allocated vs available counts for one accelerator type.
type AcceleratorCapacityRecord struct {
	Type      string `json:"type"`      // accelerator type name (e.g. "H100")
	Allocated int    `json:"allocated"` // number of accelerator units in use
	Available int    `json:"available"` // total number of units in the cluster
}

// ModelParms holds EKF-estimated latency model parameters for one model/accelerator pair.
type ModelParms struct {
	Model string  `json:"model"`
	Acc   string  `json:"acc"`
	Alpha float32 `json:"alpha"` // base overhead (ms)
	Beta  float32 `json:"beta"`  // compute time scaling
	Gamma float32 `json:"gamma"` // memory access time scaling
}

// TimingRecord holds the duration of each phase in the control cycle.
type TimingRecord struct {
	CollectMs  int64 `json:"collectMs"`
	TuneMs     int64 `json:"tuneMs"`
	OptimizeMs int64 `json:"optimizeMs"`
	ActuateMs  int64 `json:"actuateMs"`
	TotalMs    int64 `json:"totalMs"`
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd <repo-root>
go build ./pkg/monitor/...
```

Expected: no output (clean build).

- [ ] **Step 3: Commit**

```bash
git add pkg/monitor/record.go
git commit -m "feat: add AcceleratorCapacityRecord to CycleRecord"
```

---

### Task 2: Populate capacity data in BuildRecord

**Files:**
- Modify: `pkg/monitor/builder.go`
- Modify: `pkg/controller/controller.go`

`BuildRecord` gets a new parameter `capacity config.CapacityData`. It computes `allocated` by summing `NumReplicas` per accelerator type from the solution, then matches against `capacity.Count` to get `available`. Only accelerators that appear in the solution (i.e., allocated > 0) are included in the `Capacity` slice.

- [ ] **Step 1: Edit `pkg/monitor/builder.go`**

Replace the import block and function signature (full file replacement — it's short):

```go
package monitor

import (
	"time"

	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// BuildRecord assembles a CycleRecord from the data available at the end of a control cycle.
// servers: deployment-level specs from the collector (observed state).
// solution: optimizer allocation decisions (decided state).
// serviceClasses: SLO targets, matched by server class and model.
// modelData: current EKF-tuned model performance parameters.
// capacity: available accelerator counts (from capacity-data.json).
func BuildRecord(
	cycle int64,
	servers []config.ServerSpec,
	solution map[string]config.AllocationData,
	serviceClasses []config.ServiceClassSpec,
	modelData config.ModelData,
	capacity config.CapacityData,
	collectMs, tuneMs, optimizeMs, actuateMs, totalMs int64,
) *CycleRecord {
	serverRecords := make([]ServerRecord, 0, len(servers))
	var totalCost float32

	for _, s := range servers {
		sr := ServerRecord{
			Name:         s.Name,
			Class:        s.Class,
			Model:        s.Model,
			ArrivalRate:  s.CurrentAlloc.Load.ArrivalRate,
			Throughput:   s.CurrentAlloc.Load.Throughput,
			AvgInTokens:  s.CurrentAlloc.Load.AvgInTokens,
			AvgOutTokens: s.CurrentAlloc.Load.AvgOutTokens,
			ITL:          s.CurrentAlloc.ITLAverage,
			TTFT:         s.CurrentAlloc.TTFTAverage,
		}

		// SLO targets: match service class name then model name
		for _, sc := range serviceClasses {
			if sc.Name == s.Class {
				for _, mt := range sc.ModelTargets {
					if mt.Model == s.Model {
						sr.SLO_ITL = mt.SLO_ITL
						sr.SLO_TTFT = mt.SLO_TTFT
						break
					}
				}
				break
			}
		}

		// Optimizer decisions
		if alloc, ok := solution[s.Name]; ok {
			sr.Accelerator = alloc.Accelerator
			sr.NumReplicas = alloc.NumReplicas
			sr.Cost = alloc.Cost
			totalCost += alloc.Cost
		}

		serverRecords = append(serverRecords, sr)
	}

	// EKF-tuned model parameters
	internals := make([]ModelParms, 0, len(modelData.PerfData))
	for _, pd := range modelData.PerfData {
		internals = append(internals, ModelParms{
			Model: pd.Name,
			Acc:   pd.Acc,
			Alpha: pd.PerfParms.Alpha,
			Beta:  pd.PerfParms.Beta,
			Gamma: pd.PerfParms.Gamma,
		})
	}

	// Capacity: allocated (sum of replicas per accelerator type) vs available.
	// Only include accelerator types that are actively allocated.
	allocatedByType := map[string]int{}
	for _, alloc := range solution {
		if alloc.Accelerator != "" && alloc.NumReplicas > 0 {
			allocatedByType[alloc.Accelerator] += alloc.NumReplicas
		}
	}
	availableByType := map[string]int{}
	for _, ac := range capacity.Count {
		availableByType[ac.Type] = ac.Count
	}
	capacityRecords := make([]AcceleratorCapacityRecord, 0, len(allocatedByType))
	for accType, allocated := range allocatedByType {
		capacityRecords = append(capacityRecords, AcceleratorCapacityRecord{
			Type:      accType,
			Allocated: allocated,
			Available: availableByType[accType],
		})
	}

	return &CycleRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Cycle:     cycle,
		Servers:   serverRecords,
		Internals: internals,
		Capacity:  capacityRecords,
		TotalCost: totalCost,
		Timing: TimingRecord{
			CollectMs:  collectMs,
			TuneMs:     tuneMs,
			OptimizeMs: optimizeMs,
			ActuateMs:  actuateMs,
			TotalMs:    totalMs,
		},
	}
}
```

- [ ] **Step 2: Update the BuildRecord call in `pkg/controller/controller.go`**

Find this block (around line 305):

```go
	rec := monitor.BuildRecord(
		a.cycleNum,
		a.State.SystemData.Spec.Servers.Spec,
		allocSolution.Spec,
		a.State.SystemData.Spec.ServiceClasses.Spec,
		a.State.currentModelData,
		collectTime.Milliseconds(), tuneTime.Milliseconds(),
		optimizeTime.Milliseconds(), actuateTime.Milliseconds(), totalTime.Milliseconds(),
	)
```

Replace with:

```go
	rec := monitor.BuildRecord(
		a.cycleNum,
		a.State.SystemData.Spec.Servers.Spec,
		allocSolution.Spec,
		a.State.SystemData.Spec.ServiceClasses.Spec,
		a.State.currentModelData,
		a.State.SystemData.Spec.Capacity,
		collectTime.Milliseconds(), tuneTime.Milliseconds(),
		optimizeTime.Milliseconds(), actuateTime.Milliseconds(), totalTime.Milliseconds(),
	)
```

- [ ] **Step 3: Verify it compiles**

```bash
cd <repo-root>
go build ./...
```

Expected: no output (clean build).

- [ ] **Step 4: Commit**

```bash
git add pkg/monitor/builder.go pkg/controller/controller.go
git commit -m "feat: populate accelerator capacity data in cycle log"
```

---

### Task 3: Add fig_capacity() to dashboard

**Files:**
- Modify: `dashboard/dashboard.py`

Add `fig_capacity()` that reads the new `capacity` array from each cycle record. Plot `allocated` as a solid line and `available` as a dashed line per accelerator type. Insert the panel after Controls (between `controls-panel` and `internals-panel`).

- [ ] **Step 1: Update `load_data()` to also return a capacity_df**

`capacity_df` columns: `cycle`, `ts`, `type`, `allocated`, `available`

Replace the `load_data` function:

```python
def load_data():
    """Return (servers_df, internals_df, capacity_df) parsed from the JSONL log.

    servers_df columns:  cycle, ts, name, class, model,
                         rpm, throughput, avgInTok, avgOutTok,
                         itl, ttft, sloItl, sloTtft,
                         accelerator, replicas, cost
    internals_df columns: cycle, ts, model, acc, alpha, beta, gamma
    capacity_df columns:  cycle, ts, type, allocated, available
    """
    records = []
    try:
        with open(LOG_PATH) as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        records.append(json.loads(line))
                    except json.JSONDecodeError:
                        pass
    except FileNotFoundError:
        pass

    if not records:
        return pd.DataFrame(), pd.DataFrame(), pd.DataFrame()

    servers_df = pd.json_normalize(
        records,
        record_path="servers",
        meta=["cycle", "ts", "totalCost"],
        errors="ignore",
    )

    internals_df = pd.json_normalize(
        records,
        record_path="internals",
        meta=["cycle", "ts"],
        errors="ignore",
    )

    capacity_df = pd.json_normalize(
        records,
        record_path="capacity",
        meta=["cycle", "ts"],
        errors="ignore",
    )

    return servers_df, internals_df, capacity_df
```

- [ ] **Step 2: Add `fig_capacity()` function**

Add this function after `fig_controls()` and before `fig_internals()`:

```python
def fig_capacity(df):
    title = "Capacity: Accelerators Allocated vs Available"
    if df.empty or "type" not in df.columns:
        return _empty(title)

    palette = [
        "#636efa", "#ef553b", "#00cc96", "#ab63fa",
        "#ffa15a", "#19d3f3", "#ff6692", "#b6e880",
    ]
    acc_types = sorted(df["type"].unique())
    colors = {t: palette[i % len(palette)] for i, t in enumerate(acc_types)}

    fig = go.Figure()
    for acc_type in acc_types:
        s = df[df["type"] == acc_type].sort_values("cycle")
        color = colors[acc_type]

        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["allocated"],
            mode="lines+markers", name=f"{acc_type} allocated",
            line=dict(color=color), legendgroup=acc_type,
        ))
        fig.add_trace(go.Scatter(
            x=s["cycle"], y=s["available"],
            mode="lines", name=f"{acc_type} available",
            line=dict(color=color, dash="dash"),
            legendgroup=acc_type, showlegend=True,
        ))

    fig.update_layout(
        title=title, xaxis_title="Cycle",
        template="plotly_dark", paper_bgcolor="#1e1e1e", plot_bgcolor="#1e1e1e",
        legend=dict(orientation="h", yanchor="bottom", y=1.02),
        yaxis=dict(title="Accelerator Units", dtick=1, rangemode="tozero"),
    )
    return fig
```

- [ ] **Step 3: Update app layout to include the new panel**

Replace the layout's `children` list (the four `dcc.Graph` lines) with:

```python
        dcc.Graph(id="workload-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="performance-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="controls-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="capacity-panel", style={"marginBottom": "8px"}),
        dcc.Graph(id="internals-panel"),
```

- [ ] **Step 4: Update the callback**

Replace the `@app.callback` decorator outputs and the `update` function:

```python
@app.callback(
    [
        Output("workload-panel", "figure"),
        Output("performance-panel", "figure"),
        Output("controls-panel", "figure"),
        Output("capacity-panel", "figure"),
        Output("internals-panel", "figure"),
    ],
    [Input("tick", "n_intervals")],
)
def update(_n):
    servers_df, internals_df, capacity_df = load_data()
    return (
        fig_workload(servers_df),
        fig_performance(servers_df),
        fig_controls(servers_df),
        fig_capacity(capacity_df),
        fig_internals(internals_df, servers_df),
    )
```

- [ ] **Step 5: Verify dashboard runs without error**

```bash
cd <repo-root>/dashboard
INFERNO_CYCLE_LOG=../inferno-cycles.jsonl python dashboard.py
```

Expected: Dashboard starts at http://localhost:8050. Capacity panel shows empty (no data yet) without crashing. If a JSONL file exists with old records (no `capacity` field), the panel shows empty gracefully thanks to the `if df.empty or "type" not in df.columns` guard.

- [ ] **Step 6: Commit**

```bash
git add dashboard/dashboard.py
git commit -m "feat: add Capacity panel to dashboard (allocated vs available per accelerator)"
```

---

### Task 4: Auto-fetch JSONL from pod via kubectl

**Files:**
- Modify: `dashboard/dashboard.py`

Add an optional background thread that periodically runs `kubectl exec -n <namespace> deployment/inferno -c controller -- cat <path>` and writes the output to the local `LOG_PATH`. Controlled by env var `INFERNO_POD_SYNC=1`.

New env vars:
- `INFERNO_POD_SYNC` — set to `1` to enable (default: disabled)
- `INFERNO_NAMESPACE` — kubernetes namespace (default: `inferno`)
- `INFERNO_POD_SYNC_INTERVAL` — fetch interval in seconds (default: `10`)
- `INFERNO_CYCLE_LOG_POD_PATH` — path to log file inside the container (default: `inferno-cycles.jsonl`)

- [ ] **Step 1: Add pod sync configuration and thread to `dashboard/dashboard.py`**

After the existing env var block at the top (after `PORT = ...`), add:

```python
POD_SYNC = os.environ.get("INFERNO_POD_SYNC", "0") == "1"
NAMESPACE = os.environ.get("INFERNO_NAMESPACE", "inferno")
POD_SYNC_INTERVAL = int(os.environ.get("INFERNO_POD_SYNC_INTERVAL", "10"))
POD_LOG_PATH = os.environ.get("INFERNO_CYCLE_LOG_POD_PATH", "inferno-cycles.jsonl")
```

- [ ] **Step 2: Add the sync function and background thread startup**

Add this block after the env var declarations (before `# Data loading`):

```python
# ---------------------------------------------------------------------------
# Pod log sync (optional)
# ---------------------------------------------------------------------------

import subprocess
import threading


def _sync_pod_log():
    """Fetch the cycle log from the inferno controller pod via kubectl exec."""
    while True:
        try:
            result = subprocess.run(
                [
                    "kubectl", "exec",
                    "-n", NAMESPACE,
                    "deployment/inferno",
                    "-c", "controller",
                    "--",
                    "cat", POD_LOG_PATH,
                ],
                capture_output=True,
                timeout=30,
            )
            if result.returncode == 0 and result.stdout:
                with open(LOG_PATH, "wb") as f:
                    f.write(result.stdout)
        except Exception:
            pass  # silently retry on next interval
        threading.Event().wait(POD_SYNC_INTERVAL)


if POD_SYNC:
    _t = threading.Thread(target=_sync_pod_log, daemon=True)
    _t.start()
```

- [ ] **Step 3: Update the header div in the layout to show sync status**

Replace:
```python
        html.Div(
            f"Log: {LOG_PATH}  |  Refresh: {REFRESH_MS}ms",
            style={"color": "#888", "fontSize": "12px", "marginBottom": "16px"},
        ),
```

With:
```python
        html.Div(
            f"Log: {LOG_PATH}  |  Refresh: {REFRESH_MS}ms"
            + (f"  |  Pod sync: every {POD_SYNC_INTERVAL}s from {NAMESPACE}/inferno" if POD_SYNC else ""),
            style={"color": "#888", "fontSize": "12px", "marginBottom": "16px"},
        ),
```

- [ ] **Step 4: Verify pod sync works**

With the cluster running, test:

```bash
cd <repo-root>/dashboard
INFERNO_POD_SYNC=1 INFERNO_CYCLE_LOG=/tmp/inferno-cycles.jsonl python dashboard.py
```

Expected:
- Dashboard starts.
- Within 10 seconds, `/tmp/inferno-cycles.jsonl` is populated with content from the pod.
- The dashboard panels update automatically.

To confirm the fetch works without the dashboard:
```bash
kubectl exec -n inferno deployment/inferno -c controller -- cat inferno-cycles.jsonl | head -1 | python3 -m json.tool
```
Expected: pretty-printed JSON with `ts`, `cycle`, `servers`, `capacity`, etc.

- [ ] **Step 5: Update `dashboard/requirements.txt` if needed**

```bash
cd <repo-root>/dashboard
cat requirements.txt
```

`subprocess` and `threading` are stdlib — no new dependencies needed.

- [ ] **Step 6: Commit**

```bash
git add dashboard/dashboard.py
git commit -m "feat: auto-fetch cycle log from inferno pod via kubectl exec"
```

---

### Task 5: Build and deploy updated controller image

**Files:**
- `Dockerfile` (no change, just rebuild)

The Go changes (Tasks 1–2) require rebuilding the `inferno-loop` image and reloading into kind.

- [ ] **Step 1: Build the updated image**

```bash
cd <repo-root>
docker build -t quay.io/atantawi/inferno-loop:latest .
```

Expected: `Successfully tagged quay.io/atantawi/inferno-loop:latest`

- [ ] **Step 2: Load into kind**

```bash
kind load docker-image quay.io/atantawi/inferno-loop:latest --name kind-cluster
```

- [ ] **Step 3: Restart inferno deployment**

```bash
kubectl rollout restart deployment/inferno -n inferno
kubectl rollout status deployment/inferno -n inferno --timeout=120s
```

- [ ] **Step 4: Verify capacity data appears in the log**

```bash
sleep 120  # wait for EKF warm-up + a full cycle
kubectl exec -n inferno deployment/inferno -c controller -- cat inferno-cycles.jsonl | tail -1 | python3 -m json.tool | grep -A 10 '"capacity"'
```

Expected output like:
```json
"capacity": [
    {
        "type": "H100",
        "allocated": 3,
        "available": 8
    }
],
```

- [ ] **Step 5: Commit**

No code change — this is an operational step. Skip commit.

---

## Self-Review

**Spec coverage:**
1. ✅ Capacity panel after Controls — Task 3
2. ✅ Solid line = allocated, dashed = available — Task 3, Step 2
3. ✅ Only show allocated accelerators — `fig_capacity` only plots types present in the data; `builder.go` only includes types with `allocated > 0`
4. ✅ Data from optimizer output — Tasks 1–2: sourced from `allocSolution.Spec` + `Spec.Capacity`
5. ✅ Auto-fetch from pod — Task 4
6. ✅ Periodic fetch (not manual copy) — Task 4, background thread

**Placeholder scan:** No placeholders found.

**Type consistency:**
- `AcceleratorCapacityRecord.Type/Allocated/Available` defined in Task 1, used in Task 2 (`builder.go`) and Task 3 (`capacity_df` columns `type`, `allocated`, `available` match the JSON field names).
- `load_data()` returns 3-tuple in Task 3; `update()` callback unpacks 3-tuple — consistent.
- `capacity_df` guard `"type" not in df.columns` handles old JSONL records without `capacity` field.
