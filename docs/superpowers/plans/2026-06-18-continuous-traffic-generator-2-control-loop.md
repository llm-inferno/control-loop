# Continuous Traffic Generator — Plan 2 of 2: control-loop

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Collector read each pod's latest completed evaluation result over `GET /latest` (non-blocking, branchless, with a coherence check), have the Actuator publish the allocation (`accelerator` + `maxbatchsize`) onto running pods so the generator can read it, and wire the manifests for continuous mode.

**Architecture:** The Collector replaces its `POST /simulate` + poll with a single `GET /latest` per pod, decodes the self-describing envelope from Plan 1, and verifies `effectiveInput.concurrency` matches the in-force `M*` (else skips the pod as stale). The Actuator additionally patches running pods' allocation labels each cycle. Manifests add a downward-API labels projection mounted on the server-sim container and enable continuous mode.

**Tech Stack:** Go, gin, client-go (incl. `fake` clientset for tests), Kubernetes manifests.

**Repo / working directory:** `/Users/tantawi/Projects/llm-inferno/control-loop`. Branch `feat/continuous-traffic-generator` already exists (created when the spec was committed) — work on it.

**Depends on:** Plan 1 (server-sim) merged/deployed — the `/latest` envelope contract `{ "effectiveInput": <ProblemData>, "result": <AnalysisData>, "completedAt": <string> }` must be live.

## Global Constraints

- Go module: `github.com/llm-inferno/control-loop`. Run tests with `go test ./...`.
- The Collector must remain **branchless across backends** — no `inferno.server.evaluator` special-casing in the new per-pod path. Saturation handling now lives in server-sim (Plan 1).
- `effectiveInput` JSON decodes into the existing `simRequest` type (its tags already match `ProblemData`). `result` decodes into the existing `simResult` type (tags match `AnalysisData`).
- Coherence key: `effectiveInput.maxConcurrency` vs the in-force `maxbatchsize` (deployment label, which the Actuator also writes to pods). Mismatch ⇒ treat the pod as stale and skip it (same handling as a failed sim / cold-start 404).
- Label keys are defined in `pkg/controller/defaults.go`: `KeyMaxBatchSize`, `KeyAccelerator`, `KeyServerModel`, `KeyArrivalRate`, `KeyInTokens`, `KeyOutTokens`, `KeyServerClass`, `ReplicaNameSeparator`, `ServerSimPort`.
- Window upper-bound invariant: `warmupSec + maxWindowSec ≤ INFERNO_CONTROL_PERIOD − slack` for vllm-server. This is an operational constraint documented in Task 5, not enforced in code (the controller cannot see the evaluator's window config).

---

### Task 1: Collector `getLatest` + envelope decode

**Files:**
- Modify: `pkg/collector/serversim.go`
- Test: `pkg/collector/serversim_test.go` (create)

**Interfaces:**
- Produces:
  - `type latestEnvelope struct { EffectiveInput simRequest \`json:"effectiveInput"\`; Result simResult \`json:"result"\`; CompletedAt string \`json:"completedAt"\` }`
  - `func parseLatest(data []byte) (*latestEnvelope, error)` — decodes the `/latest` body.
  - `func getLatest(kubeClient *kubernetes.Clientset, namespace, podName string, port int) (*latestEnvelope, error)` — `GET /latest` via the k8s proxy with a short timeout; any non-200 (incl. cold-start 404) returns an error so the caller skips the pod.
- Keep `simRequest` and `simResult` (now reused by the envelope). `simulatePod`, `simJobResponse`, `simPollInitial`, and the poll loop are **removed**.

- [ ] **Step 1: Write the failing test**

```go
package collector

import "testing"

func TestParseLatest(t *testing.T) {
	body := []byte(`{
		"effectiveInput": {"RPS": 5, "maxConcurrency": 32, "avgInputTokens": 1024, "avgOutputTokens": 512, "accelerator":"H100","model":"m"},
		"result": {"throughput": 4.5, "avgTTFT": 120, "avgITL": 11, "maxRPS": 6, "saturation": ""},
		"completedAt": "2026-06-18T10:00:00Z"
	}`)
	env, err := parseLatest(body)
	if err != nil {
		t.Fatalf("parseLatest: %v", err)
	}
	if env.EffectiveInput.MaxConcurrency != 32 || env.EffectiveInput.RPS != 5 {
		t.Fatalf("effectiveInput wrong: %+v", env.EffectiveInput)
	}
	if env.Result.AvgITL != 11 || env.Result.Throughput != 4.5 {
		t.Fatalf("result wrong: %+v", env.Result)
	}
	if env.CompletedAt == "" {
		t.Fatal("completedAt empty")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/collector/ -run TestParseLatest -v`
Expected: compile failure — `parseLatest`/`latestEnvelope` undefined.

- [ ] **Step 3: Implement in `pkg/collector/serversim.go`**

Replace the file's `simJobResponse`, `simulatePod`, `simPollInitial` (and the poll loop) with:

```go
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/client-go/kubernetes"
)

// SimulateTimeoutEnvName overrides the per-pod /latest read timeout (seconds).
// Default 30s. (Historically this bounded the blocking /simulate window; in
// continuous mode /latest returns immediately, so the default is ample.)
const SimulateTimeoutEnvName = "INFERNO_SIMULATE_TIMEOUT_SEC"

const defaultSimTimeout = 30 * time.Second

var simTimeout = defaultSimTimeout

func init() {
	if v := os.Getenv(SimulateTimeoutEnvName); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			simTimeout = time.Duration(secs) * time.Second
		} else {
			fmt.Printf("collector: invalid %s=%q; using default %s\n", SimulateTimeoutEnvName, v, defaultSimTimeout)
		}
	}
}

type simRequest struct {
	RPS             float32 `json:"RPS"`
	MaxConcurrency  int     `json:"maxConcurrency"`
	AvgInputTokens  float32 `json:"avgInputTokens"`
	AvgOutputTokens float32 `json:"avgOutputTokens"`
	Accelerator     string  `json:"accelerator"`
	Model           string  `json:"model"`
}

type simResult struct {
	Throughput  float32 `json:"throughput"`
	AvgRespTime float32 `json:"avgRespTime"`
	AvgWaitTime float32 `json:"avgWaitTime"`
	AvgTTFT     float32 `json:"avgTTFT"`
	AvgITL      float32 `json:"avgITL"`
	MaxRPS      float32 `json:"maxRPS"`
	Saturation  string  `json:"saturation,omitempty"`
}

// latestEnvelope is the self-describing result served by server-sim GET /latest.
type latestEnvelope struct {
	EffectiveInput simRequest `json:"effectiveInput"`
	Result         simResult  `json:"result"`
	CompletedAt    string     `json:"completedAt"`
}

func parseLatest(data []byte) (*latestEnvelope, error) {
	var env latestEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode /latest: %w", err)
	}
	return &env, nil
}

// getLatest reads the most-recent completed evaluation result from the server-sim
// sidecar via the k8s API-server proxy. Non-blocking: a cold-start 404 (no result
// yet) or any transport error is returned so the caller skips the pod this cycle.
func getLatest(kubeClient *kubernetes.Clientset, namespace, podName string, port int) (*latestEnvelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simTimeout)
	defer cancel()

	data, err := kubeClient.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", podName, port)).
		SubResource("proxy").
		Suffix("/latest").
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("GET /latest: %w", err)
	}
	return parseLatest(data)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/collector/ -run TestParseLatest -v`
Expected: PASS. (The package will not fully build until Task 2 removes the `simulatePod` callers — that is the next task.)

- [ ] **Step 5: Commit**

```bash
git add pkg/collector/serversim.go pkg/collector/serversim_test.go
git commit -m "feat(collector): GET /latest envelope read (replaces blocking poll)"
```

---

### Task 2: Collector per-pod path — build replicaSpec from the envelope + coherence check

**Files:**
- Create: `pkg/collector/replicaspec.go`
- Modify: `pkg/collector/handlers.go`
- Test: `pkg/collector/replicaspec_test.go`

**Interfaces:**
- Consumes: `latestEnvelope` (Task 1), `config.ServerSpec`/`config.AllocationData`/`config.ServerLoadSpec` (`optimizer-light/pkg/config`), `ctrl.ReplicaNameSeparator`.
- Produces: `func buildReplicaSpec(serverName, podName, class, model string, maxQueueSize, inForceMaxBatch int, accelerator string, env *latestEnvelope) (config.ServerSpec, bool)` — maps the envelope to a per-pod spec; returns `ok=false` when `env==nil` or `env.EffectiveInput.MaxConcurrency != inForceMaxBatch` (stale).

Field mapping:
- `ITLAverage = env.Result.AvgITL`, `TTFTAverage = env.Result.AvgTTFT`.
- `Load.ArrivalRate = env.EffectiveInput.RPS * 60` (offered load actually run, RPM).
- `Load.Throughput = env.Result.Throughput * 60` (goodput, RPM).
- `Load.AvgInTokens = int(env.EffectiveInput.AvgInputTokens)`, `Load.AvgOutTokens = int(env.EffectiveInput.AvgOutputTokens)`.
- `MaxBatch = inForceMaxBatch`, `NumReplicas = 1`, `Accelerator = accelerator`.

- [ ] **Step 1: Write the failing test**

```go
package collector

import "testing"

func TestBuildReplicaSpecCoherent(t *testing.T) {
	env := &latestEnvelope{
		EffectiveInput: simRequest{RPS: 5, MaxConcurrency: 32, AvgInputTokens: 1024, AvgOutputTokens: 512},
		Result:         simResult{AvgITL: 11, AvgTTFT: 120, Throughput: 4},
	}
	spec, ok := buildReplicaSpec("srv", "pod-1", "Bronze", "m", 64, 32, "H100", env)
	if !ok {
		t.Fatal("ok=false, want true (concurrency matches)")
	}
	if spec.Name != "srv/pod-1" {
		t.Fatalf("name = %q", spec.Name)
	}
	if spec.CurrentAlloc.ITLAverage != 11 || spec.CurrentAlloc.TTFTAverage != 120 {
		t.Fatalf("latency wrong: %+v", spec.CurrentAlloc)
	}
	if spec.CurrentAlloc.Load.ArrivalRate != 300 || spec.CurrentAlloc.Load.Throughput != 240 {
		t.Fatalf("load wrong: %+v", spec.CurrentAlloc.Load) // 5*60=300, 4*60=240
	}
	if spec.CurrentAlloc.MaxBatch != 32 || spec.CurrentAlloc.NumReplicas != 1 {
		t.Fatalf("alloc wrong: %+v", spec.CurrentAlloc)
	}
}

func TestBuildReplicaSpecStaleConcurrencyMismatch(t *testing.T) {
	env := &latestEnvelope{EffectiveInput: simRequest{MaxConcurrency: 32}}
	if _, ok := buildReplicaSpec("srv", "p", "c", "m", 64, 128 /*in-force differs*/, "H100", env); ok {
		t.Fatal("ok=true, want false (stale: 32 != 128)")
	}
}

func TestBuildReplicaSpecNilEnv(t *testing.T) {
	if _, ok := buildReplicaSpec("srv", "p", "c", "m", 64, 32, "H100", nil); ok {
		t.Fatal("ok=true, want false (nil env)")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/collector/ -run TestBuildReplicaSpec -v`
Expected: compile failure — `buildReplicaSpec` undefined.

- [ ] **Step 3: Implement `pkg/collector/replicaspec.go`**

```go
package collector

import (
	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// buildReplicaSpec maps a /latest envelope to a per-pod ServerSpec. It enforces
// the causal-coherence check: the window's effective concurrency must equal the
// allocation currently in force. A mismatch (the generator has not yet produced
// a window under the new M*) means the observation is stale — return ok=false so
// the caller skips the pod, exactly like a cold-start 404.
func buildReplicaSpec(serverName, podName, class, model string, maxQueueSize, inForceMaxBatch int, accelerator string, env *latestEnvelope) (config.ServerSpec, bool) {
	if env == nil || env.EffectiveInput.MaxConcurrency != inForceMaxBatch {
		return config.ServerSpec{}, false
	}
	return config.ServerSpec{
		Name:         serverName + ctrl.ReplicaNameSeparator + podName,
		Class:        class,
		Model:        model,
		MaxQueueSize: maxQueueSize,
		CurrentAlloc: config.AllocationData{
			Accelerator: accelerator,
			MaxBatch:    inForceMaxBatch,
			NumReplicas: 1,
			ITLAverage:  env.Result.AvgITL,
			TTFTAverage: env.Result.AvgTTFT,
			Load: config.ServerLoadSpec{
				ArrivalRate:  env.EffectiveInput.RPS * 60,
				Throughput:   env.Result.Throughput * 60,
				AvgInTokens:  int(env.EffectiveInput.AvgInputTokens),
				AvgOutTokens: int(env.EffectiveInput.AvgOutputTokens),
			},
		},
	}, true
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/collector/ -run TestBuildReplicaSpec -v`
Expected: PASS.

- [ ] **Step 5: Rewrite the per-pod block in `pkg/collector/handlers.go`**

Replace the fan-out region (the `runningPods`/`podEntry` loop building `simRequest`, the parallel `simulatePod` calls, the saturation re-simulation branch at lines ~162-271) with a `getLatest`-based fan-out. The replacement, inside the `else` branch after `pods, err := ...List(...)`:

```go
				// collect running, ready pods owned by this deployment
				var runningPods []corev1.Pod
				for _, p := range pods.Items {
					if p.Status.Phase != corev1.PodRunning {
						continue
					}
					owned := false
					for _, owner := range p.OwnerReferences {
						if _, ok := rsUIDs[owner.UID]; ok {
							owned = true
							break
						}
					}
					if !owned {
						continue
					}
					if !ctrl.IsPodReady(p.Status.StartTime) {
						fmt.Printf("pod %s: skipping (within startup delay)\n", p.Name)
						continue
					}
					runningPods = append(runningPods, p)
				}
				numReplicas = int(*d.Spec.Replicas)

				// fan-out: read each pod's latest completed result (non-blocking)
				envs := make([]*latestEnvelope, len(runningPods))
				errs := make([]error, len(runningPods))
				var wg sync.WaitGroup
				for i, p := range runningPods {
					wg.Add(1)
					go func(i int, p corev1.Pod) {
						defer wg.Done()
						envs[i], errs[i] = getLatest(KubeClient, p.Namespace, p.Name, ctrl.ServerSimPort)
					}(i, p)
				}
				wg.Wait()

				// aggregate
				var weightedITL, weightedTTFT float64
				for i, p := range runningPods {
					if errs[i] != nil {
						fmt.Printf("pod %s: no result this cycle (%v); skipping\n", p.Name, errs[i])
						continue
					}
					spec, ok := buildReplicaSpec(serverName, p.Name,
						d.Labels[ctrl.KeyServerClass], d.Labels[ctrl.KeyServerModel],
						maxQueueSize, maxBatchSize, d.Labels[ctrl.KeyAccelerator], envs[i])
					if !ok {
						fmt.Printf("pod %s: stale result (effectiveConcurrency=%d != inForce=%d); holding\n",
							p.Name, envs[i].EffectiveInput.MaxConcurrency, maxBatchSize)
						continue
					}
					w := float64(spec.CurrentAlloc.Load.Throughput)
					fmt.Printf("pod %s: TTFT=%.1fms ITL=%.1fms throughputRPM=%.2f\n",
						p.Name, spec.CurrentAlloc.TTFTAverage, spec.CurrentAlloc.ITLAverage, w)
					weightedITL += float64(spec.CurrentAlloc.ITLAverage) * w
					weightedTTFT += float64(spec.CurrentAlloc.TTFTAverage) * w
					totalThroughputRPM += w
					replicaSpecs = append(replicaSpecs, spec)
				}
				if totalThroughputRPM > 0 {
					itlAvg = float32(weightedITL / totalThroughputRPM)
					ttftAvg = float32(weightedTTFT / totalThroughputRPM)
				}
```

Remove now-unused locals from the old block (the per-pod `rpm`/`inTok`/`outTok` reads and the `simRequest` construction). Keep the surrounding `curAlloc`/`serverSpec` assembly and the deployment-level Prometheus queries unchanged. Ensure imports still include `corev1`, `sync`, `labels`, `types`.

- [ ] **Step 6: Build the package**

Run: `go build ./pkg/collector/ && go vet ./pkg/collector/`
Expected: no errors (no remaining references to `simulatePod`, `simJobResponse`, `simRequest{...}` construction with `RPS:` etc.).

- [ ] **Step 7: Run collector tests**

Run: `go test ./pkg/collector/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/collector/replicaspec.go pkg/collector/replicaspec_test.go pkg/collector/handlers.go
git commit -m "feat(collector): branchless /latest path with coherence check"
```

---

### Task 3: Actuator — publish allocation to running pods

**Files:**
- Create: `pkg/actuator/pod_alloc.go`
- Modify: `pkg/actuator/handlers.go`
- Test: `pkg/actuator/pod_alloc_test.go`

**Interfaces:**
- Consumes: `setPodLabel` (`pkg/actuator/pairing_kube.go`), `ctrl.KeyAccelerator`, `ctrl.KeyMaxBatchSize`.
- Produces: `func patchPodsAllocation(ctx context.Context, kc kubernetes.Interface, ns, depName, accelerator string, maxBatch int) error` — lists running pods by the deployment's selector and sets the `accelerator` + `maxbatchsize` labels on each. Called from the `update` handler after `patchDeployment` for each managed deployment.

- [ ] **Step 1: Write the failing test**

```go
package actuator

import (
	"context"
	"testing"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPatchPodsAllocation(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dep", Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns", Labels: map[string]string{"app": "x"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	kc := fake.NewSimpleClientset(dep, pod)

	if err := patchPodsAllocation(context.Background(), kc, "ns", "dep", "H100", 64); err != nil {
		t.Fatalf("patchPodsAllocation: %v", err)
	}
	got, _ := kc.CoreV1().Pods("ns").Get(context.Background(), "p1", metav1.GetOptions{})
	if got.Labels[ctrl.KeyMaxBatchSize] != "64" || got.Labels[ctrl.KeyAccelerator] != "H100" {
		t.Fatalf("pod labels not set: %v", got.Labels)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/actuator/ -run TestPatchPodsAllocation -v`
Expected: compile failure — `patchPodsAllocation` undefined.

- [ ] **Step 3: Implement `pkg/actuator/pod_alloc.go`**

```go
package actuator

import (
	"context"
	"strconv"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// patchPodsAllocation writes the current allocation (accelerator + maxbatchsize)
// onto each running pod of the deployment, so the server-sim generator can read
// the in-force M* from its downward-API labels volume. Best-effort per pod: a
// transient pod patch error is skipped, not fatal to the cycle.
func patchPodsAllocation(ctx context.Context, kc kubernetes.Interface, ns, depName, accelerator string, maxBatch int) error {
	dep, err := kc.AppsV1().Deployments(ns).Get(ctx, depName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	selector := labels.Set(dep.Spec.Selector.MatchLabels).String()
	pods, err := kc.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		_ = setPodLabel(ctx, kc, ns, p.Name, ctrl.KeyAccelerator, accelerator)
		_ = setPodLabel(ctx, kc, ns, p.Name, ctrl.KeyMaxBatchSize, strconv.Itoa(maxBatch))
	}
	return nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/actuator/ -run TestPatchPodsAllocation -v`
Expected: PASS.

- [ ] **Step 5: Call it from the `update` handler**

In `pkg/actuator/handlers.go`, inside the `for _, u := range updates` loop, after the successful `patchDeployment(...)` call, add (only when an allocation was assigned — skip the zeroed-out set):

```go
		if u.Allocation.NumReplicas > 0 {
			if err := patchPodsAllocation(context.Background(), KubeClient, u.Namespace, u.DeployName,
				u.Allocation.Accelerator, u.Allocation.MaxBatch); err != nil {
				fmt.Printf("srv=[%s/%s]: pod allocation patch warning: %v\n", u.ServerName, u.Namespace, err)
			}
		}
```

(`KubeClient` is the package-level `*kubernetes.Clientset`; `*Clientset` satisfies `kubernetes.Interface`.)

- [ ] **Step 6: Build + test the package**

Run: `go build ./pkg/actuator/ && go test ./pkg/actuator/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/actuator/pod_alloc.go pkg/actuator/pod_alloc_test.go pkg/actuator/handlers.go
git commit -m "feat(actuator): publish allocation (accelerator+maxbatchsize) to running pods"
```

---

### Task 4: Manifests — downward-API labels volume + continuous mode

**Files (modify each):**
- `manifests/qa/dep-qa-granite.yaml`, `manifests/qa/dep-qa-llama.yaml`
- `manifests/blis/dep-blis-granite.yaml`, `manifests/blis/dep-blis-llama.yaml`
- `manifests/vllm-gpu/dep-vllm-qwen-server.yaml`, `manifests/vllm-gpu/dep-vllm-llama-server.yaml`

The edits are identical in shape across files; per-file values (model, accelerator, saturation policy) are noted.

- [ ] **Step 1: Add static workload labels to each pod template**

In each deployment's `spec.template.metadata.labels`, add the model and accelerator labels (the generator needs them; the Load Emulator supplies the load labels at runtime). Example for `dep-qa-granite.yaml` (model `granite_8b`, accelerator `H100`):

```yaml
    metadata:
      labels:
        app: qa-granite
        inferno.server.model: granite_8b
        inferno.server.allocation.accelerator: H100
        inferno.server.allocation.maxbatchsize: "128"
```

Use each file's own `inferno.server.model`, `inferno.server.allocation.accelerator`, and the existing `maxbatchsize` value (qa-granite `128`, qa-llama per file, blis `256`, vllm `128`) as the seed. The vllm-gpu pod templates already carry `inferno.server.vllm-deployment`; keep it and add these three.

- [ ] **Step 2: Add `SERVERSIM_CONTINUOUS` and `SERVERSIM_SATURATION_POLICY` env to the server-sim container**

In the `server-sim` container's `env:` list:

```yaml
        - name: SERVERSIM_CONTINUOUS
          value: "true"
        - name: SERVERSIM_SATURATION_POLICY
          value: "retry-at-lower-load"   # qa + blis
```

For the two `vllm-gpu` files use `value: "pass-through"` instead (a real vLLM cannot manufacture a lower-load reading).

- [ ] **Step 3: Add the downward-API labels volume + mount on the server-sim container**

For **qa** and **blis** files (no existing podinfo volume), add a `volumeMounts` block to the `server-sim` container and a `volumes` entry:

```yaml
        # under server-sim container:
        volumeMounts:
        - name: podinfo
          mountPath: /etc/podinfo
```

```yaml
      # under spec.template.spec.volumes:
      - name: podinfo
        downwardAPI:
          items:
          - path: labels
            fieldRef:
              fieldPath: metadata.labels
```

For the **vllm-gpu** files, the `podinfo` volume already exists (mounted on the `evaluator` container). Add the `labels` item to its `items:` list, and also add the same `volumeMounts` entry (`name: podinfo`, `mountPath: /etc/podinfo`) to the **server-sim** container:

```yaml
        # add to the existing podinfo downwardAPI items:
          - path: labels
            fieldRef:
              fieldPath: metadata.labels
```

- [ ] **Step 4: Validate YAML**

Run: `for f in manifests/qa/dep-qa-*.yaml manifests/blis/dep-blis-*.yaml manifests/vllm-gpu/dep-vllm-*-server.yaml; do echo "== $f =="; python3 -c "import yaml,sys; list(yaml.safe_load_all(open('$f')))" && echo OK; done`
Expected: `OK` for each file (valid YAML).

- [ ] **Step 5: Commit**

```bash
git add manifests/qa/dep-qa-*.yaml manifests/blis/dep-blis-*.yaml manifests/vllm-gpu/dep-vllm-*-server.yaml
git commit -m "feat(manifests): downward-API labels volume + continuous server-sim mode"
```

---

### Task 5: Docs — control-period invariant + behaviour notes

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Document the new model**

Add a subsection under "Known Behaviours and Operational Notes" describing: server-sim continuous mode (one job at a time), the `GET /latest` envelope, the branchless Collector path, saturation policy now in server-sim (`SERVERSIM_SATURATION_POLICY`), the Actuator writing allocation to running pods, and the causal-gating coherence check (Collector compares `effectiveInput.maxConcurrency` to the in-force `maxbatchsize`, skipping stale pods).

Add the env vars to the table: `SERVERSIM_CONTINUOUS`, `SERVERSIM_TICK_SECONDS`, `SERVERSIM_SATURATION_POLICY`, `SERVERSIM_LABELS_DIR`.

State the invariant explicitly: **for the vllm-server backend, `INFERNO_CONTROL_PERIOD` must exceed `warmupSec + maxWindowSec` (from the eval config) plus collect/decide/actuate slack**, so a post-decision window completes within the cycle; otherwise the Collector reports the pod stale until it catches up.

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: continuous traffic generator behaviour + control-period invariant"
```

---

### Task 6: Full build + test sweep

- [ ] **Step 1: Build, vet, test the whole module**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 2: Commit any incidental fixes**

```bash
git add -A && git commit -m "chore(control-loop): build/vet/test clean for continuous mode" || true
```

---

## Self-Review

**Spec coverage** (against `2026-06-18-continuous-traffic-generator-design.md`):
- Collector reads `/latest`, non-blocking, branchless → Tasks 1, 2. ✓
- Cold-start 404 → skip pod → Task 1 (`getLatest` error) + Task 2 (caller skips). ✓
- Build replicaSpec from `effectiveInput` (offered load) + `result` (perf) → Task 2. ✓
- Coherence check (`effectiveInput.concurrency` vs in-force `M*`), stale → skip, logged → Task 2. ✓
- Saturation branch removed from Collector → Task 2 Step 5. ✓
- Actuator writes `M*` (+accelerator) to running pods via same channel → Task 3. ✓
- Downward-API labels volume on server-sim container, all backends → Task 4. ✓
- Continuous mode + per-backend saturation policy env → Task 4 Steps 2. ✓
- Window upper-bound invariant documented → Task 5. ✓
- RBAC: `pods … patch` already granted (`manifests/common/deploy-loop.yaml:17`); `pods/proxy get` already granted for `/latest` (same proxy path as the old `/simulate`) — no RBAC change needed. ✓

**Placeholder scan:** none — every code/manifest step shows the literal content.

**Type consistency:** `latestEnvelope{EffectiveInput simRequest, Result simResult, CompletedAt string}` defined in Task 1, consumed in Task 2; `simRequest`/`simResult` field names match the handler usage. `buildReplicaSpec` signature identical between Task 2 definition and the handler call site. `patchPodsAllocation` signature identical between Task 3 definition and the `update` handler call. `getLatest(KubeClient, ns, pod, ServerSimPort)` matches the `*kubernetes.Clientset` receiver and the existing proxy pattern.

**Cross-plan contract:** the `/latest` JSON keys (`effectiveInput`/`result`/`completedAt`) and `effectiveInput.maxConcurrency` consumed here exactly match Plan 1 Task 8's handler output.
