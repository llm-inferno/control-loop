# Load Emulator Multi-Phase Feature Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a configurable phase sequence to the load emulator so the nominal RPM ramps linearly between phases, with the existing Ornstein-Uhlenbeck random walk tracking the changing target.

**Architecture:** A new `PhaseTracker` (in `pkg/loademulator/phases.go`) reads a YAML config, tracks wall-clock time, and returns a cumulative multiplier each cycle. `LoadEmulator` stores per-deployment original nominal baselines on first encounter, applies the multiplier to compute an adjusted nominal, updates the Kubernetes `nominal.rpm` label, and passes the adjusted nominal to the existing `perturbLoad()`. The feature is opt-in: no config file = current behavior unchanged.

**Tech Stack:** Go, `gopkg.in/yaml.v3` (already an indirect dep — no `go get` required), Kubernetes client-go, YAML ConfigMap.

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `pkg/loademulator/phases.go` | **Create** | `Phase`, `PhaseConfig`, `PhaseTracker`, YAML parsing, `GetMultiplier()` |
| `pkg/loademulator/loademulator.go` | **Modify** | Add `tracker` + `originalNominalRPM` fields; integrate phase logic in `Run()` |
| `cmd/loademulator/main.go` | **Modify** | Read `INFERNO_LOAD_PHASES`, construct `PhaseTracker`, pass to emulator |
| `pkg/controller/defaults.go` | **Modify** | Add `LoadPhasesEnvName` constant |
| `yamls/deploy/load-emulator.yaml` | **Modify** | Add optional ConfigMap volume + mount + env var |
| `scripts/kind-deploy.sh` | **Modify** | Add optional ConfigMap creation step |
| `sample-data/load-phases.yaml` | **Create** | Sample phase sequence for testing |

---

## Task 1: Add env var constant

**Files:**
- Modify: `pkg/controller/defaults.go`

- [ ] **Step 1: Add `LoadPhasesEnvName` constant**

In `pkg/controller/defaults.go`, add one line inside the existing `const` block that groups the other `INFERNO_LOAD_*` constants (after `LoadSkewEnvName`):

```go
LoadPhasesEnvName = "INFERNO_LOAD_PHASES"
```

The block becomes:

```go
LoadIntervalEnvName = "INFERNO_LOAD_INTERVAL"
LoadAlphaEnvName    = "INFERNO_LOAD_ALPHA"
LoadThetaEnvName    = "INFERNO_LOAD_THETA"
LoadSkewEnvName     = "INFERNO_LOAD_SKEW"
LoadPhasesEnvName   = "INFERNO_LOAD_PHASES"
```

- [ ] **Step 2: Build to verify no compile errors**

```bash
go build ./...
```

Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add pkg/controller/defaults.go
git commit -m "feat: add INFERNO_LOAD_PHASES env var constant"
```

---

## Task 2: Create phase tracker

**Files:**
- Create: `pkg/loademulator/phases.go`

- [ ] **Step 1: Create `pkg/loademulator/phases.go`**

```go
package loademulator

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Phase describes one segment of the load phase sequence.
// Duration is the real-time length of the phase; zero means hold forever (terminal).
// Ratio is the factor by which the nominal RPM changes from the start to the end of this
// phase, relative to the nominal at the start of the phase (chained). Ignored for terminal phases.
type Phase struct {
	Duration time.Duration
	Ratio    float64
}

// phaseEntry is the raw YAML representation of a phase.
type phaseEntry struct {
	Duration string  `yaml:"duration"`
	Ratio    float64 `yaml:"ratio"`
}

// phaseFile is the top-level YAML structure.
type phaseFile struct {
	Phases []phaseEntry `yaml:"phases"`
}

// LoadPhasesFromFile parses a YAML phase config file and returns a PhaseTracker.
// Returns nil, nil when path is empty (feature disabled).
func LoadPhasesFromFile(path string) (*PhaseTracker, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("phases: reading %s: %w", path, err)
	}
	var f phaseFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("phases: parsing %s: %w", path, err)
	}
	if len(f.Phases) == 0 {
		return nil, fmt.Errorf("phases: %s has no phases defined", path)
	}
	phases := make([]Phase, 0, len(f.Phases))
	for i, e := range f.Phases {
		d, err := time.ParseDuration(e.Duration)
		if err != nil {
			return nil, fmt.Errorf("phases: entry %d: invalid duration %q: %w", i, e.Duration, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("phases: entry %d: duration must be >= 0, got %s", i, e.Duration)
		}
		if d == 0 && i != len(f.Phases)-1 {
			return nil, fmt.Errorf("phases: entry %d: duration=0 (terminal) must be the last entry", i)
		}
		if d > 0 && e.Ratio <= 0 {
			return nil, fmt.Errorf("phases: entry %d: ratio must be > 0, got %g", i, e.Ratio)
		}
		phases = append(phases, Phase{Duration: d, Ratio: e.Ratio})
	}
	return &PhaseTracker{phases: phases}, nil
}

// PhaseTracker tracks the current position in the phase sequence and
// returns a cumulative multiplier to apply to the original nominal RPM.
type PhaseTracker struct {
	phases    []Phase
	startTime time.Time
	started   bool
	lastPhase int // last reported phase index, for transition logging
}

// GetMultiplier returns the current cumulative RPM multiplier and the 1-based
// index of the active phase. It initialises the clock on the first call.
func (pt *PhaseTracker) GetMultiplier() (float64, int) {
	if !pt.started {
		pt.startTime = time.Now()
		pt.started = true
	}
	elapsed := time.Since(pt.startTime)

	cumMult := 1.0
	cumTime := time.Duration(0)

	for i, p := range pt.phases {
		phaseNum := i + 1

		// Terminal phase: hold at cumMult forever.
		if p.Duration == 0 {
			pt.logTransition(phaseNum, cumMult)
			return cumMult, phaseNum
		}

		phaseEnd := cumTime + p.Duration
		if elapsed < phaseEnd {
			// We are inside this phase.
			pt.logTransition(phaseNum, cumMult)
			fraction := float64(elapsed-cumTime) / float64(p.Duration)
			endMult := cumMult * p.Ratio
			return cumMult + fraction*(endMult-cumMult), phaseNum
		}

		// Phase fully elapsed: apply its full ratio and advance.
		cumMult *= p.Ratio
		cumTime = phaseEnd
	}

	// Past all phases: hold at final cumMult.
	finalPhase := len(pt.phases)
	pt.logTransition(finalPhase, cumMult)
	return cumMult, finalPhase
}

// logTransition prints a message when the active phase changes.
func (pt *PhaseTracker) logTransition(phaseNum int, mult float64) {
	if phaseNum != pt.lastPhase {
		fmt.Printf("phases: entering phase %d (multiplier=%.4f)\n", phaseNum, mult)
		pt.lastPhase = phaseNum
	}
}

// LogConfig prints the parsed phase sequence to stdout.
func (pt *PhaseTracker) LogConfig() {
	fmt.Printf("phases: loaded %d phase(s):\n", len(pt.phases))
	cumMult := 1.0
	for i, p := range pt.phases {
		if p.Duration == 0 {
			fmt.Printf("  phase %d: hold forever (multiplier=%.4f)\n", i+1, cumMult)
		} else {
			endMult := cumMult * p.Ratio
			fmt.Printf("  phase %d: duration=%s ratio=%.4f multiplier %.4f -> %.4f\n",
				i+1, p.Duration, p.Ratio, cumMult, endMult)
			cumMult = endMult
		}
	}
}
```

- [ ] **Step 2: Build and tidy modules**

`gopkg.in/yaml.v3` is already in the module graph as an indirect dep; adding a direct import promotes it. Run:

```bash
go build ./...
go mod tidy
```

Expected: `go build` produces no output. `go mod tidy` moves `gopkg.in/yaml.v3` from `// indirect` to a direct dependency in `go.mod`.

- [ ] **Step 3: Commit**

```bash
git add pkg/loademulator/phases.go go.mod go.sum
git commit -m "feat: add PhaseTracker for time-based nominal RPM phases"
```

---

## Task 3: Integrate phase tracker into LoadEmulator

**Files:**
- Modify: `pkg/loademulator/loademulator.go`

- [ ] **Step 1: Add `tracker` and `originalNominalRPM` fields to `LoadEmulator`**

Replace the struct definition (lines 30–36):

```go
// Load emulator
type LoadEmulator struct {
	kubeClient          *kubernetes.Clientset
	interval            time.Duration
	alpha               float64
	theta               float64
	skew                float64
	tracker             *PhaseTracker       // nil when phases are disabled
	originalNominalRPM  map[string]float64  // baseline nominal RPM per deployment (namespace/name)
}
```

- [ ] **Step 2: Update `NewLoadEmulator` to accept and store a tracker**

Replace the `NewLoadEmulator` signature and body (lines 39–57):

```go
// NewLoadEmulator creates a new load emulator. tracker may be nil (phases disabled).
func NewLoadEmulator(intervalSec int, alpha, theta, skew float64, tracker *PhaseTracker) (loadEmulator *LoadEmulator, err error) {
	if intervalSec <= 0 || alpha < 0 || alpha > 1 || theta < 0 || theta > 1 || skew < 0 || skew > 1 {
		return nil, fmt.Errorf("%s", "invalid input: interval="+strconv.Itoa(intervalSec)+
			", alpha="+strconv.FormatFloat(alpha, 'f', 3, 64)+
			", theta="+strconv.FormatFloat(theta, 'f', 3, 64)+
			", skew="+strconv.FormatFloat(skew, 'f', 3, 64))
	}
	var kubeClient *kubernetes.Clientset
	if kubeClient, err = ctrl.GetKubeClient(); err == nil {
		return &LoadEmulator{
			kubeClient:         kubeClient,
			interval:           time.Duration(intervalSec) * time.Second,
			alpha:              alpha,
			theta:              theta,
			skew:               skew,
			tracker:            tracker,
			originalNominalRPM: make(map[string]float64),
		}, nil
	}
	return nil, err
}
```

- [ ] **Step 3: Update `Run()` to apply phase logic per deployment**

Replace the `Run()` function (lines 60–104) with the version below. The key changes are:
1. Capture `originalNominalRPM` on first encounter.
2. Compute `adjustedNomRPM` via the tracker multiplier.
3. Write `adjustedNomRPM` back to the deployment's `nominal.rpm` label.
4. Pass `adjustedNomRPM` to `perturbLoad()` instead of `nomRPM`.
5. Log the multiplier and phase index when phases are active.

```go
// run the load emulator
func (lg *LoadEmulator) Run() {
	for {
		// get deployments
		labelSelector := ctrl.KeyManaged + "=true"
		deps, err := lg.kubeClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector})
		if err != nil {
			fmt.Println(err)
			time.Sleep(time.Duration(lg.interval))
			continue
		}

		// compute phase multiplier once per cycle (same for all deployments)
		multiplier := 1.0
		phaseIdx := 0
		if lg.tracker != nil {
			multiplier, phaseIdx = lg.tracker.GetMultiplier()
		}

		// update deployments
		for _, d := range deps.Items {
			depKey := d.Namespace + "/" + d.Name

			curRPM, _ := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 64)
			curInTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyInTokens])
			curOutTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyOutTokens])

			nomRPM, _ := strconv.ParseFloat(d.Labels[ctrl.KeyNominalArrivalRate], 64)
			nomInTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyNominalInTokens])
			nomOutTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyNominalOutTokens])

			// capture original nominal RPM on first encounter
			if _, seen := lg.originalNominalRPM[depKey]; !seen {
				lg.originalNominalRPM[depKey] = nomRPM
			}

			// apply phase multiplier to original baseline
			adjustedNomRPM := lg.originalNominalRPM[depKey] * multiplier

			if lg.tracker != nil {
				fmt.Printf("deployment %s: phase=%d mult=%.4f nomRPM=%.4f\n",
					depKey, phaseIdx, multiplier, adjustedNomRPM)
			}

			// update nominal.rpm label to reflect current phase-adjusted value
			d.Labels[ctrl.KeyNominalArrivalRate] = fmt.Sprintf("%.4f", adjustedNomRPM)

			// perturb arrival rates and number of tokens randomly
			lg.perturbLoad(&curRPM, &curInTokens, &curOutTokens, adjustedNomRPM, nomInTokens, nomOutTokens)

			// update deployment labels
			d.Labels[ctrl.KeyArrivalRate] = fmt.Sprintf("%.4f", curRPM)
			d.Labels[ctrl.KeyInTokens] = fmt.Sprintf("%d", curInTokens)
			d.Labels[ctrl.KeyOutTokens] = fmt.Sprintf("%d", curOutTokens)
			if _, err := lg.kubeClient.AppsV1().Deployments(d.Namespace).Update(context.TODO(), &d, metav1.UpdateOptions{}); err != nil {
				fmt.Println(err)
				continue
			}

			// update pod labels
			selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()
			if err := lg.updatePodLabels(d.Namespace, selectorStr, d.UID, curRPM, curInTokens, curOutTokens); err != nil {
				fmt.Println(err)
			}
		}
		fmt.Printf("%d deployment(s) updated\n", len(deps.Items))
		fmt.Println("Waiting " + lg.interval.String() + "...")
		time.Sleep(time.Duration(lg.interval))
	}
}
```

- [ ] **Step 4: Build to verify no compile errors**

```bash
go build ./...
```

Expected: no output, exit 0.

- [ ] **Step 5: Commit**

```bash
git add pkg/loademulator/loademulator.go
git commit -m "feat: integrate PhaseTracker into LoadEmulator.Run()"
```

---

## Task 4: Wire env var and tracker in main

**Files:**
- Modify: `cmd/loademulator/main.go`

- [ ] **Step 1: Update `main.go` to read `INFERNO_LOAD_PHASES`, construct the tracker, and pass it to `NewLoadEmulator`**

Replace the entire file content:

```go
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	"github.com/llm-inferno/control-loop/pkg/loademulator"
)

var (
	DefaultIntervalSec int     = 20
	DefaultAlpha       float64 = 0.1
	DefaultTheta       float64 = 0.2
	DefaultSkew        float64 = 0.3
)

func main() {
	// provide help
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("Env vars: " +
			ctrl.LoadIntervalEnvName + " " +
			ctrl.LoadAlphaEnvName + " " +
			ctrl.LoadThetaEnvName + " " +
			ctrl.LoadSkewEnvName + " " +
			ctrl.LoadPhasesEnvName + " " +
			ctrl.StartupDelayEnvName)
		return
	}

	// get config from env vars (fall back to defaults)
	interval := DefaultIntervalSec
	alpha := DefaultAlpha
	theta := DefaultTheta
	skew := DefaultSkew

	if s := os.Getenv(ctrl.LoadIntervalEnvName); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadIntervalEnvName + ": " + s)
			return
		}
		interval = v
	}
	if s := os.Getenv(ctrl.LoadAlphaEnvName); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadAlphaEnvName + ": " + s)
			return
		}
		alpha = v
	}
	if s := os.Getenv(ctrl.LoadThetaEnvName); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadThetaEnvName + ": " + s)
			return
		}
		theta = v
	}
	if s := os.Getenv(ctrl.LoadSkewEnvName); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadSkewEnvName + ": " + s)
			return
		}
		skew = v
	}

	ctrl.StartupDelay = time.Duration(ctrl.DefaultStartupDelaySec) * time.Second
	if s := os.Getenv(ctrl.StartupDelayEnvName); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.StartupDelayEnvName + ": " + s)
			return
		}
		ctrl.StartupDelay = time.Duration(v) * time.Second
	}

	// load optional phase config
	var tracker *loademulator.PhaseTracker
	if phasesPath := os.Getenv(ctrl.LoadPhasesEnvName); phasesPath != "" {
		var err error
		tracker, err = loademulator.LoadPhasesFromFile(phasesPath)
		if err != nil {
			fmt.Println(err)
			return
		}
		if tracker != nil {
			tracker.LogConfig()
		}
	}

	fmt.Println("Running with interval=" + strconv.Itoa(interval) + "(sec), alpha=" + strconv.FormatFloat(alpha, 'f', 3, 64) +
		", theta=" + strconv.FormatFloat(theta, 'f', 3, 64) +
		", skew=" + strconv.FormatFloat(skew, 'f', 3, 64) +
		", startupDelay=" + ctrl.StartupDelay.String())

	// run emulator
	lg, err := loademulator.NewLoadEmulator(interval, alpha, theta, skew, tracker)
	if err != nil {
		fmt.Println(err)
		return
	}
	lg.Run()
}
```

- [ ] **Step 2: Build to verify no compile errors**

```bash
go build ./...
```

Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add cmd/loademulator/main.go
git commit -m "feat: read INFERNO_LOAD_PHASES and wire PhaseTracker in loademulator main"
```

---

## Task 5: Add sample phase config

**Files:**
- Create: `sample-data/load-phases.yaml`

- [ ] **Step 1: Create `sample-data/load-phases.yaml`**

```yaml
# Load emulator phase sequence.
# Each phase linearly ramps the nominal RPM from its start value to ratio * start value.
# Ratios are chained: each ratio is relative to the nominal at the START of that phase.
# duration uses Go time.ParseDuration syntax: 2m, 90s, 1h30m, etc.
# duration: 0s on the last phase means hold the final value forever.
phases:
  - duration: 2m
    ratio: 1.0    # hold flat at baseline for 2 minutes
  - duration: 5m
    ratio: 3.0    # ramp up to 3x baseline over 5 minutes
  - duration: 2m
    ratio: 1.0    # hold at 3x for 2 minutes
  - duration: 5m
    ratio: 0.333  # ramp back down to ~1x (0.333 * 3x = ~1x) over 5 minutes
  - duration: 0s  # hold at final value forever
```

- [ ] **Step 2: Commit**

```bash
git add sample-data/load-phases.yaml
git commit -m "feat: add sample load phase config"
```

---

## Task 6: Update Kubernetes deployment YAML

**Files:**
- Modify: `yamls/deploy/load-emulator.yaml`

- [ ] **Step 1: Add ConfigMap volume, volume mount, and `INFERNO_LOAD_PHASES` env var**

Replace the entire file content with:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: load-emulator
  namespace: inferno
spec:
  serviceAccountName: inferno
  containers:
  - name: loademulator
    image: quay.io/atantawi/inferno-loop:latest
    imagePullPolicy: IfNotPresent
    command: ["loademulator"]
    env:
    - name: INFERNO_LOAD_INTERVAL
      value: "20"
    - name: INFERNO_LOAD_ALPHA
      value: "0.1"
    - name: INFERNO_LOAD_THETA
      value: "0.2"
    - name: INFERNO_LOAD_SKEW
      value: "0.3"
    - name: INFERNO_STARTUP_DELAY
      value: "60"
    - name: INFERNO_LOAD_PHASES
      value: ""   # set to /etc/loadphases/phases.yaml to enable phase sequence
    volumeMounts:
    - name: load-phases-config
      mountPath: /etc/loadphases
      readOnly: true
    resources:
      requests:
        memory: "512Mi"
        cpu: "100m"
      limits:
        memory: "1Gi"
        cpu: "500m"
  volumes:
  - name: load-phases-config
    configMap:
      name: load-phases-config
      optional: true   # pod starts normally even if the ConfigMap does not exist
```

Note: the CPU limit was also corrected from `"500"` (invalid) to `"500m"`.

- [ ] **Step 2: Build Docker image to ensure the binary still compiles**

```bash
docker build -t quay.io/atantawi/inferno-loop:latest . --load
```

Expected: build succeeds, image tagged.

- [ ] **Step 3: Commit**

```bash
git add yamls/deploy/load-emulator.yaml
git commit -m "feat: add optional load-phases-config volume to load-emulator pod"
```

---

## Task 7: Update kind-deploy.sh

**Files:**
- Modify: `scripts/kind-deploy.sh`

- [ ] **Step 1: Add optional ConfigMap creation step before the load emulator deploy**

Replace the section at the bottom of `kind-deploy.sh` (the `echo "==> Deploying load emulator"` block and everything after the workloads section) with:

```bash
echo "==> Deploying load emulator"
PHASES_FILE="$REPO_ROOT/sample-data/load-phases.yaml"
if [ -f "$PHASES_FILE" ]; then
  echo "    Creating load-phases-config ConfigMap from $PHASES_FILE"
  kubectl create configmap load-phases-config -n inferno \
    --from-file=phases.yaml="$PHASES_FILE" \
    --save-config --dry-run=client -o yaml | kubectl apply -f -
else
  echo "    No load-phases.yaml found; skipping load-phases-config ConfigMap (phases disabled)"
fi
kubectl apply -f "$REPO_ROOT/yamls/deploy/load-emulator.yaml"

echo ""
echo "==> Done. Watch controller logs with:"
echo "    kubectl logs -f -n inferno deployment/inferno -c controller"
```

- [ ] **Step 2: Commit**

```bash
git add scripts/kind-deploy.sh
git commit -m "feat: create optional load-phases-config ConfigMap in kind-deploy.sh"
```

---

## Task 8: Update CLAUDE.md documentation

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add `INFERNO_LOAD_PHASES` to the Environment Variables table**

In the `## Environment Variables` table, add a new row after `INFERNO_LOAD_SKEW`:

```markdown
| `INFERNO_LOAD_PHASES` | `""` (disabled) | Path to YAML phase config file for the load emulator. When set, the nominal RPM follows the configured phase sequence (linear ramp between phases). Empty = static nominal (current behavior). |
```

- [ ] **Step 2: Add a note about `load-phases.yaml` under Data Files**

In the `## Data Files` section, add:

```markdown
- `load-phases.yaml` — optional load emulator phase sequence (see `sample-data/load-phases.yaml` for format); delivered as the `load-phases-config` ConfigMap
```

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: document INFERNO_LOAD_PHASES and load-phases.yaml"
```

---

## Verification

After all tasks are complete, verify end-to-end:

**1. Local build:**
```bash
go build ./...
```
Expected: clean build, no errors.

**2. Phase startup logging smoke test:**

Build the binary and run it with a minimal phase config. The binary logs the phase sequence at startup before attempting to connect to Kubernetes, so the output is visible regardless of cluster availability.

```bash
go build -o /tmp/loademulator ./cmd/loademulator

cat > /tmp/phases-test.yaml << 'EOF'
phases:
  - duration: 5m
    ratio: 2.0
  - duration: 0s
EOF

INFERNO_LOAD_PHASES=/tmp/phases-test.yaml /tmp/loademulator
```

Expected first lines of output (before any k8s error):
```
phases: loaded 2 phase(s):
  phase 1: duration=5m0s ratio=2.0000 multiplier 1.0000 -> 2.0000
  phase 2: hold forever (multiplier=2.0000)
Running with interval=20(sec), alpha=0.100, theta=0.200, skew=0.300, startupDelay=0s
```

**3. Validation error test:**

```bash
cat > /tmp/bad-phases.yaml << 'EOF'
phases:
  - duration: 0s
  - duration: 5m
    ratio: 2.0
EOF

INFERNO_LOAD_PHASES=/tmp/bad-phases.yaml /tmp/loademulator
```

Expected: prints error and exits immediately:
```
phases: entry 0: duration=0 (terminal) must be the last entry
```

**4. Kubernetes integration (kind cluster):**

```bash
scripts/kind-deploy.sh
kubectl logs -f -n inferno pod/load-emulator
```

Expected: startup log shows the phase sequence; each cycle prints `phase=N mult=X.XXXX` per deployment; the `nominal.rpm` label increases/decreases across cycles matching the ramp.

Verify the nominal label is being updated:
```bash
kubectl get deployment -n infer -o jsonpath='{range .items[*]}{.metadata.name}{" nominal.rpm="}{.metadata.labels.inferno\.server\.load\.nominal\.rpm}{"\n"}{end}'
```

Run this command twice, ~10s apart, while in a ramp phase. The value should change.
