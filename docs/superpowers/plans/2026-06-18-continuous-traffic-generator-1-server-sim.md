# Continuous Traffic Generator — Plan 1 of 2: server-sim

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make server-sim run its evaluation windows continuously in a background loop (one job at a time), store self-describing results, and serve the latest completed result over `GET /latest` — so the Collector never blocks.

**Architecture:** A config-gated background `Loop` reads its pod's load + allocation parameters from a downward-API labels file, creates a job in the existing `job.Manager`, runs it through `evaluator.Client` (with a per-backend saturation policy), and stores the *effective* input alongside the result and a completion timestamp. A new `GET /latest` handler returns the most-recent completed job as an envelope. The loop cancels an in-flight window when the allocation (`maxbatchsize`) changes, so a post-decision measurement is produced promptly.

**Tech Stack:** Go, gin, `github.com/google/uuid`, standard `context`/`time`. Tests use `httptest` mock evaluators (existing pattern in `pkg/server/server_test.go`).

**Repo / working directory:** `/Users/tantawi/Projects/llm-inferno/server-sim` (NOT control-loop). All paths below are relative to that repo.

## Global Constraints

- Go module: `github.com/llm-inferno/server-sim`. Run tests with `go test ./...`.
- The `/latest` envelope schema is the contract consumed by Plan 2 (control-loop Collector). Its JSON shape MUST be exactly: `{ "effectiveInput": <ProblemData>, "result": <AnalysisData>, "completedAt": <RFC3339 string> }`.
- `ProblemData` and `AnalysisData` are defined in `pkg/evaluator/types.go` and MUST NOT change shape (other repos depend on them).
- `effectiveInput` carries what was **actually run** — after any saturation-driven RPS reduction — never the raw label.
- Existing `POST /simulate` and `GET /simulate/:id` behaviour MUST keep working (used by qa/blis debugging and existing tests).
- Saturation retry constants match the control-loop originals being migrated: target utilization `0.95`, step `0.05`, max retries `3`.
- Do not introduce a Kubernetes client dependency in server-sim — parameters arrive via the downward-API file, read with `os.ReadFile` (mirrors `vllm-server-evaluator/pairing.go:readDownwardLabel`).

---

### Task 0: Branch

- [ ] **Step 1: Create the feature branch**

```bash
cd /Users/tantawi/Projects/llm-inferno/server-sim
git checkout -b feat/continuous-traffic-generator
```

---

### Task 1: Job envelope — store effective input + expose latest completed

**Files:**
- Modify: `pkg/job/job.go`
- Test: `pkg/job/job_test.go` (create)

**Interfaces:**
- Consumes: `evaluator.ProblemData`, `evaluator.AnalysisData` (`pkg/evaluator/types.go`).
- Produces:
  - `Job` gains exported fields `EffectiveInput evaluator.ProblemData` and `CompletedAt time.Time`.
  - `Manager.Complete(id string, effectiveInput evaluator.ProblemData, result evaluator.AnalysisData)` — **signature change** (adds `effectiveInput`).
  - `Manager.Latest() *Job` — returns the most-recently-completed job, or `nil` if none.

- [ ] **Step 1: Write the failing test**

```go
package job

import (
	"testing"

	"github.com/llm-inferno/server-sim/pkg/evaluator"
)

func TestCompleteStoresEffectiveInputAndLatest(t *testing.T) {
	m := NewManager(60 * 1e9) // 60s ttl
	id := m.Create()
	in := evaluator.ProblemData{RPS: 5, MaxConcurrency: 32, Model: "m", Accelerator: "H100"}
	out := evaluator.AnalysisData{AvgITL: 10, AvgTTFT: 100, Throughput: 5}
	m.Complete(id, in, out)

	j := m.Get(id)
	if j.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", j.Status)
	}
	if j.EffectiveInput.MaxConcurrency != 32 || j.Result.AvgITL != 10 {
		t.Fatalf("envelope not stored: %+v", j)
	}
	if j.CompletedAt.IsZero() {
		t.Fatalf("CompletedAt not set")
	}

	latest := m.Latest()
	if latest == nil || latest.ID != id {
		t.Fatalf("Latest() = %v, want job %s", latest, id)
	}
}

func TestLatestReturnsNilWhenNoneCompleted(t *testing.T) {
	m := NewManager(60 * 1e9)
	m.Create() // pending only
	if m.Latest() != nil {
		t.Fatalf("Latest() should be nil when no job has completed")
	}
}

func TestLatestPicksMostRecentCompletion(t *testing.T) {
	m := NewManager(60 * 1e9)
	id1 := m.Create()
	id2 := m.Create()
	m.Complete(id1, evaluator.ProblemData{RPS: 1}, evaluator.AnalysisData{})
	m.Complete(id2, evaluator.ProblemData{RPS: 2}, evaluator.AnalysisData{})
	if m.Latest().ID != id2 {
		t.Fatalf("Latest().ID = %s, want %s (most recent)", m.Latest().ID, id2)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/job/ -run TestComplete -v`
Expected: compile failure — `Complete` takes 2 args, `Job` has no `EffectiveInput`/`CompletedAt`, no `Latest`.

- [ ] **Step 3: Implement the changes**

In `pkg/job/job.go`, change the `Job` struct's `completedAt` to exported and add `EffectiveInput`:

```go
// Job holds the state of a single simulation job.
type Job struct {
	ID             string
	Status         Status
	EffectiveInput evaluator.ProblemData // load/allocation actually run (post saturation retry)
	Result         *evaluator.AnalysisData
	Error          string
	CompletedAt    time.Time // zero while pending
}
```

Update `sweep()` to use `j.CompletedAt` instead of `j.completedAt`. Change `Complete` and add `Latest`:

```go
// Complete marks a job as completed with the effective input that produced the result.
func (m *Manager) Complete(id string, effectiveInput evaluator.ProblemData, result evaluator.AnalysisData) {
	m.mu.Lock()
	if j, ok := m.jobs[id]; ok {
		j.Status = StatusCompleted
		j.EffectiveInput = effectiveInput
		j.Result = &result
		j.CompletedAt = time.Now()
	}
	m.mu.Unlock()
}

// Latest returns the most-recently-completed job, or nil if none has completed.
func (m *Manager) Latest() *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest *Job
	for _, j := range m.jobs {
		if j.Status != StatusCompleted {
			continue
		}
		if latest == nil || j.CompletedAt.After(latest.CompletedAt) {
			latest = j
		}
	}
	return latest
}
```

Also update `Fail` to set `j.CompletedAt = time.Now()` (rename from `completedAt`).

- [ ] **Step 4: Fix the existing caller**

`pkg/server/server.go:handleSimulate` calls `s.jobs.Complete(id, result)`. Update it to pass the request `pd` as the effective input:

```go
s.jobs.Complete(id, pd, result)
```

- [ ] **Step 5: Run job + server tests**

Run: `go test ./pkg/job/ ./pkg/server/ -v`
Expected: PASS (existing server tests still green; `handleGetJob` is unchanged and still reads `j.Result`).

- [ ] **Step 6: Commit**

```bash
git add pkg/job/job.go pkg/job/job_test.go pkg/server/server.go
git commit -m "feat(job): store effective input + expose latest completed job"
```

---

### Task 2: Config — continuous mode, tick interval, saturation policy

**Files:**
- Modify: `pkg/config/config.go`
- Test: `pkg/config/config_test.go` (create or extend)

**Interfaces:**
- Produces: `Config` gains `ContinuousMode bool`, `TickInterval time.Duration`, `SaturationPolicy string`, `LabelsDir string`.
- Constants in `config` package: `SaturationPolicyRetry = "retry-at-lower-load"`, `SaturationPolicyPassThrough = "pass-through"`.
- Env vars: `SERVERSIM_CONTINUOUS` (`"true"`), `SERVERSIM_TICK_SECONDS` (int, default `5`, floor `1`), `SERVERSIM_SATURATION_POLICY` (default `retry-at-lower-load`), `SERVERSIM_LABELS_DIR` (default `/etc/podinfo`).

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"testing"
	"time"
)

func TestLoadContinuousDefaults(t *testing.T) {
	t.Setenv("SERVERSIM_CONTINUOUS", "true")
	cfg := Load()
	if !cfg.ContinuousMode {
		t.Fatal("ContinuousMode should be true")
	}
	if cfg.TickInterval != 5*time.Second {
		t.Fatalf("TickInterval = %v, want 5s", cfg.TickInterval)
	}
	if cfg.SaturationPolicy != SaturationPolicyRetry {
		t.Fatalf("SaturationPolicy = %q, want %q", cfg.SaturationPolicy, SaturationPolicyRetry)
	}
	if cfg.LabelsDir != "/etc/podinfo" {
		t.Fatalf("LabelsDir = %q", cfg.LabelsDir)
	}
}

func TestLoadTickFloorAndPolicyOverride(t *testing.T) {
	t.Setenv("SERVERSIM_TICK_SECONDS", "0")
	t.Setenv("SERVERSIM_SATURATION_POLICY", "pass-through")
	cfg := Load()
	if cfg.TickInterval != 1*time.Second {
		t.Fatalf("TickInterval = %v, want 1s floor", cfg.TickInterval)
	}
	if cfg.SaturationPolicy != SaturationPolicyPassThrough {
		t.Fatalf("SaturationPolicy = %q", cfg.SaturationPolicy)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/config/ -v`
Expected: compile failure — fields/constants undefined.

- [ ] **Step 3: Implement**

Add to the `Config` struct and `Load()` in `pkg/config/config.go`:

```go
const (
	SaturationPolicyRetry       = "retry-at-lower-load"
	SaturationPolicyPassThrough = "pass-through"
)
```

Add struct fields `ContinuousMode bool`, `TickInterval time.Duration`, `SaturationPolicy string`, `LabelsDir string`. In `Load()` set defaults (`TickInterval: 5 * time.Second`, `SaturationPolicy: SaturationPolicyRetry`, `LabelsDir: "/etc/podinfo"`) and parse env:

```go
if os.Getenv("SERVERSIM_CONTINUOUS") == "true" {
	cfg.ContinuousMode = true
}
if v := os.Getenv("SERVERSIM_TICK_SECONDS"); v != "" {
	if s, err := strconv.Atoi(v); err == nil {
		if s < 1 {
			s = 1
		}
		cfg.TickInterval = time.Duration(s) * time.Second
	}
}
if v := os.Getenv("SERVERSIM_SATURATION_POLICY"); v != "" {
	cfg.SaturationPolicy = v
}
if v := os.Getenv("SERVERSIM_LABELS_DIR"); v != "" {
	cfg.LabelsDir = v
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): continuous-mode, tick interval, saturation policy"
```

---

### Task 3: Read effective input from the downward-API labels file

**Files:**
- Create: `pkg/server/labels.go`
- Test: `pkg/server/labels_test.go`

**Interfaces:**
- Consumes: `config.Config.LabelsDir`, `evaluator.ProblemData`.
- Produces:
  - `func ReadLabels(path string) (map[string]string, error)` — parses a downward-API `metadata.labels` projection (lines `key="value"`).
  - `func LabelsToProblemData(labels map[string]string) (evaluator.ProblemData, bool)` — extracts the workload; returns `ok=false` if any required field (rpm, intokens, outtokens, model, accelerator) is missing/zero. RPS = rpm/60. `MaxConcurrency` comes from `inferno.server.allocation.maxbatchsize` (0 if absent — evaluator resolves it).
- Label keys (verbatim, matching control-loop `pkg/controller/defaults.go`): `inferno.server.load.rpm`, `inferno.server.load.intokens`, `inferno.server.load.outtokens`, `inferno.server.model`, `inferno.server.allocation.accelerator`, `inferno.server.allocation.maxbatchsize`.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleLabels = `app="vllm-qwen-14b-server"
inferno.server.load.rpm="300"
inferno.server.load.intokens="1024"
inferno.server.load.outtokens="512"
inferno.server.model="qwen_2_5_14b"
inferno.server.allocation.accelerator="H100"
inferno.server.allocation.maxbatchsize="32"
`

func TestReadLabelsAndConvert(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "labels"), []byte(sampleLabels), 0o644); err != nil {
		t.Fatal(err)
	}
	labels, err := ReadLabels(filepath.Join(dir, "labels"))
	if err != nil {
		t.Fatalf("ReadLabels: %v", err)
	}
	pd, ok := LabelsToProblemData(labels)
	if !ok {
		t.Fatal("LabelsToProblemData ok=false, want true")
	}
	if pd.RPS != 5 { // 300/60
		t.Fatalf("RPS = %v, want 5", pd.RPS)
	}
	if pd.AvgInputTokens != 1024 || pd.AvgOutputTokens != 512 {
		t.Fatalf("tokens = %v/%v", pd.AvgInputTokens, pd.AvgOutputTokens)
	}
	if pd.MaxConcurrency != 32 || pd.Model != "qwen_2_5_14b" || pd.Accelerator != "H100" {
		t.Fatalf("bad pd: %+v", pd)
	}
}

func TestLabelsToProblemDataMissingFieldNotReady(t *testing.T) {
	// rpm absent (pod not yet labelled by the load emulator) → not ready.
	labels := map[string]string{
		"inferno.server.model":                       "m",
		"inferno.server.allocation.accelerator":      "H100",
	}
	if _, ok := LabelsToProblemData(labels); ok {
		t.Fatal("ok should be false when rpm is missing")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/server/ -run TestReadLabels -v` and `-run TestLabelsToProblemData`
Expected: compile failure — functions undefined.

- [ ] **Step 3: Implement `pkg/server/labels.go`**

```go
package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/llm-inferno/server-sim/pkg/evaluator"
)

// Label keys written on the pod by the Load Emulator (load.*) and the
// Actuator (allocation.*). Must match control-loop pkg/controller/defaults.go.
const (
	labelRPM          = "inferno.server.load.rpm"
	labelInTokens     = "inferno.server.load.intokens"
	labelOutTokens    = "inferno.server.load.outtokens"
	labelModel        = "inferno.server.model"
	labelAccelerator  = "inferno.server.allocation.accelerator"
	labelMaxBatchSize = "inferno.server.allocation.maxbatchsize"
)

// ReadLabels parses a downward-API metadata.labels projection. Each line is
// key="value"; surrounding quotes are stripped. Missing file → error.
func ReadLabels(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read labels %s: %w", path, err)
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out, nil
}

// LabelsToProblemData builds a ProblemData from pod labels. Returns ok=false
// when a required workload field is absent or non-positive — the pod is not
// yet ready to drive (e.g. the Load Emulator has not labelled it). RPS is
// derived from rpm. MaxConcurrency may be 0 (the evaluator resolves a default).
func LabelsToProblemData(labels map[string]string) (evaluator.ProblemData, bool) {
	rpm, err1 := strconv.ParseFloat(labels[labelRPM], 32)
	inTok, err2 := strconv.Atoi(labels[labelInTokens])
	outTok, err3 := strconv.Atoi(labels[labelOutTokens])
	model := labels[labelModel]
	acc := labels[labelAccelerator]
	if err1 != nil || err2 != nil || err3 != nil || rpm <= 0 || inTok <= 0 || outTok <= 0 || model == "" || acc == "" {
		return evaluator.ProblemData{}, false
	}
	maxBatch, _ := strconv.Atoi(labels[labelMaxBatchSize]) // 0 if absent → evaluator default
	return evaluator.ProblemData{
		RPS:             float32(rpm / 60.0),
		MaxConcurrency:  maxBatch,
		AvgInputTokens:  float32(inTok),
		AvgOutputTokens: float32(outTok),
		Accelerator:     acc,
		Model:           model,
	}, true
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/server/ -run 'TestReadLabels|TestLabelsToProblemData' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/labels.go pkg/server/labels_test.go
git commit -m "feat(server): parse effective input from downward-API labels"
```

---

### Task 4: Context-aware evaluator call

**Files:**
- Modify: `pkg/evaluator/client.go`
- Test: `pkg/evaluator/client_test.go` (create)

**Interfaces:**
- Produces: `func (c *Client) SolveCtx(ctx context.Context, pd ProblemData) (AnalysisData, error)`. Existing `Solve(pd)` is kept and delegates to `SolveCtx(context.Background(), pd)`.
- Cancelling `ctx` aborts the in-flight HTTP request (so the evaluator's `runWindow`, which keys off the request context, stops).

- [ ] **Step 1: Write the failing test**

```go
package evaluator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSolveCtxCancels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until client cancels
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	_, err := c.SolveCtx(ctx, ProblemData{RPS: 1})
	if err == nil {
		t.Fatal("expected error on cancellation")
	}
}

func TestSolveCtxSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(AnalysisData{AvgITL: 7})
	}))
	defer srv.Close()
	ad, err := NewClient(srv.URL).SolveCtx(context.Background(), ProblemData{RPS: 1})
	if err != nil || ad.AvgITL != 7 {
		t.Fatalf("ad=%+v err=%v", ad, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/evaluator/ -run TestSolveCtx -v`
Expected: compile failure — `SolveCtx` undefined.

- [ ] **Step 3: Implement**

Refactor `Solve` to build the request with a context. Replace the body of `Solve`:

```go
// Solve calls POST {baseURL}/solve with the given ProblemData and returns AnalysisData.
func (c *Client) Solve(pd ProblemData) (AnalysisData, error) {
	return c.SolveCtx(context.Background(), pd)
}

// SolveCtx is Solve with caller-controlled cancellation. Cancelling ctx aborts
// the in-flight request, which stops the evaluator's measurement window.
func (c *Client) SolveCtx(ctx context.Context, pd ProblemData) (AnalysisData, error) {
	body, err := json.Marshal(pd)
	if err != nil {
		return AnalysisData{}, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/solve", bytes.NewReader(body))
	if err != nil {
		return AnalysisData{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return AnalysisData{}, fmt.Errorf("POST /solve: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return AnalysisData{}, fmt.Errorf("evaluator returned status %d", resp.StatusCode)
	}
	var ad AnalysisData
	if err := json.NewDecoder(resp.Body).Decode(&ad); err != nil {
		return AnalysisData{}, fmt.Errorf("decode response: %w", err)
	}
	return ad, nil
}
```

Add `"context"` to imports.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/evaluator/ -run TestSolveCtx -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/evaluator/client.go pkg/evaluator/client_test.go
git commit -m "feat(evaluator): context-aware SolveCtx for cancellable windows"
```

---

### Task 5: Solve-with-saturation-policy helper

**Files:**
- Create: `pkg/server/policy.go`
- Test: `pkg/server/policy_test.go`

**Interfaces:**
- Consumes: `evaluator.Client.SolveCtx`, `config.Config.SaturationPolicy`, `evaluator.ProblemData`, `evaluator.AnalysisData`.
- Produces: `func solveWithPolicy(ctx context.Context, cli solver, policy string, pd evaluator.ProblemData) (evaluator.ProblemData, evaluator.AnalysisData, error)` — returns the **effective** ProblemData (post-retry) and the result. `solver` is an interface `{ SolveCtx(context.Context, evaluator.ProblemData) (evaluator.AnalysisData, error) }` so tests can mock it.
- Retry constants `overloadTargetUtilization=0.95`, `overloadRetryStep=0.05`, `overloadMaxRetries=3`.

Behaviour:
- `pass-through`: one Solve; return its result and the original pd, regardless of saturation.
- `retry-at-lower-load`: if the first result is saturated, re-Solve at `RPS = MaxRPS * utilization` for utilization `0.95, 0.90, 0.85`, returning the first unsaturated result and the reduced pd. If still saturated after 3 attempts, return the last (saturated) result and the last reduced pd with an error so the caller can skip publishing.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"context"
	"testing"

	"github.com/llm-inferno/server-sim/pkg/config"
	"github.com/llm-inferno/server-sim/pkg/evaluator"
)

type scriptedSolver struct {
	calls   int
	results []evaluator.AnalysisData
	lastRPS []float32
}

func (s *scriptedSolver) SolveCtx(_ context.Context, pd evaluator.ProblemData) (evaluator.AnalysisData, error) {
	s.lastRPS = append(s.lastRPS, pd.RPS)
	r := s.results[s.calls]
	s.calls++
	return r, nil
}

func TestPolicyPassThroughKeepsSaturated(t *testing.T) {
	s := &scriptedSolver{results: []evaluator.AnalysisData{{Saturation: evaluator.SaturationKV, MaxRPS: 4}}}
	pd := evaluator.ProblemData{RPS: 10}
	eff, ad, err := solveWithPolicy(context.Background(), s, config.SaturationPolicyPassThrough, pd)
	if err != nil {
		t.Fatalf("pass-through should not error: %v", err)
	}
	if ad.Saturation == "" || eff.RPS != 10 || s.calls != 1 {
		t.Fatalf("pass-through wrong: calls=%d eff=%v ad=%v", s.calls, eff, ad)
	}
}

func TestPolicyRetryRecoversUnsaturated(t *testing.T) {
	s := &scriptedSolver{results: []evaluator.AnalysisData{
		{Saturation: evaluator.SaturationOverload, MaxRPS: 4}, // first: saturated
		{Saturation: "", MaxRPS: 4},                           // retry at 0.95*4=3.8: ok
	}}
	pd := evaluator.ProblemData{RPS: 10}
	eff, ad, err := solveWithPolicy(context.Background(), s, config.SaturationPolicyRetry, pd)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ad.Saturation != "" {
		t.Fatalf("should have recovered unsaturated")
	}
	if eff.RPS != float32(4*0.95) {
		t.Fatalf("effective RPS = %v, want %v", eff.RPS, float32(4*0.95))
	}
}

func TestPolicyRetryExhaustedErrors(t *testing.T) {
	s := &scriptedSolver{results: []evaluator.AnalysisData{
		{Saturation: "x", MaxRPS: 4}, {Saturation: "x", MaxRPS: 4},
		{Saturation: "x", MaxRPS: 4}, {Saturation: "x", MaxRPS: 4},
	}}
	_, _, err := solveWithPolicy(context.Background(), s, config.SaturationPolicyRetry, evaluator.ProblemData{RPS: 10})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/server/ -run TestPolicy -v`
Expected: compile failure — `solveWithPolicy`/`solver` undefined.

- [ ] **Step 3: Implement `pkg/server/policy.go`**

```go
package server

import (
	"context"
	"fmt"

	"github.com/llm-inferno/server-sim/pkg/config"
	"github.com/llm-inferno/server-sim/pkg/evaluator"
)

const (
	overloadTargetUtilization = float32(0.95)
	overloadRetryStep         = float32(0.05)
	overloadMaxRetries        = 3
)

// solver is the subset of evaluator.Client used here (mockable in tests).
type solver interface {
	SolveCtx(context.Context, evaluator.ProblemData) (evaluator.AnalysisData, error)
}

// solveWithPolicy runs one window and applies the saturation policy. It returns
// the EFFECTIVE input actually run (post retry-adjustment) and the result.
func solveWithPolicy(ctx context.Context, cli solver, policy string, pd evaluator.ProblemData) (evaluator.ProblemData, evaluator.AnalysisData, error) {
	ad, err := cli.SolveCtx(ctx, pd)
	if err != nil {
		return pd, ad, err
	}
	if policy == config.SaturationPolicyPassThrough || !ad.IsSaturated() {
		return pd, ad, nil
	}
	// retry-at-lower-load
	util := overloadTargetUtilization
	eff := pd
	for attempt := 1; attempt <= overloadMaxRetries; attempt++ {
		eff = pd
		eff.RPS = ad.MaxRPS * util
		next, nerr := cli.SolveCtx(ctx, eff)
		if nerr != nil {
			return eff, ad, nerr
		}
		ad = next
		if !ad.IsSaturated() {
			return eff, ad, nil
		}
		util -= overloadRetryStep
	}
	return eff, ad, fmt.Errorf("still saturated after %d retries", overloadMaxRetries)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/server/ -run TestPolicy -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/policy.go pkg/server/policy_test.go
git commit -m "feat(server): saturation policy (pass-through / retry-at-lower-load)"
```

---

### Task 6: Allocation-change detector

**Files:**
- Create: `pkg/server/alloc_watch.go`
- Test: `pkg/server/alloc_watch_test.go`

**Interfaces:**
- Produces: `func concurrencyFromFile(path string) (int, bool)` — reads the labels file and returns the current `maxbatchsize` (ok=false if unreadable/absent). Used by the loop's watcher goroutine to detect allocation changes mid-window.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConcurrencyFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "labels")
	os.WriteFile(p, []byte(`inferno.server.allocation.maxbatchsize="64"`+"\n"), 0o644)
	got, ok := concurrencyFromFile(p)
	if !ok || got != 64 {
		t.Fatalf("got %d ok=%v, want 64 true", got, ok)
	}

	os.WriteFile(p, []byte(`other="x"`+"\n"), 0o644)
	if _, ok := concurrencyFromFile(p); ok {
		t.Fatal("ok should be false when maxbatchsize absent")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/server/ -run TestConcurrencyFromFile -v`
Expected: compile failure — undefined.

- [ ] **Step 3: Implement `pkg/server/alloc_watch.go`**

```go
package server

import "strconv"

// concurrencyFromFile reads the current maxbatchsize (allocation concurrency)
// from the downward-API labels file. ok=false if the file is unreadable or the
// label is absent/invalid.
func concurrencyFromFile(path string) (int, bool) {
	labels, err := ReadLabels(path)
	if err != nil {
		return 0, false
	}
	v, ok := labels[labelMaxBatchSize]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/server/ -run TestConcurrencyFromFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/alloc_watch.go pkg/server/alloc_watch_test.go
git commit -m "feat(server): read current allocation concurrency for edge-detect"
```

---

### Task 7: The background Loop

**Files:**
- Create: `pkg/server/loop.go`
- Test: `pkg/server/loop_test.go`

**Interfaces:**
- Consumes: `config.Config`, `*job.Manager`, `solver` (Task 5), `concurrencyFromFile` (Task 6), `ReadLabels`/`LabelsToProblemData` (Task 3), `solveWithPolicy` (Task 5).
- Produces:
  - `type Loop struct { cfg config.Config; jobs *job.Manager; cli solver; labelsPath string }`
  - `func NewLoop(cfg config.Config, jobs *job.Manager, cli solver) *Loop`
  - `func (l *Loop) Run(ctx context.Context)` — ticker loop; one window at a time.
  - `func (l *Loop) runOnce(ctx context.Context)` — one window: read labels → create job → solveWithPolicy with mid-window allocation-change cancellation → store. Skips silently when labels not ready or the window failed.
- `labelsPath = filepath.Join(cfg.LabelsDir, "labels")`.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llm-inferno/server-sim/pkg/config"
	"github.com/llm-inferno/server-sim/pkg/evaluator"
	"github.com/llm-inferno/server-sim/pkg/job"
)

type okSolver struct{ ad evaluator.AnalysisData }

func (s okSolver) SolveCtx(_ context.Context, _ evaluator.ProblemData) (evaluator.AnalysisData, error) {
	return s.ad, nil
}

func writeLabels(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "labels")
	if err := os.WriteFile(p, []byte(sampleLabels), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunOncePublishesLatest(t *testing.T) {
	dir := t.TempDir()
	writeLabels(t, dir)
	cfg := config.Config{SaturationPolicy: config.SaturationPolicyPassThrough, LabelsDir: dir, TickInterval: time.Second}
	jobs := job.NewManager(60 * time.Second)
	l := NewLoop(cfg, jobs, okSolver{ad: evaluator.AnalysisData{AvgITL: 9, Throughput: 5}})

	l.runOnce(context.Background())

	latest := jobs.Latest()
	if latest == nil {
		t.Fatal("no latest job after runOnce")
	}
	if latest.Result.AvgITL != 9 {
		t.Fatalf("latest result wrong: %+v", latest.Result)
	}
	if latest.EffectiveInput.MaxConcurrency != 32 {
		t.Fatalf("effective input concurrency = %d, want 32", latest.EffectiveInput.MaxConcurrency)
	}
}

func TestRunOnceSkipsWhenLabelsMissing(t *testing.T) {
	dir := t.TempDir() // no labels file
	cfg := config.Config{SaturationPolicy: config.SaturationPolicyPassThrough, LabelsDir: dir}
	jobs := job.NewManager(60 * time.Second)
	NewLoop(cfg, jobs, okSolver{}).runOnce(context.Background())
	if jobs.Latest() != nil {
		t.Fatal("should not publish when labels missing")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/server/ -run TestRunOnce -v`
Expected: compile failure — `NewLoop`/`Loop` undefined.

- [ ] **Step 3: Implement `pkg/server/loop.go`**

```go
package server

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/llm-inferno/server-sim/pkg/config"
	"github.com/llm-inferno/server-sim/pkg/job"
)

// Loop drives continuous evaluation windows, one at a time, reading its
// workload from the downward-API labels file.
type Loop struct {
	cfg        config.Config
	jobs       *job.Manager
	cli        solver
	labelsPath string
}

func NewLoop(cfg config.Config, jobs *job.Manager, cli solver) *Loop {
	return &Loop{cfg: cfg, jobs: jobs, cli: cli, labelsPath: filepath.Join(cfg.LabelsDir, "labels")}
}

// Run ticks until ctx is cancelled, running one window per tick.
func (l *Loop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.runOnce(ctx)
		}
	}
}

// runOnce executes a single window. It reads the current labels, creates a job,
// runs the window (cancelling it if the allocation concurrency changes
// mid-flight), and stores the effective input + result. Silently skips when the
// pod is not yet ready or the window fails — the Collector handles the
// resulting absence/staleness.
func (l *Loop) runOnce(parent context.Context) {
	labels, err := ReadLabels(l.labelsPath)
	if err != nil {
		return // not ready
	}
	pd, ok := LabelsToProblemData(labels)
	if !ok {
		return // not ready
	}

	id := l.jobs.Create()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// Watch for an allocation change; cancel the in-flight window so the next
	// window runs under the new concurrency promptly.
	startConc := pd.MaxConcurrency
	go l.watchAllocation(ctx, cancel, startConc)

	eff, result, err := solveWithPolicy(ctx, l.cli, l.cfg.SaturationPolicy, pd)
	if err != nil {
		l.jobs.Fail(id, err.Error())
		log.Printf("loop: window failed (skipping publish): %v", err)
		return
	}
	l.jobs.Complete(id, eff, result)
}

// watchAllocation polls the labels file and cancels when maxbatchsize changes
// from startConc. Returns when ctx is done.
func (l *Loop) watchAllocation(ctx context.Context, cancel context.CancelFunc, startConc int) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if cur, ok := concurrencyFromFile(l.labelsPath); ok && cur != startConc {
				log.Printf("loop: allocation changed %d -> %d; abandoning in-flight window", startConc, cur)
				cancel()
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/server/ -run TestRunOnce -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/loop.go pkg/server/loop_test.go
git commit -m "feat(server): continuous evaluation loop with allocation edge-detect"
```

---

### Task 8: `GET /latest` handler + wire the loop into the server

**Files:**
- Modify: `pkg/server/server.go`
- Test: `pkg/server/server_test.go` (add cases)

**Interfaces:**
- Consumes: `Manager.Latest()` (Task 1), `Loop` (Task 7).
- Produces:
  - Route `GET /latest`. Returns `404 {"error":"no result yet"}` when `Latest()==nil`; else `200` with envelope `{ "effectiveInput": <ProblemData>, "result": <AnalysisData>, "completedAt": <time> }`.
  - `Server.New` starts the loop in a goroutine when `cfg.ContinuousMode` is true, using the same `evalCli` (which satisfies `solver` via `SolveCtx`). A `context.Context` field on `Server` is cancelled by a new `Server.Shutdown()` (optional; `Run` can pass `context.Background()`).

- [ ] **Step 1: Write the failing test**

```go
func TestLatestColdStart404(t *testing.T) {
	eval := mockEvaluator(t, evaluator.AnalysisData{AvgITL: 5})
	defer eval.Close()
	s := New(config.Config{EvaluatorURL: eval.URL, JobTTL: time.Minute})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/latest")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cold-start /latest = %d, want 404", resp.StatusCode)
	}
}

func TestLatestReturnsEnvelope(t *testing.T) {
	eval := mockEvaluator(t, evaluator.AnalysisData{AvgITL: 5, Throughput: 3})
	defer eval.Close()
	s := New(config.Config{EvaluatorURL: eval.URL, JobTTL: time.Minute})
	// Drive one job synchronously via the existing POST path.
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	id := submitJob(t, srv)
	pollJob(t, srv, id)

	resp, err := http.Get(srv.URL + "/latest")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("/latest status err=%v code=%d", err, resp.StatusCode)
	}
	var env struct {
		EffectiveInput evaluator.ProblemData `json:"effectiveInput"`
		Result         evaluator.AnalysisData `json:"result"`
		CompletedAt    string                 `json:"completedAt"`
	}
	json.NewDecoder(resp.Body).Decode(&env)
	if env.Result.AvgITL != 5 || env.CompletedAt == "" {
		t.Fatalf("bad envelope: %+v", env)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/server/ -run TestLatest -v`
Expected: FAIL — `/latest` returns 404 for the not-found route (no handler), and the envelope test fails to decode.

- [ ] **Step 3: Implement**

In `pkg/server/server.go`, register the route in `New` and start the loop when continuous:

```go
s.router.GET("/latest", s.handleLatest)
...
if cfg.ContinuousMode {
	loop := NewLoop(cfg, s.jobs, s.evalCli)
	go loop.Run(context.Background())
}
return s
```

Add the handler:

```go
// handleLatest returns the most-recent completed job as a self-describing envelope.
func (s *Server) handleLatest(c *gin.Context) {
	j := s.jobs.Latest()
	if j == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no result yet"})
		return
	}
	c.IndentedJSON(http.StatusOK, gin.H{
		"effectiveInput": j.EffectiveInput,
		"result":         j.Result,
		"completedAt":    j.CompletedAt,
	})
}
```

Add `"context"` to imports. Note `s.evalCli` (`*evaluator.Client`) satisfies `solver` because it has `SolveCtx`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/server/ -v`
Expected: PASS (all, including existing simulate tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/server/server.go pkg/server/server_test.go
git commit -m "feat(server): GET /latest envelope + start continuous loop"
```

---

### Task 9: Full build + module test sweep

- [ ] **Step 1: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 2: Run the whole suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 3: Commit (if any incidental fixes were needed)**

```bash
git add -A && git commit -m "chore(server-sim): build/vet clean for continuous mode" || true
```

---

## Self-Review

**Spec coverage** (against `2026-06-18-continuous-traffic-generator-design.md`):
- Continuous loop, one job at a time → Task 7. ✓
- `GET /latest`, cold-start 404, envelope w/ effectiveInput + completedAt → Tasks 1, 8. ✓
- Saturation policy moved into loop, per-backend config → Tasks 2, 5. ✓
- Load params via downward-API labels → Tasks 3, 7. ✓
- Allocation edge-detect (abandon in-flight on M* change) → Tasks 4, 6, 7. ✓
- Effective input = actually-run (post-retry) → Task 5 returns `eff`, Task 7 stores it. ✓
- `POST /simulate` / `GET /simulate/:id` unchanged → Task 1 Step 4 keeps the caller working; Task 8 adds only a route. ✓
- TTL > control period → operational note; `JobTTL` default 60min already far exceeds any control period (no code change; documented in Plan 2 manifests). ✓

**Deferred to Plan 2 (control-loop):** Collector `GET /latest` consumption + coherence check; Actuator writing accelerator+maxbatchsize to running pods; manifests (downward-API labels volume on the server-sim container, pod-template model/accelerator labels, `SERVERSIM_CONTINUOUS` env); the window-upper-bound config invariant validation.

**Type consistency:** `solver` interface (`SolveCtx`) used by Tasks 5/7; satisfied by `*evaluator.Client` (Task 4) and test mocks. `Manager.Complete(id, ProblemData, AnalysisData)` consistent across Tasks 1, 7. Envelope JSON keys (`effectiveInput`/`result`/`completedAt`) consistent across Tasks 1 and 8 and match the Plan 2 contract.
