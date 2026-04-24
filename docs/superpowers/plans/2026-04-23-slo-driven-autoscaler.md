# SLO-Driven Autoscaler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `SLODrivenAnalyzer` and `SLOEngine` to inference-sim — an SLO-driven, model-based, self-tuning autoscaler that learns per-(model, variant) Alpha/Beta/Gamma via EKF and computes max sustainable RPS at target latency using queue-analysis.

**Architecture:** Two new structs implement the existing `Analyzer` and `Engine` interfaces in `sim/cluster/`. `SLODrivenAnalyzer` holds one EKF instance per **(ModelID, VariantSpec)** pair (via `github.com/llm-inferno/model-tuner`), runs a predict+update cycle each tick per variant, then calls `github.com/llm-inferno/queue-analysis` to convert the learned parameters into a per-variant max-RPS capacity figure. Variants still in warm-up are excluded from both supply and demand. `SLOEngine` converts the RPS-unit supply/demand gap into an exact replica delta (scale-up by N, scale-down by N). All existing components are untouched.

**Tech Stack:** Go, `github.com/llm-inferno/model-tuner v0.5.0` (EKF), `github.com/llm-inferno/queue-analysis v0.4.0` (queueing model), `gonum.org/v1/gonum` (transitive, via model-tuner).

**Working directory for all commands:** `/Users/tantawi/Projects/blis/inference-sim`

---

## File Map

| Action | Path | Responsibility |
|---|---|---|
| Modify | `sim/routing.go` | Add `TTFT`, `ITL`, `DispatchRate` fields to `RoutingSnapshot` |
| Modify | `sim/cluster/autoscaler.go` | Add `ITL` field to `ReplicaMetrics` |
| Modify | `sim/cluster/default_collector.go` | Populate `TTFT`, `ITL`, `DispatchRate` from `RoutingSnapshot` |
| Modify | `go.mod` | Add `model-tuner v0.5.0`, promote `queue-analysis v0.4.0` to direct |
| Create | `sim/cluster/slo_driven_analyzer.go` | `SLODrivenAnalyzer`, `modelVariantKey`, `perVariantState`, `SLOAnalyzerConfig`, `ModelSLOConfig`, helpers |
| Create | `sim/cluster/slo_driven_analyzer_test.go` | Unit tests for `SLODrivenAnalyzer` |
| Create | `sim/cluster/slo_engine.go` | `SLOEngine` |
| Create | `sim/cluster/slo_engine_test.go` | Unit tests for `SLOEngine` |
| Modify | `sim/cluster/deployment.go` | Add `SLOByModel`, `WarmUpCycles` to `DeploymentConfig` |

---

## Task 1: Add ITL to RoutingSnapshot and ReplicaMetrics; wire DefaultCollector

**Files:**
- Modify: `sim/routing.go`
- Modify: `sim/cluster/autoscaler.go`
- Modify: `sim/cluster/default_collector.go`

- [ ] **Step 1: Add `TTFT`, `ITL`, `DispatchRate` to `RoutingSnapshot` in `sim/routing.go`**

  Add three fields after `KvTokensInUse` (line 27):

  ```go
  // After the existing KvTokensInUse field:
  TTFT         float64 // μs; 0 if not yet available (populated by inference-sim when tracking decode latency)
  ITL          float64 // μs; 0 if not yet available (populated by inference-sim when tracking inter-token latency)
  DispatchRate float64 // req/s completed by this instance; 0 if not yet available
  ```

- [ ] **Step 2: Add `ITL` to `ReplicaMetrics` in `sim/cluster/autoscaler.go`**

  Add `ITL` between the existing `TTFT` and `DispatchRate` fields (lines 48–49):

  ```go
  TTFT         float64 // μs — zero until QueueingModelAnalyzer; Analyze() must guard against zero before dividing
  ITL          float64 // μs — zero until QueueingModelAnalyzer; Analyze() must guard against zero before dividing
  DispatchRate float64 // req/s — zero until QueueingModelAnalyzer; Analyze() must guard against zero before dividing
  ```

- [ ] **Step 3: Populate `TTFT`, `ITL`, `DispatchRate` in `default_collector.go`**

  In the `ReplicaMetrics` literal (lines 35–44), add the three new fields after the existing fields:

  ```go
  rm := ReplicaMetrics{
      InstanceID:            snap.ID,
      Variant:               NewVariantSpec(snap.GPUType, max(snap.TPDegree, 1)),
      KVUtilization:         snap.KVUtilization,
      QueueDepth:            snap.QueueDepth,
      InFlightCount:         snap.InFlightRequests,
      CostPerHour:           snap.CostPerHour,
      TotalKvCapacityTokens: snap.TotalKvCapacityTokens,
      KvTokensInUse:         snap.KvTokensInUse,
      TTFT:                  snap.TTFT,
      ITL:                   snap.ITL,
      DispatchRate:          snap.DispatchRate,
  }
  ```

- [ ] **Step 4: Run existing tests — must all pass**

  ```bash
  go test ./sim/... -count=1
  ```

  Expected: all existing tests pass. Adding zero-valued fields to existing structs is backward-compatible.

- [ ] **Step 5: Commit**

  ```bash
  git add sim/routing.go sim/cluster/autoscaler.go sim/cluster/default_collector.go
  git commit -m "feat: add TTFT, ITL, DispatchRate to RoutingSnapshot and ReplicaMetrics"
  ```

---

## Task 2: Add dependencies to go.mod

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add model-tuner and queue-analysis**

  ```bash
  go get github.com/llm-inferno/model-tuner@v0.5.0
  go get github.com/llm-inferno/queue-analysis@v0.4.0
  go mod tidy
  ```

  Expected: `go.mod` now contains `github.com/llm-inferno/model-tuner v0.5.0` and `github.com/llm-inferno/queue-analysis v0.4.0` as direct dependencies, plus `gonum.org/v1/gonum` (and other transitive deps) as indirect.

  > **Note:** If `go get` fails with a proxy error, the repos may require auth. Use local replace directives instead:
  > ```bash
  > go mod edit -replace github.com/llm-inferno/model-tuner=../../../llm-inferno/model-tuner
  > go mod edit -replace github.com/llm-inferno/queue-analysis=../../../llm-inferno/queue-analysis
  > go mod tidy
  > ```

  > **Note:** If `go build` fails with a Go version error (model-tuner uses `range N` syntax from Go 1.22), update the `go` directive:
  > ```bash
  > go mod edit -go 1.22
  > go mod tidy
  > ```

- [ ] **Step 2: Verify build still passes**

  ```bash
  go build ./...
  ```

  Expected: compiles cleanly. No new imports in `.go` files yet, so no errors.

- [ ] **Step 3: Commit**

  ```bash
  git add go.mod go.sum
  git commit -m "chore: add model-tuner v0.5.0 and queue-analysis v0.4.0 dependencies"
  ```

---

## Task 3: Define config types and SLODrivenAnalyzer skeleton + write failing tests

**Files:**
- Create: `sim/cluster/slo_driven_analyzer.go`
- Create: `sim/cluster/slo_driven_analyzer_test.go`

- [ ] **Step 1: Write the failing tests**

  Create `sim/cluster/slo_driven_analyzer_test.go`:

  ```go
  package cluster

  import (
  	"testing"

  	"github.com/stretchr/testify/assert"
  	"github.com/stretchr/testify/require"
  )

  // newTestSLOConfig returns a config with a single model "m1" and InitObs=2 (bootstrap ready after 2 observations).
  func newTestSLOConfig() SLOAnalyzerConfig {
  	return SLOAnalyzerConfig{
  		InitObs:    2,
  		UseSliding: true,
  		WindowSize: 10,
  		WarmUpCycles: 3,
  		SLOByModel: map[string]ModelSLOConfig{
  			"m1": {
  				ITLTargetMs:  10.0,
  				TTFTTargetMs: 50.0,
  				AvgInTokens:  512,
  				AvgOutTokens: 128,
  				MaxBatchSize: 32,
  			},
  		},
  	}
  }

  // testReplicas returns one replica with valid TTFT/ITL/DispatchRate for model "m1".
  func testReplicas() []ReplicaMetrics {
  	return []ReplicaMetrics{{
  		InstanceID:   "r1",
  		Variant:      NewVariantSpec("A100", 1),
  		TTFT:         40_000, // 40ms in μs
  		ITL:          8_000,  // 8ms in μs
  		DispatchRate: 5.0,    // req/s
  		InFlightCount: 4,
  		CostPerHour:  10.0,
  	}}
  }

  func TestSLODrivenAnalyzerName(t *testing.T) {
  	a := NewSLODrivenAnalyzer(newTestSLOConfig())
  	assert.Equal(t, "slo-driven", a.Name())
  }

  func TestSLODrivenAnalyzerEmptyReplicas(t *testing.T) {
  	a := NewSLODrivenAnalyzer(newTestSLOConfig())
  	result := a.Analyze(ModelSignals{ModelID: "m1"})
  	assert.Equal(t, "m1", result.ModelID)
  	assert.Zero(t, result.RequiredCapacity)
  	assert.Zero(t, result.SpareCapacity)
  	assert.Zero(t, result.TotalSupply)
  }

  func TestSLODrivenAnalyzerUnknownModel(t *testing.T) {
  	a := NewSLODrivenAnalyzer(newTestSLOConfig())
  	result := a.Analyze(ModelSignals{ModelID: "unknown", Replicas: testReplicas()})
  	assert.Zero(t, result.RequiredCapacity)
  	assert.Zero(t, result.SpareCapacity)
  }

  func TestSLODrivenAnalyzerWarmUp(t *testing.T) {
  	a := NewSLODrivenAnalyzer(newTestSLOConfig()) // InitObs=2
  	// Call 1: IE accumulating (count=1 < InitObs=2) — excluded
  	result := a.Analyze(ModelSignals{ModelID: "m1", Replicas: testReplicas()})
  	assert.Zero(t, result.RequiredCapacity, "cycle 1: IE accumulating, no supply yet")
  	assert.Zero(t, result.SpareCapacity, "cycle 1: IE accumulating, no supply yet")
  	assert.Zero(t, result.TotalSupply, "cycle 1: IE accumulating, no supply yet")
  	// Call 2: IE ready (count=2 == InitObs=2); SWE seeded and fit — included
  	result = a.Analyze(ModelSignals{ModelID: "m1", Replicas: testReplicas()})
  	assert.Greater(t, result.TotalSupply, 0.0, "supply should be positive after IE bootstrap")
  }

  func TestSLODrivenAnalyzerWarmUpDoesNotAdvanceOnEmptyReplicas(t *testing.T) {
  	a := NewSLODrivenAnalyzer(newTestSLOConfig()) // InitObs=2
  	// Three calls with empty replicas — IE counter must NOT advance
  	for i := 0; i < 3; i++ {
  		a.Analyze(ModelSignals{ModelID: "m1"})
  	}
  	// Real call 1: IE count=1 < InitObs=2 — excluded
  	result := a.Analyze(ModelSignals{ModelID: "m1", Replicas: testReplicas()})
  	assert.Zero(t, result.TotalSupply, "IE should not have advanced during empty-replica calls")
  	// Real call 2: IE count=2 == InitObs=2 — SWE fit — included
  	result = a.Analyze(ModelSignals{ModelID: "m1", Replicas: testReplicas()})
  	assert.Greater(t, result.TotalSupply, 0.0)
  }

  func TestSLODrivenAnalyzerVariantCapacitiesInvariant(t *testing.T) {
  	a := NewSLODrivenAnalyzer(newTestSLOConfig())
  	// Exhaust warm-up
  	for i := 0; i < 3; i++ {
  		a.Analyze(ModelSignals{ModelID: "m1", Replicas: testReplicas()})
  	}
  	result := a.Analyze(ModelSignals{ModelID: "m1", Replicas: testReplicas()})
  	// sum(vc.Supply) must equal TotalSupply
  	var sumSupply float64
  	for _, vc := range result.VariantCapacities {
  		sumSupply += vc.Supply
  	}
  	assert.InDelta(t, result.TotalSupply, sumSupply, 1e-9, "sum(VariantCapacity.Supply) must equal TotalSupply")
  	// mutual exclusivity: RequiredCapacity and SpareCapacity cannot both be positive
  	assert.False(t, result.RequiredCapacity > 0 && result.SpareCapacity > 0,
  		"RequiredCapacity and SpareCapacity must be mutually exclusive")
  }

  func TestSLODrivenAnalyzerResultModelID(t *testing.T) {
  	a := NewSLODrivenAnalyzer(newTestSLOConfig())
  	result := a.Analyze(ModelSignals{ModelID: "m1", Replicas: testReplicas()})
  	require.Equal(t, "m1", result.ModelID)
  }
  ```

- [ ] **Step 2: Create the minimal skeleton so tests compile**

  Create `sim/cluster/slo_driven_analyzer.go`:

  ```go
  // slo_driven_analyzer.go implements SLODrivenAnalyzer — an Analyzer that learns
  // per-(model, variant) Alpha/Beta/Gamma via a three-phase NM bootstrap → SWE → EKF pipeline.
  package cluster

  import (
  	"github.com/llm-inferno/model-tuner/pkg/estimator"
  	tunercore "github.com/llm-inferno/model-tuner/pkg/core"
  )

  // ModelSLOConfig holds SLO targets and model parameters for one model.
  type ModelSLOConfig struct {
  	ITLTargetMs  float64 // inter-token latency SLO, milliseconds
  	TTFTTargetMs float64 // time-to-first-token SLO, milliseconds
  	AvgInTokens  float64 // average input tokens per request
  	AvgOutTokens float64 // average output tokens per request
  	MaxBatchSize int     // maximum concurrent requests
  }

  // SLOAnalyzerConfig configures SLODrivenAnalyzer.
  // Mirrors TunerService config fields; defaults applied in NewSLODrivenAnalyzer.
  type SLOAnalyzerConfig struct {
  	SLOByModel        map[string]ModelSLOConfig // keyed by ModelID; absent models return neutral result
  	InitObs           int                       // IE bootstrap observations before params available (default: 5)
  	UseSliding        bool                      // use SlidingWindowEstimator as primary (default: true)
  	WindowSize        int                       // SWE window capacity (default: 20)
  	ResidualThreshold float64                   // SWE outlier rejection threshold (default: 0.3; 0 = disabled)
  	InitFitThreshold  float64                   // max NM func value for SW quality gate; 0 = no gate (default: 0)
  	WarmUpCycles      int                       // EKF NIS gate disable count (default: 5; EKF path only)
  }

  // modelVariantKey identifies one (model, variant) estimation pipeline instance.
  type modelVariantKey struct {
  	ModelID string
  	Variant VariantSpec
  }

  type perVariantState struct {
  	ie          *estimator.InitEstimator          // Phase 1: NM bootstrap
  	swe         *estimator.SlidingWindowEstimator // Phase 2a: sliding-window NM (nil until ie.IsReady())
  	tuner       *tunercore.Tuner                  // Phase 2b: EKF (nil unless UseSliding=false or ekfFallback)
  	ekfFallback bool                              // SW init fit quality was too poor; use EKF instead
  	ekfUpdates  int                               // accepted EKF updates (for NIS gate)
  	alpha       float64                           // current best estimate; 0 until first successful fit
  	beta        float64
  	gamma       float64
  }

  // SLODrivenAnalyzer implements Analyzer using a three-phase parameter estimation pipeline
  // (NM bootstrap → sliding-window NM → EKF fallback) and queue-analysis capacity prediction.
  type SLODrivenAnalyzer struct {
  	config       SLOAnalyzerConfig
  	variantState map[modelVariantKey]*perVariantState
  }

  // NewSLODrivenAnalyzer constructs an SLODrivenAnalyzer with defaults for unset config fields.
  func NewSLODrivenAnalyzer(cfg SLOAnalyzerConfig) *SLODrivenAnalyzer {
  	if cfg.InitObs == 0 {
  		cfg.InitObs = 5
  	}
  	if cfg.WindowSize == 0 {
  		cfg.WindowSize = 20
  	}
  	if cfg.ResidualThreshold == 0 {
  		cfg.ResidualThreshold = 0.3
  	}
  	if cfg.WarmUpCycles == 0 {
  		cfg.WarmUpCycles = 5
  	}
  	// UseSliding defaults to true; zero value (false) treated as true here.
  	cfg.UseSliding = true
  	return &SLODrivenAnalyzer{
  		config:       cfg,
  		variantState: make(map[modelVariantKey]*perVariantState),
  	}
  }

  // Name returns the analyzer identifier for observability.
  func (a *SLODrivenAnalyzer) Name() string { return "slo-driven" }

  // Analyze is a placeholder — implemented in Task 4.
  func (a *SLODrivenAnalyzer) Analyze(metrics ModelSignals) AnalyzerResult {
  	return AnalyzerResult{ModelID: metrics.ModelID}
  }
  ```

- [ ] **Step 3: Run the failing tests**

  ```bash
  go test ./sim/cluster/ -run TestSLODriven -v -count=1
  ```

  Expected: `TestSLODrivenAnalyzerName` PASSES (Name() is already implemented). All other tests FAIL because `Analyze()` returns a zero result (no estimation pipeline running).

  Confirm at least one test fails before proceeding.

- [ ] **Step 4: Commit the skeleton**

  ```bash
  git add sim/cluster/slo_driven_analyzer.go sim/cluster/slo_driven_analyzer_test.go
  git commit -m "test: add SLODrivenAnalyzer test suite with failing cases"
  ```

---

## Task 4: Implement SLODrivenAnalyzer.Analyze()

**Files:**
- Modify: `sim/cluster/slo_driven_analyzer.go`

- [ ] **Step 1: Replace the full `slo_driven_analyzer.go` with the complete implementation**

  The file already exists — replace it entirely:

  ```go
  // slo_driven_analyzer.go implements SLODrivenAnalyzer — an Analyzer that learns
  // per-(model, variant) Alpha/Beta/Gamma via a three-phase NM bootstrap → SWE → EKF pipeline.
  package cluster

  import (
  	"fmt"
  	"math"
  	"sort"

  	"github.com/llm-inferno/model-tuner/pkg/estimator"
  	tunerconfig "github.com/llm-inferno/model-tuner/pkg/config"
  	tunercore "github.com/llm-inferno/model-tuner/pkg/core"
  	qanalyzer "github.com/llm-inferno/queue-analysis/pkg/analyzer"
  	"github.com/sirupsen/logrus"
  )

  // ModelSLOConfig holds SLO targets and model parameters for one model.
  type ModelSLOConfig struct {
  	ITLTargetMs  float64 // inter-token latency SLO, milliseconds
  	TTFTTargetMs float64 // time-to-first-token SLO, milliseconds
  	AvgInTokens  float64 // average input tokens per request
  	AvgOutTokens float64 // average output tokens per request
  	MaxBatchSize int     // maximum concurrent requests
  }

  // SLOAnalyzerConfig configures SLODrivenAnalyzer.
  // Mirrors TunerService config fields; defaults applied in NewSLODrivenAnalyzer.
  type SLOAnalyzerConfig struct {
  	SLOByModel        map[string]ModelSLOConfig
  	InitObs           int     // IE bootstrap observations (default: 5)
  	UseSliding        bool    // use SlidingWindowEstimator as primary (default: true)
  	WindowSize        int     // SWE window capacity (default: 20)
  	ResidualThreshold float64 // SWE outlier rejection (default: 0.3; 0 = disabled)
  	InitFitThreshold  float64 // max NM func value for SW quality gate; 0 = no gate (default: 0)
  	WarmUpCycles      int     // EKF NIS gate disable count (default: 5; EKF path only)
  }

  // modelVariantKey identifies one (model, variant) estimation pipeline instance.
  type modelVariantKey struct {
  	ModelID string
  	Variant VariantSpec
  }

  type perVariantState struct {
  	ie          *estimator.InitEstimator
  	swe         *estimator.SlidingWindowEstimator // nil until ie.IsReady()
  	tuner       *tunercore.Tuner                  // nil unless UseSliding=false or ekfFallback
  	ekfFallback bool
  	ekfUpdates  int
  	alpha       float64
  	beta        float64
  	gamma       float64
  }

  // SLODrivenAnalyzer implements Analyzer using a three-phase estimation pipeline
  // (NM bootstrap → SWE → EKF fallback) and queue-analysis capacity prediction.
  type SLODrivenAnalyzer struct {
  	config       SLOAnalyzerConfig
  	variantState map[modelVariantKey]*perVariantState
  }

  // NewSLODrivenAnalyzer constructs an SLODrivenAnalyzer with defaults for unset config fields.
  func NewSLODrivenAnalyzer(cfg SLOAnalyzerConfig) *SLODrivenAnalyzer {
  	if cfg.InitObs == 0 {
  		cfg.InitObs = 5
  	}
  	if cfg.WindowSize == 0 {
  		cfg.WindowSize = 20
  	}
  	if cfg.ResidualThreshold == 0 {
  		cfg.ResidualThreshold = 0.3
  	}
  	if cfg.WarmUpCycles == 0 {
  		cfg.WarmUpCycles = 5
  	}
  	cfg.UseSliding = true
  	return &SLODrivenAnalyzer{
  		config:       cfg,
  		variantState: make(map[modelVariantKey]*perVariantState),
  	}
  }

  func (a *SLODrivenAnalyzer) Name() string { return "slo-driven" }

  // Analyze runs the three-phase estimation pipeline per observed variant, then returns
  // RPS-unit supply/demand. Variants with alpha==0 (pipeline not yet warmed up) are excluded.
  // Returns a neutral result for unknown models or empty replicas.
  func (a *SLODrivenAnalyzer) Analyze(metrics ModelSignals) AnalyzerResult {
  	result := AnalyzerResult{ModelID: metrics.ModelID}

  	sloConfig, ok := a.config.SLOByModel[metrics.ModelID]
  	if !ok {
  		logrus.Warnf("[slo-analyzer] model %q not in SLOByModel config — returning neutral", metrics.ModelID)
  		return result
  	}
  	if len(metrics.Replicas) == 0 {
  		return result // IE counters do not advance
  	}

  	// Group replicas by variant.
  	byVariant := make(map[VariantSpec][]ReplicaMetrics)
  	for _, r := range metrics.Replicas {
  		byVariant[r.Variant] = append(byVariant[r.Variant], r)
  	}

  	// For each variant: feed observations into the estimation pipeline, then include in
  	// result only once alpha > 0 (pipeline has produced at least one successful fit).
  	vcs := make([]VariantCapacity, 0, len(byVariant))
  	for variant, replicas := range byVariant {
  		key := modelVariantKey{ModelID: metrics.ModelID, Variant: variant}
  		state := a.getOrInitVariantState(key)

  		for _, r := range replicas {
  			if r.DispatchRate <= 0 || r.ITL <= 0 {
  				continue
  			}
  			env := tunercore.NewEnvironmentPrefillDecode(
  				float32(r.DispatchRate*60), // req/s → req/min (Lambda is in RPM)
  				float32(r.InFlightCount),
  				0.0, // avgQueueTime not directly observable; 0 is safe
  				sloConfig.MaxBatchSize,
  				float32(sloConfig.AvgInTokens),
  				float32(sloConfig.AvgOutTokens),
  				float32(r.TTFT/1000), // μs → ms
  				float32(r.ITL/1000),  // μs → ms
  			)
  			a.updateVariantParams(state, env, sloConfig)
  		}

  		// Exclude variants where the pipeline has not yet produced estimates.
  		if state.alpha <= 0 || state.beta <= 0 || state.gamma <= 0 {
  			continue
  		}

  		maxRPS := computeMaxRPS(state.alpha, state.beta, state.gamma, sloConfig)
  		const minRPS = 1.0
  		if maxRPS < minRPS {
  			maxRPS = minRPS
  		}

  		var demand float64
  		for _, r := range replicas {
  			demand += r.DispatchRate
  		}
  		vcs = append(vcs, VariantCapacity{
  			Variant:        variant,
  			Supply:         maxRPS * float64(len(replicas)),
  			Demand:         demand,
  			ReplicaCount:   len(replicas),
  			CostPerReplica: replicas[0].CostPerHour,
  		})
  	}

  	if len(vcs) == 0 {
  		return result
  	}

  	// Sort by cost ascending for determinism.
  	sort.Slice(vcs, func(i, j int) bool {
  		if vcs[i].CostPerReplica != vcs[j].CostPerReplica {
  			return vcs[i].CostPerReplica < vcs[j].CostPerReplica
  		}
  		if vcs[i].Variant.GPUType != vcs[j].Variant.GPUType {
  			return vcs[i].Variant.GPUType < vcs[j].Variant.GPUType
  		}
  		return vcs[i].Variant.TPDegree < vcs[j].Variant.TPDegree
  	})
  	result.VariantCapacities = vcs

  	for _, vc := range vcs {
  		result.TotalSupply += vc.Supply
  		result.TotalDemand += vc.Demand
  	}
  	if result.TotalSupply > 0 {
  		result.Utilization = result.TotalDemand / result.TotalSupply
  	}

  	// Scale-up signal.
  	if result.TotalDemand > result.TotalSupply {
  		result.RequiredCapacity = result.TotalDemand - result.TotalSupply
  		return result
  	}

  	// Scale-down signal with N-1 redistribution check (same logic as V2SaturationAnalyzer).
  	initReplicas := 0
  	for _, vc := range vcs {
  		initReplicas += vc.ReplicaCount
  	}
  	if initReplicas <= 1 {
  		return result
  	}
  	maxPerReplicaSupply := 0.0
  	for _, vc := range vcs {
  		if vc.ReplicaCount > 0 {
  			perReplica := vc.Supply / float64(vc.ReplicaCount)
  			if perReplica > maxPerReplicaSupply {
  				maxPerReplicaSupply = perReplica
  			}
  		}
  	}
  	if result.TotalSupply-maxPerReplicaSupply > result.TotalDemand {
  		result.SpareCapacity = result.TotalSupply - result.TotalDemand
  	}
  	return result
  }

  // getOrInitVariantState returns the per-(model, variant) state, creating it on first call.
  // Only the InitEstimator is created here; SWE and tuner are created lazily in updateVariantParams.
  func (a *SLODrivenAnalyzer) getOrInitVariantState(key modelVariantKey) *perVariantState {
  	if state, ok := a.variantState[key]; ok {
  		return state
  	}
  	state := &perVariantState{
  		ie: estimator.NewInitEstimator(a.config.InitObs, true),
  	}
  	a.variantState[key] = state
  	return state
  }

  // updateVariantParams feeds one environment observation into the three-phase estimation pipeline
  // and updates state.alpha/beta/gamma on each successful estimation.
  //
  // Phase 1 (InitEstimator): accumulates observations until InitObs are collected.
  // Phase 2a (SlidingWindowEstimator): primary path when UseSliding=true. Created once IE is ready,
  //   seeded from IE observations. Every subsequent call adds one observation and re-fits.
  //   If init fit quality is poor (InitFitThreshold > 0 exceeded), falls back to Phase 2b.
  // Phase 2b (EKF): activated when UseSliding=false or ekfFallback=true.
  //   Tuner created on first call, seeded with IE.Fit() result. NIS gate disabled for first WarmUpCycles updates.
  func (a *SLODrivenAnalyzer) updateVariantParams(state *perVariantState, env *tunercore.EnvironmentPrefillDecode, sloConfig ModelSLOConfig) {
  	// Phase 1: feed InitEstimator.
  	state.ie.AddObservation(env)
  	if !state.ie.IsReady() {
  		return
  	}

  	// Phase 2a: SlidingWindowEstimator (primary path).
  	if a.config.UseSliding && !state.ekfFallback {
  		if state.swe == nil {
  			// First call after IE ready: create SWE seeded from IE observations.
  			// env is already included via SeedFromEstimator — do NOT call AddObservation here.
  			state.swe = estimator.NewSlidingWindowEstimator(a.config.WindowSize, a.config.InitObs, a.config.ResidualThreshold)
  			state.swe.SeedFromEstimator(state.ie)
  			if fitted, err := state.ie.Fit(); err == nil {
  				fv := state.ie.LastFitFuncValue()
  				if a.config.InitFitThreshold > 0 && fv > a.config.InitFitThreshold {
  					logrus.Warnf("[slo-analyzer] SW init fit quality poor (funcValue=%.3f > threshold=%.3f) — falling back to EKF",
  						fv, a.config.InitFitThreshold)
  					state.ekfFallback = true
  				} else {
  					state.swe.SeedLastFit(fitted)
  				}
  			}
  		} else {
  			// Subsequent calls: add the new observation.
  			state.swe.AddObservation(env)
  		}

  		if !state.ekfFallback {
  			if !state.swe.IsReady() {
  				return
  			}
  			fitted, err := state.swe.Fit()
  			if err != nil {
  				logrus.Debugf("[slo-analyzer] SWE fit error: %v", err)
  				return
  			}
  			state.alpha, state.beta, state.gamma = fitted[0], fitted[1], fitted[2]
  			return
  		}
  	}

  	// Phase 2b: EKF (UseSliding=false or ekfFallback=true).
  	if state.tuner == nil {
  		fitInitState, _ := state.ie.Fit()
  		cfg := buildEKFConfig(fitInitState)
  		tuner, _, err := tunercore.SetupTunerForQueueingModel(cfg, env, "prefill-decode")
  		if err != nil {
  			logrus.Errorf("[slo-analyzer] EKF init failed: %v", err)
  			return
  		}
  		state.tuner = tuner
  	}
  	skipNIS := state.ekfUpdates < a.config.WarmUpCycles
  	results, err := state.tuner.RunWithValidation(env, skipNIS)
  	if err != nil || results == nil || results.ValidationFailed {
  		logrus.Debugf("[slo-analyzer] EKF update skipped: err=%v validationFailed=%v", err, results != nil && results.ValidationFailed)
  		return
  	}
  	state.alpha = float64(results.ServiceParms.Alpha)
  	state.beta = float64(results.ServiceParms.Beta)
  	state.gamma = float64(results.ServiceParms.Gamma)
  	state.ekfUpdates++
  }

  // computeMaxRPS asks queue-analysis for the max request rate (req/s) that satisfies both
  // the TTFT and ITL SLO targets, given the current EKF-estimated model parameters.
  // Returns 0 on error (caller clamps to minRPS).
  func computeMaxRPS(alpha, beta, gamma float64, sloConfig ModelSLOConfig) float64 {
  	cfg := &qanalyzer.Configuration{
  		MaxBatchSize: sloConfig.MaxBatchSize,
  		MaxNumTokens: qanalyzer.DefaultMaxNumTokens,
  		MaxQueueSize: 0,
  		ServiceParms: &qanalyzer.ServiceParms{
  			Alpha: float32(alpha),
  			Beta:  float32(beta),
  			Gamma: float32(gamma),
  		},
  	}
  	req := &qanalyzer.RequestSize{
  		AvgInputTokens:  float32(sloConfig.AvgInTokens),
  		AvgOutputTokens: float32(sloConfig.AvgOutTokens),
  	}
  	qa, err := qanalyzer.NewLLMQueueAnalyzer(cfg, req)
  	if err != nil {
  		logrus.Debugf("[slo-analyzer] computeMaxRPS: NewLLMQueueAnalyzer error: %v", err)
  		return 0
  	}
  	targetRate, _, _, err := qa.Size(&qanalyzer.TargetPerf{
  		TargetTTFT: float32(sloConfig.TTFTTargetMs),
  		TargetITL:  float32(sloConfig.ITLTargetMs),
  	})
  	if err != nil {
  		logrus.Debugf("[slo-analyzer] computeMaxRPS: Size error: %v", err)
  		return 0
  	}
  	return float64(min(targetRate.RateTargetTTFT, targetRate.RateTargetITL))
  }

  // buildEKFConfig builds a model-tuner ConfigData seeded with the NM fit result.
  // fitInitState is [alpha, beta, gamma] from InitEstimator.Fit(); nil or non-positive values
  // fall back to prefill-decode reference defaults (alpha=7.47, beta=0.044, gamma=3.37e-5).
  func buildEKFConfig(fitInitState []float64) *tunerconfig.ConfigData {
  	a, b, g := 7.47, 0.044, 3.37e-5
  	if len(fitInitState) == 3 && fitInitState[0] > 0 && fitInitState[1] > 0 && fitInitState[2] > 0 {
  		a, b, g = fitInitState[0], fitInitState[1], fitInitState[2]
  	}
  	const factor = 10.0
  	const eps = 1e-9
  	return &tunerconfig.ConfigData{
  		FilterData: tunerconfig.FilterData{
  			GammaFactor: 1.0,
  			ErrorLevel:  0.05,
  			TPercentile: 1.96,
  		},
  		ModelData: tunerconfig.ModelData{
  			InitState:            []float64{a, b, g},
  			PercentChange:        []float64{0.1, 0.1, 0.1},
  			BoundedState:         true,
  			MinState:             []float64{math.Max(a/factor, eps), math.Max(b/factor, eps), math.Max(g/factor, eps)},
  			MaxState:             []float64{a * factor, b * factor, g * factor},
  			ExpectedObservations: []float64{20.0, 20.0},
  		},
  	}
  }
  ```

- [ ] **Step 2: Run the tests — all should pass**

  ```bash
  go test ./sim/cluster/ -run TestSLODriven -v -count=1
  ```

  Expected: all `TestSLODriven*` tests PASS. If `TestSLODrivenAnalyzerWarmUp` fails with "supply should be positive after IE bootstrap", check that `SlidingWindowEstimator.Fit()` succeeds on 2 synthetic observations with TTFT=40ms, ITL=8ms — Nelder-Mead should converge to positive alpha/beta/gamma that produce a non-zero maxRPS at ITLTarget=10ms, TTFTTarget=50ms. If `computeMaxRPS` returns 0, check for an error from `qa.Size()` by adding a temporary log statement.

- [ ] **Step 3: Run full cluster test suite to verify no regressions**

  ```bash
  go test ./sim/cluster/ -count=1
  ```

  Expected: all tests pass.

- [ ] **Step 4: Commit**

  ```bash
  git add sim/cluster/slo_driven_analyzer.go
  git commit -m "feat: implement SLODrivenAnalyzer with EKF tuning and queue-analysis capacity"
  ```

---

## Task 5: Write failing tests for SLOEngine

**Files:**
- Create: `sim/cluster/slo_engine_test.go`

- [ ] **Step 1: Write the failing tests**

  Create `sim/cluster/slo_engine_test.go`:

  ```go
  package cluster

  import (
  	"testing"

  	"github.com/stretchr/testify/assert"
  	"github.com/stretchr/testify/require"
  )

  func TestSLOEngineScaleUpN(t *testing.T) {
  	e := &SLOEngine{}
  	// 1 replica with Supply=10 req/s. RequiredCapacity=15 → ceil(15/10)=2
  	results := []AnalyzerResult{{
  		ModelID:          "m1",
  		RequiredCapacity: 15.0,
  		VariantCapacities: []VariantCapacity{{
  			Variant:        NewVariantSpec("A100", 1),
  			Supply:         10.0,
  			ReplicaCount:   1,
  			CostPerReplica: 5.0,
  		}},
  	}}
  	inv := GPUInventory{byVariant: map[VariantSpec]int{NewVariantSpec("A100", 1): 10}}
  	decisions := e.Optimize(results, inv)
  	require.Len(t, decisions, 1)
  	assert.Equal(t, "m1", decisions[0].ModelID)
  	assert.Equal(t, NewVariantSpec("A100", 1), decisions[0].Variant)
  	assert.Equal(t, 2, decisions[0].Delta)
  }

  func TestSLOEngineScaleUpPicksCheapestVariant(t *testing.T) {
  	e := &SLOEngine{}
  	// Two variants; cheapest (cost=3) should be picked for scale-up.
  	results := []AnalyzerResult{{
  		ModelID:          "m1",
  		RequiredCapacity: 5.0,
  		VariantCapacities: []VariantCapacity{
  			{Variant: NewVariantSpec("A100", 1), Supply: 10.0, ReplicaCount: 1, CostPerReplica: 10.0},
  			{Variant: NewVariantSpec("T4", 1), Supply: 8.0, ReplicaCount: 1, CostPerReplica: 3.0},
  		},
  	}}
  	inv := GPUInventory{byVariant: map[VariantSpec]int{
  		NewVariantSpec("A100", 1): 5,
  		NewVariantSpec("T4", 1):   5,
  	}}
  	decisions := e.Optimize(results, inv)
  	require.Len(t, decisions, 1)
  	assert.Equal(t, NewVariantSpec("T4", 1), decisions[0].Variant)
  }

  func TestSLOEngineScaleUpInventoryExhausted(t *testing.T) {
  	e := &SLOEngine{}
  	results := []AnalyzerResult{{
  		ModelID:          "m1",
  		RequiredCapacity: 15.0,
  		VariantCapacities: []VariantCapacity{{
  			Variant:        NewVariantSpec("A100", 1),
  			Supply:         10.0,
  			ReplicaCount:   1,
  			CostPerReplica: 5.0,
  		}},
  	}}
  	// Zero free slots — no scale-up possible
  	inv := GPUInventory{byVariant: map[VariantSpec]int{NewVariantSpec("A100", 1): 0}}
  	decisions := e.Optimize(results, inv)
  	assert.Empty(t, decisions)
  }

  func TestSLOEngineScaleUpFallbackOnNoActiveReplicas(t *testing.T) {
  	e := &SLOEngine{}
  	// Variant has no active replicas — can't compute maxRPS; should fall back to Delta=1
  	results := []AnalyzerResult{{
  		ModelID:          "m1",
  		RequiredCapacity: 15.0,
  		VariantCapacities: []VariantCapacity{{
  			Variant:        NewVariantSpec("A100", 1),
  			Supply:         0.0,
  			ReplicaCount:   0,
  			CostPerReplica: 5.0,
  		}},
  	}}
  	inv := GPUInventory{byVariant: map[VariantSpec]int{NewVariantSpec("A100", 1): 10}}
  	decisions := e.Optimize(results, inv)
  	require.Len(t, decisions, 1)
  	assert.Equal(t, 1, decisions[0].Delta)
  }

  // SpareCapacity=12, Supply=20, ReplicaCount=2 → perReplica=10, N=floor(12/10)=1
  func TestSLOEngineScaleDownDelta(t *testing.T) {
  	e := &SLOEngine{}
  	results := []AnalyzerResult{{
  		ModelID:       "m1",
  		SpareCapacity: 12.0,
  		VariantCapacities: []VariantCapacity{{
  			Variant:        NewVariantSpec("A100", 1),
  			Supply:         20.0,
  			ReplicaCount:   2,
  			CostPerReplica: 5.0,
  		}},
  	}}
  	decisions := e.Optimize(results, GPUInventory{byVariant: map[VariantSpec]int{}})
  	require.Len(t, decisions, 1)
  	assert.Equal(t, -1, decisions[0].Delta)
  }

  // SpareCapacity=25, Supply=30, ReplicaCount=3 → perReplica=10, N=floor(25/10)=2
  func TestSLOEngineScaleDownAggressiveN(t *testing.T) {
  	e := &SLOEngine{}
  	results := []AnalyzerResult{{
  		ModelID:       "m1",
  		SpareCapacity: 25.0,
  		VariantCapacities: []VariantCapacity{{
  			Variant:        NewVariantSpec("A100", 1),
  			Supply:         30.0,
  			ReplicaCount:   3,
  			CostPerReplica: 5.0,
  		}},
  	}}
  	decisions := e.Optimize(results, GPUInventory{byVariant: map[VariantSpec]int{}})
  	require.Len(t, decisions, 1)
  	assert.Equal(t, -2, decisions[0].Delta)
  }

  func TestSLOEngineScaleDownPicksMostExpensiveVariant(t *testing.T) {
  	e := &SLOEngine{}
  	results := []AnalyzerResult{{
  		ModelID:       "m1",
  		SpareCapacity: 5.0,
  		VariantCapacities: []VariantCapacity{
  			{Variant: NewVariantSpec("A100", 1), Supply: 10.0, ReplicaCount: 1, CostPerReplica: 3.0},
  			{Variant: NewVariantSpec("H100", 1), Supply: 12.0, ReplicaCount: 1, CostPerReplica: 8.0},
  		},
  	}}
  	decisions := e.Optimize(results, GPUInventory{byVariant: map[VariantSpec]int{}})
  	require.Len(t, decisions, 1)
  	assert.Equal(t, NewVariantSpec("H100", 1), decisions[0].Variant)
  }

  func TestSLOEngineNoDecision(t *testing.T) {
  	e := &SLOEngine{}
  	results := []AnalyzerResult{{ModelID: "m1"}} // no RequiredCapacity or SpareCapacity
  	decisions := e.Optimize(results, GPUInventory{byVariant: map[VariantSpec]int{}})
  	assert.Empty(t, decisions)
  }

  func TestSLOEngineAtMostOneDecisionPerModel(t *testing.T) {
  	e := &SLOEngine{}
  	results := []AnalyzerResult{
  		{ModelID: "m1", RequiredCapacity: 5.0, VariantCapacities: []VariantCapacity{
  			{Variant: NewVariantSpec("A100", 1), Supply: 10.0, ReplicaCount: 1, CostPerReplica: 5.0},
  		}},
  		{ModelID: "m2", SpareCapacity: 5.0, VariantCapacities: []VariantCapacity{
  			{Variant: NewVariantSpec("A100", 1), Supply: 10.0, ReplicaCount: 2, CostPerReplica: 5.0},
  		}},
  	}
  	inv := GPUInventory{byVariant: map[VariantSpec]int{NewVariantSpec("A100", 1): 10}}
  	decisions := e.Optimize(results, inv)
  	modelIDs := make(map[string]int)
  	for _, d := range decisions {
  		modelIDs[d.ModelID]++
  	}
  	for model, count := range modelIDs {
  		assert.Equal(t, 1, count, "model %q should have at most one decision", model)
  	}
  }
  ```

- [ ] **Step 2: Verify tests fail (SLOEngine not yet defined)**

  ```bash
  go test ./sim/cluster/ -run TestSLOEngine -v -count=1
  ```

  Expected: compile error — `SLOEngine` undefined.

- [ ] **Step 3: Commit the tests**

  ```bash
  git add sim/cluster/slo_engine_test.go
  git commit -m "test: add SLOEngine test suite (failing — SLOEngine not yet implemented)"
  ```

---

## Task 6: Implement SLOEngine

**Files:**
- Create: `sim/cluster/slo_engine.go`

- [ ] **Step 1: Create `sim/cluster/slo_engine.go`**

  ```go
  // slo_engine.go implements SLOEngine — an Engine that computes exact replica deltas
  // from RPS-unit supply/demand produced by SLODrivenAnalyzer.
  package cluster

  import "math"

  // SLOEngine translates RPS-unit capacity signals into replica scale decisions.
  // Scale-up is aggressive: Delta = +ceil(RequiredCapacity / maxRPS_per_replica).
  // Scale-down is aggressive: Delta = -floor(SpareCapacity / maxRPS_per_replica), clamped to [-replicaCount, -1].
  // GPU inventory is respected for scale-up; exhausted inventory skips the decision.
  type SLOEngine struct{}

  // Optimize produces at most one ScaleDecision per ModelID.
  // Uses sortedByAscCost and sortedByDescCost from engine.go (same package).
  func (e *SLOEngine) Optimize(results []AnalyzerResult, inventory GPUInventory) []ScaleDecision {
  	var decisions []ScaleDecision
  	for _, r := range results {
  		if r.RequiredCapacity > 0 {
  			if d := e.scaleUpDecision(r, inventory); d != nil {
  				decisions = append(decisions, *d)
  			}
  			continue
  		}
  		if r.SpareCapacity > 0 {
  			if d := e.scaleDownDecision(r); d != nil {
  				decisions = append(decisions, *d)
  			}
  		}
  	}
  	return decisions
  }

  func (e *SLOEngine) scaleUpDecision(r AnalyzerResult, inventory GPUInventory) *ScaleDecision {
  	for _, vc := range sortedByAscCost(r.VariantCapacities) {
  		var n int
  		if vc.ReplicaCount > 0 && vc.Supply > 0 {
  			maxRPSPerReplica := vc.Supply / float64(vc.ReplicaCount)
  			n = int(math.Ceil(r.RequiredCapacity / maxRPSPerReplica))
  			if n < 1 {
  				n = 1
  			}
  		} else {
  			n = 1 // no active replicas — can't estimate; add one
  		}
  		if inventory.FreeSlots(vc.Variant) < n {
  			continue // not enough GPU slots for this variant; try next
  		}
  		return &ScaleDecision{ModelID: r.ModelID, Variant: vc.Variant, Delta: n}
  	}
  	return nil // all variants exhausted
  }

  func (e *SLOEngine) scaleDownDecision(r AnalyzerResult) *ScaleDecision {
  	for _, vc := range sortedByDescCost(r.VariantCapacities) {
  		if vc.ReplicaCount <= 0 || vc.Supply <= 0 {
  			continue
  		}
  		perReplica := vc.Supply / float64(vc.ReplicaCount)
  		n := int(math.Floor(r.SpareCapacity / perReplica))
  		if n < 1 {
  			n = 1
  		}
  		if n > vc.ReplicaCount {
  			n = vc.ReplicaCount
  		}
  		return &ScaleDecision{ModelID: r.ModelID, Variant: vc.Variant, Delta: -n}
  	}
  	return nil
  }
  ```

- [ ] **Step 2: Run tests — all must pass**

  ```bash
  go test ./sim/cluster/ -run TestSLOEngine -v -count=1
  ```

  Expected: all `TestSLOEngine*` tests PASS.

- [ ] **Step 3: Run full cluster test suite to verify no regressions**

  ```bash
  go test ./sim/cluster/ -count=1
  ```

  Expected: all tests pass.

- [ ] **Step 4: Commit**

  ```bash
  git add sim/cluster/slo_engine.go
  git commit -m "feat: implement SLOEngine with aggressive N-replica scale-up and scale-down"
  ```

---

## Task 7: Add SLO analyzer config fields to DeploymentConfig

**Files:**
- Modify: `sim/cluster/deployment.go`

- [ ] **Step 1: Add fields to `DeploymentConfig`**

  In `deployment.go`, add after the `AutoscalerAnalyzerConfig` field (around line 91):

  ```go
  // SLO-driven autoscaler configuration (Phase 1C extension).
  // SLOByModel maps ModelID → per-model SLO targets.
  // Other SLO* fields mirror SLOAnalyzerConfig; zero values are safe
  // (NewSLODrivenAnalyzer applies defaults: InitObs=5, WindowSize=20, WarmUpCycles=5, UseSliding=true).
  SLOByModel      map[string]ModelSLOConfig `yaml:"slo_by_model,omitempty"`
  SLOInitObs      int                       `yaml:"slo_init_obs,omitempty"`
  SLOUseSliding   bool                      `yaml:"slo_use_sliding,omitempty"`
  SLOWindowSize   int                       `yaml:"slo_window_size,omitempty"`
  SLOWarmUpCycles int                       `yaml:"slo_warm_up_cycles,omitempty"`
  ```

  > **Note:** Do NOT rename the existing `AutoscalerAnalyzerConfig` field or change any existing field. Insert the new fields below it.

- [ ] **Step 2: Run full test suite**

  ```bash
  go test ./... -count=1
  ```

  Expected: all tests pass. The new fields have zero values — no existing tests are affected.

- [ ] **Step 3: Commit**

  ```bash
  git add sim/cluster/deployment.go
  git commit -m "feat: add SLO analyzer config fields to DeploymentConfig"
  ```

---

## Task 8: Full verification pass

- [ ] **Step 1: Build all packages**

  ```bash
  go build ./...
  ```

  Expected: clean build, no errors.

- [ ] **Step 2: Run full test suite**

  ```bash
  go test ./... -count=1 -timeout 120s
  ```

  Expected: all tests pass including all pre-existing tests (regression). Look specifically for:
  - `sim/cluster/` — all autoscaler, engine, analyzer, collector, and cluster tests pass
  - No build errors in `sim/` or `cmd/`

- [ ] **Step 3: Verify `SLODrivenAnalyzer` + `SLOEngine` satisfy the interfaces**

  Add a compile-time interface check to `slo_driven_analyzer.go` and `slo_engine.go` (not test code — these are permanent guards):

  In `slo_driven_analyzer.go` add after the struct definitions:
  ```go
  var _ Analyzer = (*SLODrivenAnalyzer)(nil)
  ```

  In `slo_engine.go` add after the struct definition:
  ```go
  var _ Engine = (*SLOEngine)(nil)
  ```

  Then rebuild:
  ```bash
  go build ./sim/cluster/
  ```

  Expected: clean build — confirms interface compliance at compile time.

- [ ] **Step 4: Final commit**

  ```bash
  git add sim/cluster/slo_driven_analyzer.go sim/cluster/slo_engine.go
  git commit -m "chore: add compile-time interface checks for SLODrivenAnalyzer and SLOEngine"
  ```

---

## Self-Review

### Spec Coverage Check

| Spec Section | Task |
|---|---|
| `SLODrivenAnalyzer` struct + `SLOAnalyzerConfig` + `ModelSLOConfig` | Task 3–4 |
| `perModelState` with EKF tuner + warmUpLeft | Task 3–4 |
| `Analyze()` flow: EKF update per replica | Task 4 |
| Warm-up: neutral result for first WarmUpCycles; counter doesn't advance on empty replicas | Task 4 |
| Queue-analysis for maxRPS | Task 4 |
| TotalSupply/TotalDemand/RequiredCapacity/SpareCapacity in RPS units | Task 4 |
| N-1 redistribution safety check | Task 4 |
| `SLOEngine.Optimize()`: scale-up by N, scale-down by −1 | Task 6 |
| GPU inventory enforcement | Task 6 |
| Add TTFT/ITL/DispatchRate to `RoutingSnapshot` | Task 1 |
| Add ITL to `ReplicaMetrics` | Task 1 |
| Populate fields in `DefaultCollector` | Task 1 |
| Add SLOByModel/WarmUpCycles to `DeploymentConfig` | Task 7 |
| go.mod dependencies | Task 2 |
| Zero TTFT/DispatchRate replicas excluded from EKF but counted in replicaCount | Task 4 (ITL ≤ 0 guard) |
| Clamp zero/negative maxRPS to 1.0 | Task 4 (`minRPS = 1.0`) |
| Unknown ModelID → neutral result + log warning | Task 4 |
| Multi-variant: per-variant supply/demand in VariantCapacities | Task 4 |

### Placeholder Scan
None — all steps contain complete code.

### Type Consistency
- `SLODrivenAnalyzer.Analyze(ModelSignals) AnalyzerResult` — matches `Analyzer` interface ✓
- `SLOEngine.Optimize([]AnalyzerResult, GPUInventory) []ScaleDecision` — matches `Engine` interface ✓
- `sortedByAscCost` / `sortedByDescCost` reused from `engine.go` — same package, no duplication ✓
- `tunercore.NewEnvironmentPrefillDecode` → `*tunercore.EnvironmentPrefillDecode` — implements `tunercore.Environment` ✓
- `tunercore.SetupTunerForQueueingModel` sets the observation function — required for RunWithValidation to work ✓
- `qanalyzer.DefaultMaxNumTokens = 8192` — exported constant from queue-analysis ✓

---

**Plan complete and saved to `docs/superpowers/plans/2026-04-23-slo-driven-autoscaler.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — Fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
