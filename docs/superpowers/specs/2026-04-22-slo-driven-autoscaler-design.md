# SLO-Driven Autoscaler Engine for inference-sim

**Date:** 2026-04-22  
**Status:** Approved  
**Repos:** [inference-sim](https://github.com/inference-sim/inference-sim) (target), [llm-inferno/control-loop](https://github.com/llm-inferno/control-loop) (source of components)

---

## Context

inference-sim has a pluggable autoscaler pipeline: `Collector → Analyzer → Engine → Actuator`. The current threshold-based `V2SaturationAnalyzer` + `GreedyEngine` scales based on KV-cache utilization. The goal is to add an SLO-driven, model-aware, self-tuning alternative by porting the tuner (EKF-based parameter learning) and queue analyzer (capacity prediction at target latency) from llm-inferno.

The result is a new `SLODrivenAnalyzer` and `SLOEngine` that can be dropped into the existing pipeline without changing any existing interfaces.

---

## Architecture

The pipeline structure is unchanged. Two new structs implement the existing `Analyzer` and `Engine` interfaces:

```
Collector → SLODrivenAnalyzer → SLOEngine → Actuator
             (new, Analyzer)     (new, Engine)
```

All existing components (`V2SaturationAnalyzer`, `GreedyEngine`, `UnlimitedEngine`, `DefaultCollector`, `DirectActuator`) are untouched and continue to work.

---

## Components

### SLODrivenAnalyzer (implements `Analyzer`)

**Purpose:** Learns per-model performance parameters (Alpha/Beta/Gamma) from observed latency metrics using EKF, then uses queue-analysis to compute max sustainable RPS per replica at target SLO.

**State:**
```go
type SLODrivenAnalyzer struct {
    config     SLOAnalyzerConfig
    modelState map[string]*perModelState  // keyed by ModelID
}

type perModelState struct {
    ekf        *tuner.EKFFilter  // from model-tuner library
    warmUpLeft int               // counts down from WarmUpCycles to 0
}

type SLOAnalyzerConfig struct {
    SLOByModel   map[string]ModelSLOConfig
    WarmUpCycles int  // default: 5; cycles before EKF output is trusted
}

type ModelSLOConfig struct {
    ITLTargetMs   float64
    TTFTTargetMs  float64
    AvgInTokens   float64
    AvgOutTokens  float64
}
```

**`Analyze(ModelSignals) AnalyzerResult` flow:**
1. For each replica in `ModelSignals.Replicas`: extract TTFT, ITL, and DispatchRate
2. Run EKF update via model-tuner library → refined Alpha, Beta, Gamma (observations always accumulate, even during warm-up)
3. If `warmUpLeft > 0`: decrement counter, return neutral result (RequiredCapacity=0, SpareCapacity=0)
4. Call queue-analysis with (Alpha, Beta, Gamma, ITL_slo, TTFT_slo, avgTokens) → `maxRPS_per_replica`
5. Compute:
   - `TotalSupply = maxRPS_per_replica × replicaCount` (RPS)
   - `TotalDemand = Σ DispatchRate across replicas` (RPS)
   - `RequiredCapacity = max(0, TotalDemand - TotalSupply)` (RPS)
   - `SpareCapacity` with N-1 safety check (same logic as V2SaturationAnalyzer)
6. Return `AnalyzerResult` with per-variant breakdown in `VariantCapacities`

New model IDs are initialized on first `Analyze()` call — no pre-registration required.

---

### SLOEngine (implements `Engine`)

**Purpose:** Translates RPS-unit supply/demand from `AnalyzerResult` into exact replica deltas, enabling faster convergence than the ±1 per-tick approach of existing engines.

**`Optimize([]AnalyzerResult, GPUInventory) []ScaleDecision` flow:**
```
for each model:
    maxRPS_per_replica = TotalSupply / replicaCount  (from VariantCapacities)
    if RequiredCapacity > 0:
        N = ceil(RequiredCapacity / maxRPS_per_replica)
        → ScaleDecision{ModelID, cheapest variant with N free slots, Delta: +N}
    if SpareCapacity >= maxRPS_per_replica:
        → ScaleDecision{ModelID, most expensive active variant, Delta: -1}
```

Scale-up: aggressive (+N, fast convergence under step load changes).  
Scale-down: conservative (−1 per tick, same as existing engines).  
GPU inventory: same constraint enforcement as `GreedyEngine`.

---

## Collector Extension

`ReplicaMetrics` in inference-sim already has `TTFT` and `DispatchRate` fields (marked "future: QueueingModelAnalyzer") but they are zero-filled. Two changes needed:

**`sim/routing.go`** — add to `RoutingSnapshot`:
```go
TTFT         float64  // microseconds; 0 if not yet available
ITL          float64  // inter-token latency, microseconds; 0 if not yet available
DispatchRate float64  // req/s completed by this instance
```

Note: whether the simulator already tracks ITL internally needs to be confirmed at implementation time. If not, it will need to be computed from per-request latency data before being surfaced in `RoutingSnapshot`.

**`sim/cluster/default_collector.go`** — populate these fields when mapping `RoutingSnapshot → ReplicaMetrics`. The existing zero-fill behavior is preserved when the simulator doesn't set them (backward-compatible).

---

## Configuration

Add to `DeploymentConfig` in `sim/cluster/deployment.go`:
```go
SLOByModel   map[string]ModelSLOConfig  // SLO targets per model ID
WarmUpCycles int                         // default: 5
```

The user selects the new components at startup by constructing `autoscalerPipeline` with `SLODrivenAnalyzer` and `SLOEngine` — the same way existing components are selected. No structural changes to autoscaler setup code.

---

## Error Handling & Edge Cases

| Condition | Behavior |
|---|---|
| `ModelSignals.Replicas` is empty | Skip EKF update, return neutral result; warm-up counter does not advance |
| Zero TTFT / DispatchRate for a replica | Exclude that replica from EKF observation; still count it in replicaCount for supply |
| EKF produces zero or negative maxRPS | Clamp to configurable minimum (e.g., 1 RPS) to prevent division-by-zero in engine |
| ModelID not in `SLOByModel` config | Log warning, return neutral result; no scaling until model is configured |
| GPUInventory exhausted on scale-up | Skip decision for that model; same as `GreedyEngine` behavior |
| Multi-variant models | maxRPS computed per variant (different perfParms per GPU type); VariantCapacities carries per-variant supply/demand |

---

## Files

### New files (inference-sim)

| File | Contents |
|---|---|
| `sim/cluster/slo_driven_analyzer.go` | `SLODrivenAnalyzer`, `perModelState`, `SLOAnalyzerConfig`, `ModelSLOConfig` |
| `sim/cluster/slo_engine.go` | `SLOEngine`, `scaleUpDecision()`, `scaleDownDecision()` |

### Modified files (inference-sim)

| File | Change |
|---|---|
| `sim/routing.go` | Add `TTFT`, `ITL`, `DispatchRate` to `RoutingSnapshot` |
| `sim/cluster/default_collector.go` | Populate `ReplicaMetrics.TTFT`, `ITL`, and `DispatchRate` from `RoutingSnapshot` |
| `sim/cluster/deployment.go` | Add `SLOByModel`, `WarmUpCycles` to `DeploymentConfig` |
| `go.mod` | Add `github.com/llm-inferno/model-tuner` (direct), promote `github.com/llm-inferno/queue-analysis` (direct) |

### Unchanged (inference-sim)

`Analyzer` interface, `Engine` interface, `autoscaler.go`, `DirectActuator`, `DefaultCollector` logic, `V2SaturationAnalyzer`, `GreedyEngine`, `UnlimitedEngine` — all existing components and interfaces are untouched.

---

## Verification

**Unit tests:**
- `SLODrivenAnalyzer`: synthetic `ModelSignals` with known TTFT/DispatchRate → assert EKF state updates and `AnalyzerResult` supply/demand values
- Warm-up: assert neutral result for first `WarmUpCycles` calls even with valid metrics
- `SLOEngine`: various RequiredCapacity/SpareCapacity inputs → assert correct Delta and variant selection

**Integration tests:**
- Step load increase with new components → assert scale-out within expected cycles with Delta > 1
- Compare convergence speed vs. `V2SaturationAnalyzer` + `GreedyEngine`
- Assert no `ScaleDecision` emitted during warm-up period

**Regression:**
- All existing sim tests with `V2SaturationAnalyzer` + `GreedyEngine` must pass unchanged

---

## Open Questions

- **Multi-service-class support:** Single SLO per model for now. Extension point: `SLOByModel` can be extended to a list of `ModelSLOConfig` entries (one per service class), with the analyzer tracking capacity per class and the engine distributing replicas across classes. Deferred.
- **model-tuner library API:** Confirmed to have a callable Go package (not REST-only). API details to be verified at implementation time.
