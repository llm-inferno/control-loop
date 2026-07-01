# llm-d Backend Seam + llmd Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Abstract the control loop's per-deployment sense/actuate operations behind `Sensor`/`Actuator` interfaces with a behavior-preserving `serversim` implementation and a new all-Prometheus `llmd` implementation, so inferno can run standalone against a real llm-d deployment (replicas-only, m\* pinned) for an A/B against WVA.

**Architecture:** The collector and actuator are separate binaries with no runtime channel; the seam is realized as *two* interfaces in a tiny leaf package `pkg/backend`, with the sense implementations living in `pkg/collector` and the actuate implementations in `pkg/actuator` (next to the k8s helpers they reuse). A process-level `INFERNO_BACKEND` env selects `serversim` (default) or `llmd` for every managed deployment that process handles. The `llmd` sensor reads all metrics from the OpenShift Thanos endpoint over an authenticated (bearer-token + CA) Prometheus client; the `llmd` actuator patches `/spec/replicas` only.

**Tech Stack:** Go, `github.com/prometheus/client_golang` (Prometheus API), `k8s.io/client-go`, `github.com/llm-inferno/optimizer-light/pkg/config`, gin.

## Global Constraints

- Module path: `github.com/llm-inferno/control-loop`. Controller package import alias in other packages: `ctrl "github.com/llm-inferno/control-loop/pkg/controller"`.
- Config types come from `github.com/llm-inferno/optimizer-light/pkg/config`: `config.ServerSpec{Name string, Class string, Model string, MaxQueueSize int, CurrentAlloc config.AllocationData}`; `config.AllocationData{Accelerator string, NumReplicas int, MaxBatch int, ITLAverage float32, TTFTAverage float32, AvgConcurrency float32, Load config.ServerLoadSpec}`; `config.ServerLoadSpec{ArrivalRate float32, Throughput float32, AvgInTokens int, AvgOutTokens int}`.
- Existing behavior for `serversim` (backends `vllm-server`/`queue-analysis`/`blis`) MUST NOT change. Verify by `go build ./...`, `go vet ./...`, and a re-run of an existing experiment (qa or blis) before merge.
- The repo currently has no automated tests; add standard Go `_test.go` files. `go test ./...` must pass.
- No new third-party dependencies beyond what `go.mod` already provides (`prometheus/client_golang`, `prometheus/common`, `k8s.io/*` are already imported).
- Every commit message ends with: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- Work on a new branch off `main` (not the docs branch): `feat/llmd-backend`.

---

### Task 1: `pkg/backend` package — interfaces, `DeploymentUpdate`, mode selection

**Files:**
- Create: `pkg/backend/backend.go`
- Create: `pkg/backend/backend_test.go`
- Modify: `pkg/controller/defaults.go` (add const block near the other `*EnvName` consts, ~line 48, and near the Evaluator value consts, ~line 148)
- Modify: `pkg/actuator/update_logic.go` (remove `DeploymentUpdate`, reference `backend.DeploymentUpdate`)
- Modify: `pkg/actuator/handlers.go:40` (`u.Allocation` etc. now on `backend.DeploymentUpdate` — field names unchanged, only the type moves)

**Interfaces:**
- Produces:
  - `backend.Sensor` interface: `Sense(ctx context.Context, dep appsv1.Deployment, kc kubernetes.Interface) (server config.ServerSpec, replicas []config.ServerSpec, ok bool, err error)`
  - `backend.Actuator` interface: `Actuate(ctx context.Context, kc kubernetes.Interface, u DeploymentUpdate) error`
  - `backend.DeploymentUpdate` struct: `{ServerName string; UID string; DeployName string; Namespace string; Allocation config.AllocationData}` (moved verbatim from `pkg/actuator/update_logic.go`)
  - `backend.Mode` type + `backend.ModeFromEnv() Mode` returning `ModeServerSim` (default) or `ModeLLMD`; string values `"serversim"` / `"llmd"`.
- Consumes: `ctrl.BackendEnvName` (new const).

- [ ] **Step 1: Add the new consts to `pkg/controller/defaults.go`**

In the `const (...)` block that holds the `*EnvName` values (right after `CalibPollIntervalSecEnvName`, ~line 48) add:

```go
	// BackendEnvName selects the sense/actuate backend for this process:
	// "serversim" (default) or "llmd". Applies to every managed deployment
	// the process handles.
	BackendEnvName = "INFERNO_BACKEND"

	// Prometheus client configuration (used by the collector). When unset the
	// collector falls back to http://localhost:9090 with no auth (server-sim).
	PrometheusURLEnvName       = "INFERNO_PROMETHEUS_URL"        // e.g. https://thanos-querier.openshift-monitoring.svc:9091
	PrometheusTokenPathEnvName = "INFERNO_PROMETHEUS_TOKEN_PATH" // default: SA token path
	PrometheusCAPathEnvName    = "INFERNO_PROMETHEUS_CA_PATH"    // default: SA CA path
	PrometheusInsecureEnvName  = "INFERNO_PROMETHEUS_INSECURE"   // "true" => skip TLS verify
	PromWindowEnvName          = "INFERNO_PROM_WINDOW"           // PromQL range window, default "1m"
```

In the "Evaluator label values" block (after `EvaluatorBlis`, ~line 148) add:

```go
	EvaluatorLLMD = "llm-d"
```

- [ ] **Step 2: Write the failing test for `ModeFromEnv`**

Create `pkg/backend/backend_test.go`:

```go
package backend

import (
	"os"
	"testing"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
)

func TestModeFromEnv(t *testing.T) {
	cases := map[string]Mode{
		"":          ModeServerSim,
		"serversim": ModeServerSim,
		"llmd":      ModeLLMD,
		"llm-d":     ModeLLMD,
		"LLMD":      ModeLLMD,
		"bogus":     ModeServerSim,
	}
	for in, want := range cases {
		t.Setenv(ctrl.BackendEnvName, in)
		if in == "" {
			os.Unsetenv(ctrl.BackendEnvName)
		}
		if got := ModeFromEnv(); got != want {
			t.Errorf("ModeFromEnv(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./pkg/backend/ -run TestModeFromEnv -v`
Expected: FAIL to compile (`backend` package / `Mode` undefined).

- [ ] **Step 4: Write `pkg/backend/backend.go`**

```go
// Package backend abstracts the per-deployment sense and actuate operations
// so the control loop can run against different serving environments
// (server-sim simulators vs a real llm-d deployment). The collector and
// actuator are separate processes with no shared runtime state, so the seam
// is two interfaces selected independently in each process by INFERNO_BACKEND.
package backend

import (
	"context"
	"os"
	"strings"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	"github.com/llm-inferno/optimizer-light/pkg/config"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes"
)

// Mode is the selected backend for a process.
type Mode string

const (
	ModeServerSim Mode = "serversim"
	ModeLLMD      Mode = "llmd"
)

// ModeFromEnv reads INFERNO_BACKEND. Anything other than an llmd spelling
// ("llmd"/"llm-d", case-insensitive) selects the server-sim backend, so the
// default and any unknown value preserve today's behavior.
func ModeFromEnv() Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ctrl.BackendEnvName))) {
	case "llmd", "llm-d":
		return ModeLLMD
	default:
		return ModeServerSim
	}
}

// Sensor reads one managed Deployment into a deployment-level ServerSpec plus
// one ServerSpec per reporting replica. ok=false means "no usable reading this
// cycle" (the caller drops the deployment's per-replica contribution), distinct
// from err which signals an operational failure.
type Sensor interface {
	Sense(ctx context.Context, dep appsv1.Deployment, kc kubernetes.Interface) (
		server config.ServerSpec, replicas []config.ServerSpec, ok bool, err error)
}

// DeploymentUpdate is the resolved patch target for a single managed server.
type DeploymentUpdate struct {
	ServerName string
	UID        string
	DeployName string
	Namespace  string
	Allocation config.AllocationData
}

// Actuator applies one resolved allocation onto one managed Deployment.
type Actuator interface {
	Actuate(ctx context.Context, kc kubernetes.Interface, u DeploymentUpdate) error
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./pkg/backend/ -run TestModeFromEnv -v`
Expected: PASS.

- [ ] **Step 6: Move `DeploymentUpdate` out of the actuator package**

In `pkg/actuator/update_logic.go`: delete the `DeploymentUpdate` struct definition (lines 11-20) and change `ComputeUpdates` to return `[]backend.DeploymentUpdate`. Full resulting file:

```go
package actuator

import (
	"sort"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// ComputeUpdates produces one DeploymentUpdate per serverMap entry, applying
// the optimizer's allocation when present and the zero allocation otherwise.
//
// The set of updates is exactly serverMap (the Collector's view); allocations
// for server names not in serverMap are dropped because the Actuator has no
// Kube reference for them. Output is sorted by ServerName for stable logging.
func ComputeUpdates(
	allocMap map[string]config.AllocationData,
	serverMap map[string]ctrl.ServerKubeInfo,
) []backend.DeploymentUpdate {
	names := make([]string, 0, len(serverMap))
	for name := range serverMap {
		names = append(names, name)
	}
	sort.Strings(names)

	updates := make([]backend.DeploymentUpdate, 0, len(names))
	for _, name := range names {
		info := serverMap[name]
		alloc, ok := allocMap[name]
		if !ok {
			alloc = config.AllocationData{} // zero value: replicas=0, accelerator="", load=0
		}
		updates = append(updates, backend.DeploymentUpdate{
			ServerName: name,
			UID:        info.UID,
			DeployName: info.Name,
			Namespace:  info.Space,
			Allocation: alloc,
		})
	}
	return updates
}
```

(`pkg/actuator/handlers.go` needs no field changes — `u.ServerName`, `u.DeployName`, `u.Namespace`, `u.Allocation` are identical on the moved type. It will compile once the type is `backend.DeploymentUpdate`.)

- [ ] **Step 7: Build everything and commit**

Run: `go build ./... && go vet ./... && go test ./pkg/backend/ -v`
Expected: build/vet clean, test PASS.

```bash
git add pkg/backend/ pkg/controller/defaults.go pkg/actuator/update_logic.go
git commit -m "feat(backend): add Sensor/Actuator seam, mode selection, move DeploymentUpdate

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Configurable, authenticated Prometheus client

**Files:**
- Create: `pkg/collector/prometheus.go`
- Create: `pkg/collector/prometheus_test.go`
- Modify: `pkg/collector/utils.go` (reimplement `PrometheusQuery` on top of the shared client; keep the same signature so the 3 callers in `handlers.go` are unchanged)

**Interfaces:**
- Consumes: `ctrl.PrometheusURLEnvName`, `ctrl.PrometheusTokenPathEnvName`, `ctrl.PrometheusCAPathEnvName`, `ctrl.PrometheusInsecureEnvName` (Task 1).
- Produces:
  - `func newPromAPI() (v1.API, error)` — builds a Prometheus `v1.API` from env (URL default `http://localhost:9090`, optional bearer token, optional CA / insecure TLS).
  - `func queryVector(ctx context.Context, api v1.API, query string) (model.Vector, error)` — returns the full labeled vector (for llmd per-pod grouping).
  - `func queryScalar(ctx context.Context, api v1.API, query string) (float64, error)` — returns `vector[0].Value` (server-sim's single-value queries).
  - `PrometheusQuery(query string) (float64, error)` — unchanged signature; now builds the env-configured client and calls `queryScalar`.

- [ ] **Step 1: Write the failing test for the token round-tripper**

Create `pkg/collector/prometheus_test.go`:

```go
package collector

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerRoundTripperAddsHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	rt := &bearerRoundTripper{token: "abc123", rt: http.DefaultTransport}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if gotAuth != "Bearer abc123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer abc123")
	}
}

func TestBearerRoundTripperEmptyTokenNoHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	rt := &bearerRoundTripper{token: "", rt: http.DefaultTransport}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/collector/ -run TestBearerRoundTripper -v`
Expected: FAIL to compile (`bearerRoundTripper` undefined).

- [ ] **Step 3: Write `pkg/collector/prometheus.go`**

```go
package collector

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultPrometheusURL = "http://localhost:9090"
	defaultSATokenPath   = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultSACAPath      = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// bearerRoundTripper injects an Authorization: Bearer <token> header (when the
// token is non-empty) and delegates to the wrapped RoundTripper.
type bearerRoundTripper struct {
	token string
	rt    http.RoundTripper
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.rt.RoundTrip(req)
}

// newPromAPI builds a Prometheus v1.API from the environment. Defaults preserve
// server-sim behavior: no env => http://localhost:9090, no token, no custom TLS.
// For an HTTPS endpoint (e.g. OpenShift Thanos) it attaches a bearer token read
// from INFERNO_PROMETHEUS_TOKEN_PATH (default SA token) and trusts the CA at
// INFERNO_PROMETHEUS_CA_PATH (default SA CA), unless INFERNO_PROMETHEUS_INSECURE=true.
func newPromAPI() (v1.API, error) {
	url := os.Getenv(ctrl.PrometheusURLEnvName)
	if url == "" {
		url = defaultPrometheusURL
	}

	var base http.RoundTripper = api.DefaultRoundTripper
	if strings.HasPrefix(url, "https") {
		tlsCfg := &tls.Config{}
		if strings.EqualFold(os.Getenv(ctrl.PrometheusInsecureEnvName), "true") {
			tlsCfg.InsecureSkipVerify = true
		} else {
			caPath := os.Getenv(ctrl.PrometheusCAPathEnvName)
			if caPath == "" {
				caPath = defaultSACAPath
			}
			if pem, err := os.ReadFile(caPath); err == nil {
				pool := x509.NewCertPool()
				if pool.AppendCertsFromPEM(pem) {
					tlsCfg.RootCAs = pool
				}
			}
		}
		base = &http.Transport{TLSClientConfig: tlsCfg}
	}

	// Read a bearer token only when a token file is present (so localhost/http
	// keeps working with no token). Explicit path forces the read.
	tokenPath := os.Getenv(ctrl.PrometheusTokenPathEnvName)
	explicit := tokenPath != ""
	if tokenPath == "" {
		tokenPath = defaultSATokenPath
	}
	var token string
	if b, err := os.ReadFile(tokenPath); err == nil {
		token = strings.TrimSpace(string(b))
	} else if explicit {
		return nil, fmt.Errorf("reading Prometheus token %s: %w", tokenPath, err)
	}

	client, err := api.NewClient(api.Config{
		Address:      url,
		RoundTripper: &bearerRoundTripper{token: token, rt: base},
	})
	if err != nil {
		return nil, fmt.Errorf("creating Prometheus client: %w", err)
	}
	return v1.NewAPI(client), nil
}

// queryVector runs an instant query and returns the full labeled vector.
func queryVector(ctx context.Context, apiv1 v1.API, query string) (model.Vector, error) {
	result, warnings, err := apiv1.Query(ctx, query, metav1.Now().Time)
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus: %w", err)
	}
	if len(warnings) > 0 {
		fmt.Printf("Prometheus query warnings: %v\n", warnings)
	}
	vector, ok := result.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %T", result)
	}
	return vector, nil
}

// queryScalar runs an instant query and returns the first sample's value.
func queryScalar(ctx context.Context, apiv1 v1.API, query string) (float64, error) {
	vector, err := queryVector(ctx, apiv1, query)
	if err != nil {
		return 0, err
	}
	if len(vector) == 0 {
		return 0, fmt.Errorf("no data returned from query")
	}
	return float64(vector[0].Value), nil
}
```

- [ ] **Step 4: Reimplement `PrometheusQuery` in `pkg/collector/utils.go` on the shared client**

Replace the whole file body with:

```go
package collector

import (
	"context"
)

// PrometheusQuery executes a PromQL query against the env-configured Prometheus
// endpoint and returns the first sample's value. Kept for the server-sim sense
// path; the endpoint/auth are configured via INFERNO_PROMETHEUS_* (defaults to
// http://localhost:9090 with no auth).
func PrometheusQuery(query string) (float64, error) {
	apiv1, err := newPromAPI()
	if err != nil {
		return 0, err
	}
	return queryScalar(context.Background(), apiv1, query)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/collector/ -run TestBearerRoundTripper -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/collector/prometheus.go pkg/collector/prometheus_test.go pkg/collector/utils.go
git commit -m "feat(collector): configurable token-authenticated Prometheus client

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: `serversim` Sensor — extract today's sense path (behavior-preserving)

**Files:**
- Create: `pkg/collector/sensor_serversim.go`
- Modify: `pkg/collector/handlers.go` (`collect()` delegates per-deployment work to a `backend.Sensor`)

**Interfaces:**
- Consumes: `backend.Sensor` (Task 1); existing package-private `getLatest`, `buildReplicaSpec`, `selectArrivalRate`, `PrometheusQuery`.
- Produces: `type serversimSensor struct{}` implementing `backend.Sensor`; `func newServerSimSensor() *serversimSensor`.

- [ ] **Step 1: Create `pkg/collector/sensor_serversim.go` with the extracted per-deployment body**

Move the per-deployment logic (today `handlers.go` lines 42-262, everything inside the `for _, d := range deps.Items` loop) into `Sense`. Complete file:

```go
package collector

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// serversimSensor is today's sense path: Prometheus load queries + per-pod
// server-sim GET /latest + the causal-coherence gate. Behavior-preserving.
type serversimSensor struct{}

func newServerSimSensor() *serversimSensor { return &serversimSensor{} }

func (s *serversimSensor) Sense(ctx context.Context, d appsv1.Deployment, kc kubernetes.Interface) (
	config.ServerSpec, []config.ServerSpec, bool, error) {

	serverName := d.Labels[ctrl.KeyServerName]
	maxBatchSize, _ := strconv.Atoi(d.Labels[ctrl.KeyMaxBatchSize])
	maxQueueSize, _ := strconv.Atoi(d.Labels[ctrl.KeyMaxQueueSize])

	var arrvRate, inTokens, outTokens float64
	var err error

	throughputQuery := fmt.Sprintf(`sum(rate(vllm:request_success_total{job="%s"}[1m]))*60`, d.Name)
	if arrvRate, err = PrometheusQuery(throughputQuery); err != nil {
		fmt.Println(err.Error())
		fmt.Println("checking if label exists ...")
		arrvRate, _ = strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 32)
	}
	fmt.Printf("Average arrival rate / throughput %f \n", arrvRate)

	inTokenQuery := fmt.Sprintf(`delta(vllm:prompt_tokens_total{job="%s"}[1m])/delta(vllm:request_success_total{job="%s"}[1m])`, d.Name, d.Name)
	if inTokens, err = PrometheusQuery(inTokenQuery); err != nil {
		fmt.Println(err.Error())
		fmt.Printf("checking if label %s exists ...\n", ctrl.KeyInTokens)
		avgInTokensInt, _ := strconv.Atoi(d.Labels[ctrl.KeyInTokens])
		inTokens = float64(avgInTokensInt)
	}
	if math.IsNaN(inTokens) || math.IsInf(inTokens, 0) {
		inTokens = 0.0
	}
	fmt.Printf("Average input tokens per request %f \n", inTokens)

	outTokenQuery := fmt.Sprintf(`delta(vllm:generation_tokens_total{job="%s"}[1m])/delta(vllm:request_success_total{job="%s"}[1m])`, d.Name, d.Name)
	if outTokens, err = PrometheusQuery(outTokenQuery); err != nil {
		fmt.Println(err.Error())
		fmt.Printf("checking if label %s exists ...\n", ctrl.KeyOutTokens)
		avgOutTokensInt, _ := strconv.Atoi(d.Labels[ctrl.KeyOutTokens])
		outTokens = float64(avgOutTokensInt)
	}
	if math.IsNaN(outTokens) || math.IsInf(outTokens, 0) {
		outTokens = 0.0
	}
	fmt.Printf("Average output tokens per request %f \n", outTokens)

	var itlAvg, ttftAvg, occAvg float32
	var totalThroughputRPM, totalOfferedRPM float64
	var numReporting, numReplicas int
	replicaSpecs := make([]config.ServerSpec, 0)
	selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()

	rsList, err := kc.AppsV1().ReplicaSets(d.Namespace).List(context.TODO(), metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil {
		fmt.Printf("error listing ReplicaSets for %s: %v\n", serverName, err)
	} else {
		rsUIDs := make(map[types.UID]struct{})
		for _, rs := range rsList.Items {
			for _, owner := range rs.OwnerReferences {
				if owner.UID == d.UID {
					rsUIDs[rs.UID] = struct{}{}
					break
				}
			}
		}

		pods, err := kc.CoreV1().Pods(d.Namespace).List(context.TODO(), metav1.ListOptions{LabelSelector: selectorStr})
		if err != nil {
			fmt.Printf("error listing pods for %s: %v\n", serverName, err)
		} else {
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

			var weightedITL, weightedTTFT, sumOcc float64
			for i, p := range runningPods {
				if errs[i] != nil {
					fmt.Printf("pod %s: no result this cycle (%v); skipping\n", p.Name, errs[i])
					continue
				}
				spec, ok := buildReplicaSpec(serverName, p.Name,
					d.Labels[ctrl.KeyServerClass], d.Labels[ctrl.KeyServerModel],
					maxQueueSize, maxBatchSize, d.Labels[ctrl.KeyAccelerator], envs[i])
				if !ok {
					switch {
					case envs[i] == nil:
						fmt.Printf("pod %s: no usable result this cycle; holding\n", p.Name)
					case maxBatchSize <= 0:
						fmt.Printf("pod %s: no allocation in force yet (maxbatchsize=%d); holding\n", p.Name, maxBatchSize)
					default:
						fmt.Printf("pod %s: stale result (effectiveConcurrency=%d != inForce=%d); holding\n",
							p.Name, envs[i].EffectiveInput.MaxConcurrency, maxBatchSize)
					}
					continue
				}
				w := float64(spec.CurrentAlloc.Load.Throughput)
				fmt.Printf("pod %s: TTFT=%.1fms ITL=%.1fms throughputRPM=%.2f occ=%.2f\n",
					p.Name, spec.CurrentAlloc.TTFTAverage, spec.CurrentAlloc.ITLAverage, w, spec.CurrentAlloc.AvgConcurrency)
				weightedITL += float64(spec.CurrentAlloc.ITLAverage) * w
				weightedTTFT += float64(spec.CurrentAlloc.TTFTAverage) * w
				sumOcc += float64(spec.CurrentAlloc.AvgConcurrency)
				totalThroughputRPM += w
				totalOfferedRPM += float64(spec.CurrentAlloc.Load.ArrivalRate)
				numReporting++
				replicaSpecs = append(replicaSpecs, spec)
			}
			if totalThroughputRPM > 0 {
				itlAvg = float32(weightedITL / totalThroughputRPM)
				ttftAvg = float32(weightedTTFT / totalThroughputRPM)
			}
			if numReporting > 0 {
				occAvg = float32(sumOcc / float64(numReporting))
			}
		}
	}

	setpoint, perr := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 64)
	hasSetpoint := perr == nil && setpoint > 0
	arrivalRateRPM := selectArrivalRate(numReporting, numReplicas, totalOfferedRPM, setpoint, arrvRate, hasSetpoint)

	curAlloc := config.AllocationData{
		Accelerator:    d.Labels[ctrl.KeyAccelerator],
		NumReplicas:    numReplicas,
		MaxBatch:       maxBatchSize,
		ITLAverage:     itlAvg,
		TTFTAverage:    ttftAvg,
		AvgConcurrency: occAvg,
		Load: config.ServerLoadSpec{
			ArrivalRate:  float32(arrivalRateRPM),
			Throughput:   float32(totalThroughputRPM),
			AvgInTokens:  int(inTokens),
			AvgOutTokens: int(outTokens),
		},
	}
	fmt.Printf("curAlloc[%s]: replicas=%d acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d occPerReplica=%.2f occTotal=%.2f\n",
		serverName, curAlloc.NumReplicas, curAlloc.Accelerator, curAlloc.MaxBatch,
		curAlloc.ITLAverage, curAlloc.TTFTAverage,
		curAlloc.Load.ArrivalRate, curAlloc.Load.Throughput, curAlloc.Load.AvgInTokens, curAlloc.Load.AvgOutTokens,
		curAlloc.AvgConcurrency, curAlloc.AvgConcurrency*float32(curAlloc.NumReplicas))

	serverSpec := config.ServerSpec{
		Name:         serverName,
		Class:        d.Labels[ctrl.KeyServerClass],
		Model:        d.Labels[ctrl.KeyServerModel],
		MaxQueueSize: maxQueueSize,
		CurrentAlloc: curAlloc,
	}
	return serverSpec, replicaSpecs, true, nil
}
```

- [ ] **Step 2: Rewrite `collect()` in `pkg/collector/handlers.go` to dispatch through the sensor**

Replace the entire `collect` function (lines 24-280) and drop the now-unused imports (`math`, `strconv`, `sync`, `corev1`, `labels`, `types` move into `sensor_serversim.go` and `sensor_llmd.go`). `selectArrivalRate` stays where it is (still referenced by `serversimSensor`). Resulting `handlers.go`:

```go
package collector

import (
	"context"
	"fmt"
	"net/http"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// sensor is selected once at package init from INFERNO_BACKEND.
var sensor backend.Sensor = selectSensor()

func selectSensor() backend.Sensor {
	if backend.ModeFromEnv() == backend.ModeLLMD {
		fmt.Println("collector: using llmd sensor")
		return newLLMDSensor()
	}
	fmt.Println("collector: using serversim sensor")
	return newServerSimSensor()
}

func collect(c *gin.Context) {
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments(ctrl.WatchNamespace()).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector})
	if err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "kube client: " + err.Error()})
		return
	}

	serverSpecs := make([]config.ServerSpec, 0)
	replicaSpecs := make([]config.ServerSpec, 0)
	serverMap := make(map[string]ctrl.ServerKubeInfo)

	for _, d := range deps.Items {
		if d.Labels == nil || d.Labels[ctrl.KeyServerName] == "" {
			continue
		}
		serverName := d.Labels[ctrl.KeyServerName]
		serverMap[serverName] = ctrl.ServerKubeInfo{
			UID:   string(d.UID),
			Name:  d.Name,
			Space: d.Namespace,
		}

		spec, replicas, ok, err := sensor.Sense(context.TODO(), d, KubeClient)
		if err != nil {
			fmt.Printf("sense %s: %v\n", serverName, err)
			continue
		}
		if !ok {
			continue
		}
		serverSpecs = append(serverSpecs, spec)
		replicaSpecs = append(replicaSpecs, replicas...)
	}

	for _, r := range replicaSpecs {
		fmt.Printf("replicaAlloc[%s]: acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d occ=%.2f\n",
			r.Name, r.CurrentAlloc.Accelerator, r.CurrentAlloc.MaxBatch,
			r.CurrentAlloc.ITLAverage, r.CurrentAlloc.TTFTAverage,
			r.CurrentAlloc.Load.ArrivalRate, r.CurrentAlloc.Load.Throughput, r.CurrentAlloc.Load.AvgInTokens, r.CurrentAlloc.Load.AvgOutTokens,
			r.CurrentAlloc.AvgConcurrency)
	}

	c.IndentedJSON(http.StatusOK, ctrl.ServerCollectorInfo{
		Spec:         serverSpecs,
		ReplicaSpecs: replicaSpecs,
		KubeResource: serverMap,
	})
}
```

Keep the `selectArrivalRate` function (currently at the end of `handlers.go`) — move it into `sensor_serversim.go` so `handlers.go` no longer needs `strconv`. (Cut lines 282-305 from `handlers.go`, paste at the end of `sensor_serversim.go`.)

- [ ] **Step 3: Add a placeholder `newLLMDSensor` so the package compiles**

`selectSensor` references `newLLMDSensor` (built in Task 5). Add a temporary stub at the top of a new `pkg/collector/sensor_llmd.go` so this task builds on its own:

```go
package collector

// Temporary stub; replaced by the real llmd sensor in Task 5.
func newLLMDSensor() *serversimSensor { return newServerSimSensor() }
```

- [ ] **Step 4: Build, vet, test**

Run: `go build ./... && go vet ./... && go test ./pkg/collector/ -v`
Expected: clean; existing tests pass. Confirm no unused-import errors in `handlers.go`.

- [ ] **Step 5: Commit**

```bash
git add pkg/collector/sensor_serversim.go pkg/collector/sensor_llmd.go pkg/collector/handlers.go
git commit -m "refactor(collector): extract server-sim sense path behind backend.Sensor

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: `serversim` Actuator — extract today's actuate path + gate pairing by mode

**Files:**
- Create: `pkg/actuator/actuator_serversim.go`
- Modify: `pkg/actuator/handlers.go` (`update()` dispatches through a `backend.Actuator`)
- Modify: `pkg/actuator/actuator.go` (start the pairing reconciler only in serversim mode)

**Interfaces:**
- Consumes: `backend.Actuator`, `backend.DeploymentUpdate` (Task 1); existing `patchDeployment`, `patchPodsAllocation`.
- Produces: `type serversimActuator struct{}` implementing `backend.Actuator`; `func newServerSimActuator() *serversimActuator`.

- [ ] **Step 1: Create `pkg/actuator/actuator_serversim.go`**

```go
package actuator

import (
	"context"
	"fmt"

	"github.com/llm-inferno/control-loop/pkg/backend"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
)

// serversimActuator is today's actuate path: patch replicas + accelerator/
// maxbatchsize labels on the Deployment, then project the maxbatchsize label
// onto each running pod for the server-sim sidecar to read. Behavior-preserving.
type serversimActuator struct{}

func newServerSimActuator() *serversimActuator { return &serversimActuator{} }

func (a *serversimActuator) Actuate(ctx context.Context, kc kubernetes.Interface, u backend.DeploymentUpdate) error {
	if err := patchDeployment(u.ServerName, u.DeployName, u.Namespace, &u.Allocation); err != nil {
		if apierrors.IsNotFound(err) {
			fmt.Printf("srv=[%s/%s]: deployment gone, skipping\n", u.ServerName, u.Namespace)
			return nil
		}
		return err
	}
	if u.Allocation.NumReplicas > 0 {
		if err := patchPodsAllocation(ctx, kc, u.Namespace, u.DeployName,
			u.Allocation.Accelerator, u.Allocation.MaxBatch); err != nil {
			fmt.Printf("srv=[%s/%s]: pod allocation patch warning: %v\n", u.ServerName, u.Namespace, err)
		}
	}
	return nil
}
```

- [ ] **Step 2: Rewrite `update()` in `pkg/actuator/handlers.go` to dispatch through the actuator**

Replace the `update` function (lines 20-61). `patchDeployment` stays in this file. Resulting top of file:

```go
package actuator

import (
	"context"
	"fmt"
	"net/http"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// actuatorImpl is selected once from INFERNO_BACKEND.
var actuatorImpl backend.Actuator = selectActuator()

func selectActuator() backend.Actuator {
	if backend.ModeFromEnv() == backend.ModeLLMD {
		fmt.Println("actuator: using llmd actuator (replicas only)")
		return newLLMDActuator()
	}
	fmt.Println("actuator: using serversim actuator")
	return newServerSimActuator()
}

func update(c *gin.Context) {
	var info ctrl.ServerActuatorInfo
	if err := c.BindJSON(&info); err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "binding error: " + err.Error()})
		return
	}
	updates := ComputeUpdates(info.Spec, info.KubeResource)
	for _, u := range updates {
		if err := actuatorImpl.Actuate(context.Background(), KubeClient, u); err != nil {
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "kube client: " + err.Error()})
			return
		}
	}
	c.IndentedJSON(http.StatusOK, "Done")
}
```

(Keep `patchDeployment` below `update` unchanged. It still imports `config`, `metav1`, `types` — those imports remain used.)

- [ ] **Step 3: Gate the pairing reconciler on serversim mode in `pkg/actuator/actuator.go`**

The pairing reconciler is server-sim/vllm-server-specific; it must not run in llmd mode. In `NewActuator()`, wrap the reconciler start (lines 40-47):

```go
	// Pairing is a server-sim/vLLM-server concern; never run it in llmd mode.
	if backend.ModeFromEnv() == backend.ModeServerSim {
		period := pairingTickInterval()
		if period > 0 {
			fmt.Printf("actuator: starting pairing reconciler (tick=%s)\n", period)
			go runReconciler(context.Background(), KubeClient, period)
		} else {
			fmt.Printf("actuator: pairing reconciler disabled (INFERNO_PAIRING_TICK_SEC=0)\n")
		}
	} else {
		fmt.Println("actuator: pairing reconciler disabled (llmd backend)")
	}
```

Add `"github.com/llm-inferno/control-loop/pkg/backend"` to the imports of `actuator.go`.

- [ ] **Step 4: Add a placeholder `newLLMDActuator` so the package compiles**

Create `pkg/actuator/actuator_llmd.go`:

```go
package actuator

// Temporary stub; replaced by the real llmd actuator in Task 6.
func newLLMDActuator() *serversimActuator { return newServerSimActuator() }
```

- [ ] **Step 5: Build, vet, test**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/actuator/actuator_serversim.go pkg/actuator/actuator_llmd.go pkg/actuator/handlers.go pkg/actuator/actuator.go
git commit -m "refactor(actuator): extract server-sim actuate path; gate pairing by backend mode

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: `llmd` Sensor — all-Prometheus, keyed by discovered pod names

**Files:**
- Modify: `pkg/collector/sensor_llmd.go` (replace the stub with the real implementation)
- Create: `pkg/collector/sensor_llmd_test.go`

**Interfaces:**
- Consumes: `newPromAPI`, `queryVector` (Task 2); `ctrl.PromWindowEnvName`; `ctrl.Key*` labels.
- Produces:
  - `type llmdSensor struct{ api v1.API; window string }`, `func newLLMDSensor() *llmdSensor`.
  - Pure mapper `func buildLLMDSpecs(serverName, class, model, accelerator string, maxQueueSize, numReplicas int, pods []string, m llmdMetrics) (config.ServerSpec, []config.ServerSpec)`.
  - `type llmdMetrics struct{ throughputRPM, itlSec, ttftSec, occupancy map[string]float64; inTokens, outTokens float64 }`.

- [ ] **Step 1: Write the failing test for the pure mapper**

Create `pkg/collector/sensor_llmd_test.go`:

```go
package collector

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func TestBuildLLMDSpecs_TwoPods(t *testing.T) {
	m := llmdMetrics{
		throughputRPM: map[string]float64{"pod-a": 120, "pod-b": 60},
		itlSec:        map[string]float64{"pod-a": 0.010, "pod-b": 0.020}, // 10ms, 20ms
		ttftSec:       map[string]float64{"pod-a": 0.100, "pod-b": 0.200}, // 100ms, 200ms
		occupancy:     map[string]float64{"pod-a": 4, "pod-b": 2},
		inTokens:      512,
		outTokens:     128,
	}
	server, replicas, ok := func() (config.ServerSpecT, []config.ServerSpecT, bool) { return mapForTest(m) }()
	_ = ok
	_ = server
	_ = replicas
	t.Skip("replaced below") // placeholder to keep the compiler honest before impl
}
```

Then immediately replace that file's body with the real test (the placeholder above documents intent; write this as the actual test):

```go
package collector

import (
	"math"
	"testing"
)

func approxEq(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-2 }

func TestBuildLLMDSpecs_TwoPods(t *testing.T) {
	m := llmdMetrics{
		throughputRPM: map[string]float64{"pod-a": 120, "pod-b": 60},
		itlSec:        map[string]float64{"pod-a": 0.010, "pod-b": 0.020},
		ttftSec:       map[string]float64{"pod-a": 0.100, "pod-b": 0.200},
		occupancy:     map[string]float64{"pod-a": 4, "pod-b": 2},
		inTokens:      512,
		outTokens:     128,
	}
	server, replicas := buildLLMDSpecs("srv", "Premium", "qwen3_32b", "H100",
		0, 2, []string{"pod-a", "pod-b"}, m)

	if len(replicas) != 2 {
		t.Fatalf("replicas = %d, want 2", len(replicas))
	}
	// deployment throughput = sum = 180 RPM; ArrivalRate := Throughput
	if !approxEq(server.CurrentAlloc.Load.Throughput, 180) {
		t.Errorf("throughput = %v, want 180", server.CurrentAlloc.Load.Throughput)
	}
	if server.CurrentAlloc.Load.ArrivalRate != server.CurrentAlloc.Load.Throughput {
		t.Errorf("ArrivalRate %v != Throughput %v", server.CurrentAlloc.Load.ArrivalRate, server.CurrentAlloc.Load.Throughput)
	}
	// ITL is throughput-weighted: (0.010*120 + 0.020*60)/180 s = 0.01333 s -> 13.33 ms
	if !approxEq(server.CurrentAlloc.ITLAverage, 13.333) {
		t.Errorf("ITL = %v ms, want ~13.33", server.CurrentAlloc.ITLAverage)
	}
	// TTFT weighted: (0.100*120 + 0.200*60)/180 = 0.13333 s -> 133.33 ms
	if !approxEq(server.CurrentAlloc.TTFTAverage, 133.333) {
		t.Errorf("TTFT = %v ms, want ~133.33", server.CurrentAlloc.TTFTAverage)
	}
	// occupancy is mean over reporting pods: (4+2)/2 = 3
	if !approxEq(server.CurrentAlloc.AvgConcurrency, 3) {
		t.Errorf("occ = %v, want 3", server.CurrentAlloc.AvgConcurrency)
	}
	if server.CurrentAlloc.Load.AvgInTokens != 512 || server.CurrentAlloc.Load.AvgOutTokens != 128 {
		t.Errorf("tokens = %d/%d, want 512/128", server.CurrentAlloc.Load.AvgInTokens, server.CurrentAlloc.Load.AvgOutTokens)
	}
	if server.CurrentAlloc.NumReplicas != 2 {
		t.Errorf("numReplicas = %d, want 2", server.CurrentAlloc.NumReplicas)
	}
}

func TestBuildLLMDSpecs_NoReporting(t *testing.T) {
	m := llmdMetrics{
		throughputRPM: map[string]float64{},
		itlSec:        map[string]float64{},
		ttftSec:       map[string]float64{},
		occupancy:     map[string]float64{},
	}
	server, replicas := buildLLMDSpecs("srv", "Premium", "qwen3_32b", "H100", 0, 1, nil, m)
	if len(replicas) != 0 {
		t.Errorf("replicas = %d, want 0", len(replicas))
	}
	if server.CurrentAlloc.Load.Throughput != 0 || server.CurrentAlloc.ITLAverage != 0 {
		t.Errorf("expected zeroed alloc, got %+v", server.CurrentAlloc)
	}
}
```

Delete the first placeholder version — keep only this real test in the file.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/collector/ -run TestBuildLLMDSpecs -v`
Expected: FAIL to compile (`llmdMetrics`, `buildLLMDSpecs` undefined).

- [ ] **Step 3: Replace `pkg/collector/sensor_llmd.go` with the real implementation**

ITL/TTFT are stored in **milliseconds** in `AllocationData` (server-sim path multiplies AvgITL/AvgTTFT which are ms; here Prometheus gives seconds, so convert ×1000). Occupancy is the mean of the per-pod `num_requests_running` gauge over reporting pods (matches the server-sim `occAvg`). ArrivalRate := Throughput (no arrival counter).

```go
package collector

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// llmdMetrics holds per-pod (keyed by pod name) real-vLLM readings plus the
// deployment-level token averages, as fetched from Prometheus/Thanos.
type llmdMetrics struct {
	throughputRPM map[string]float64 // completions/min
	itlSec        map[string]float64 // seconds
	ttftSec       map[string]float64 // seconds
	occupancy     map[string]float64 // num_requests_running gauge
	inTokens      float64
	outTokens     float64
}

// llmdSensor senses a real llm-d variant entirely from Prometheus. No server-sim
// sidecar, no coherence gate (m* is pinned). ArrivalRate := Throughput because
// this vLLM exports no arrival counter (documented limitation).
type llmdSensor struct {
	api    v1.API
	window string
}

func newLLMDSensor() *llmdSensor {
	apiv1, err := newPromAPI()
	if err != nil {
		fmt.Printf("llmd sensor: prometheus client error: %v\n", err)
	}
	window := os.Getenv(ctrl.PromWindowEnvName)
	if window == "" {
		window = "1m"
	}
	return &llmdSensor{api: apiv1, window: window}
}

func (s *llmdSensor) Sense(ctx context.Context, d appsv1.Deployment, kc kubernetes.Interface) (
	config.ServerSpec, []config.ServerSpec, bool, error) {

	serverName := d.Labels[ctrl.KeyServerName]
	maxQueueSize, _ := strconv.Atoi(d.Labels[ctrl.KeyMaxQueueSize])
	numReplicas := 0
	if d.Spec.Replicas != nil {
		numReplicas = int(*d.Spec.Replicas)
	}

	podNames, err := runningPodNames(ctx, kc, d)
	if err != nil {
		return config.ServerSpec{}, nil, false, err
	}
	if len(podNames) == 0 {
		// nothing reporting this cycle; still emit a zeroed deployment spec so
		// numReplicas/labels flow, mirroring the server-sim empty case.
		server, replicas := buildLLMDSpecs(serverName, d.Labels[ctrl.KeyServerClass],
			d.Labels[ctrl.KeyServerModel], d.Labels[ctrl.KeyAccelerator], maxQueueSize, numReplicas, nil, llmdMetrics{})
		return server, replicas, true, nil
	}

	m, err := s.fetchMetrics(ctx, d.Namespace, podNames)
	if err != nil {
		return config.ServerSpec{}, nil, false, err
	}
	server, replicas := buildLLMDSpecs(serverName, d.Labels[ctrl.KeyServerClass],
		d.Labels[ctrl.KeyServerModel], d.Labels[ctrl.KeyAccelerator], maxQueueSize, numReplicas, podNames, m)
	return server, replicas, true, nil
}

// runningPodNames discovers the deployment's running pods owned by its current
// ReplicaSets (same discipline as the server-sim path).
func runningPodNames(ctx context.Context, kc kubernetes.Interface, d appsv1.Deployment) ([]string, error) {
	selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()
	rsList, err := kc.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil {
		return nil, err
	}
	rsUIDs := make(map[types.UID]struct{})
	for _, rs := range rsList.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.UID == d.UID {
				rsUIDs[rs.UID] = struct{}{}
				break
			}
		}
	}
	pods, err := kc.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil {
		return nil, err
	}
	var names []string
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
		if owned {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// fetchMetrics runs the per-pod PromQL queries, keyed on the exact discovered
// pod names to avoid a prefix regex over-matching a sibling deployment.
func (s *llmdSensor) fetchMetrics(ctx context.Context, ns string, pods []string) (llmdMetrics, error) {
	sel := fmt.Sprintf(`namespace="%s",pod=~"%s"`, ns, strings.Join(pods, "|"))
	w := s.window
	m := llmdMetrics{
		throughputRPM: map[string]float64{},
		itlSec:        map[string]float64{},
		ttftSec:       map[string]float64{},
		occupancy:     map[string]float64{},
	}

	tput, err := queryVector(ctx, s.api, fmt.Sprintf(`sum by(pod)(rate(vllm:request_success_total{%s}[%s]))*60`, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(tput, m.throughputRPM)

	itl, err := queryVector(ctx, s.api, fmt.Sprintf(
		`sum by(pod)(rate(vllm:request_time_per_output_token_seconds_sum{%s}[%s])) / sum by(pod)(rate(vllm:request_time_per_output_token_seconds_count{%s}[%s]))`, sel, w, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(itl, m.itlSec)

	ttft, err := queryVector(ctx, s.api, fmt.Sprintf(
		`sum by(pod)(rate(vllm:time_to_first_token_seconds_sum{%s}[%s])) / sum by(pod)(rate(vllm:time_to_first_token_seconds_count{%s}[%s]))`, sel, w, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(ttft, m.ttftSec)

	occ, err := queryVector(ctx, s.api, fmt.Sprintf(`avg_over_time(vllm:num_requests_running{%s}[%s])`, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(occ, m.occupancy)

	// deployment-level token averages (scalar): tokens / completions over the window
	if v, err := queryScalar(ctx, s.api, fmt.Sprintf(
		`sum(delta(vllm:prompt_tokens_total{%s}[%s])) / sum(delta(vllm:request_success_total{%s}[%s]))`, sel, w, sel, w)); err == nil {
		m.inTokens = sanitize(v)
	}
	if v, err := queryScalar(ctx, s.api, fmt.Sprintf(
		`sum(delta(vllm:generation_tokens_total{%s}[%s])) / sum(delta(vllm:request_success_total{%s}[%s]))`, sel, w, sel, w)); err == nil {
		m.outTokens = sanitize(v)
	}
	return m, nil
}

func collectByPod(v model.Vector, into map[string]float64) {
	for _, s := range v {
		pod := string(s.Metric["pod"])
		if pod == "" {
			continue
		}
		f := float64(s.Value)
		if f == f { // not NaN
			into[pod] = f
		}
	}
}

func sanitize(f float64) float64 {
	if f != f { // NaN
		return 0
	}
	return f
}

// buildLLMDSpecs is the pure mapping from per-pod metrics to the deployment
// ServerSpec + per-replica ServerSpecs. ITL/TTFT are converted seconds→ms and
// aggregated throughput-weighted; occupancy is the mean over reporting pods;
// ArrivalRate := Throughput.
func buildLLMDSpecs(serverName, class, model, accelerator string, maxQueueSize, numReplicas int,
	pods []string, m llmdMetrics) (config.ServerSpec, []config.ServerSpec) {

	replicas := make([]config.ServerSpec, 0, len(pods))
	var weightedITL, weightedTTFT, totalTputRPM, sumOcc float64
	numReporting := 0

	for _, pod := range pods {
		tput, ok := m.throughputRPM[pod]
		if !ok {
			continue
		}
		itlMs := m.itlSec[pod] * 1000
		ttftMs := m.ttftSec[pod] * 1000
		occ := m.occupancy[pod]

		replicas = append(replicas, config.ServerSpec{
			Name:         serverName + ctrl.ReplicaNameSeparator + pod,
			Class:        class,
			Model:        model,
			MaxQueueSize: maxQueueSize,
			CurrentAlloc: config.AllocationData{
				Accelerator:    accelerator,
				NumReplicas:    1,
				ITLAverage:     float32(itlMs),
				TTFTAverage:    float32(ttftMs),
				AvgConcurrency: float32(occ),
				Load: config.ServerLoadSpec{
					ArrivalRate:  float32(tput),
					Throughput:   float32(tput),
					AvgInTokens:  int(m.inTokens),
					AvgOutTokens: int(m.outTokens),
				},
			},
		})
		weightedITL += itlMs * tput
		weightedTTFT += ttftMs * tput
		totalTputRPM += tput
		sumOcc += occ
		numReporting++
	}

	var itlAvg, ttftAvg, occAvg float32
	if totalTputRPM > 0 {
		itlAvg = float32(weightedITL / totalTputRPM)
		ttftAvg = float32(weightedTTFT / totalTputRPM)
	}
	if numReporting > 0 {
		occAvg = float32(sumOcc / float64(numReporting))
	}

	server := config.ServerSpec{
		Name:         serverName,
		Class:        class,
		Model:        model,
		MaxQueueSize: maxQueueSize,
		CurrentAlloc: config.AllocationData{
			Accelerator:    accelerator,
			NumReplicas:    numReplicas,
			ITLAverage:     itlAvg,
			TTFTAverage:    ttftAvg,
			AvgConcurrency: occAvg,
			Load: config.ServerLoadSpec{
				ArrivalRate:  float32(totalTputRPM),
				Throughput:   float32(totalTputRPM),
				AvgInTokens:  int(m.inTokens),
				AvgOutTokens: int(m.outTokens),
			},
		},
	}
	return server, replicas
}
```

Note: `MaxBatch` is intentionally left 0 on llmd specs; the controller pins it via `DEFAULT_MAX_BATCH_SIZE` (`Optimize()` sets `MaxBatchSize` where 0). This matches the design.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/collector/ -run TestBuildLLMDSpecs -v && go build ./... && go vet ./...`
Expected: PASS; build/vet clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/collector/sensor_llmd.go pkg/collector/sensor_llmd_test.go
git commit -m "feat(collector): llmd sensor — all-Prometheus, keyed by discovered pod names

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: `llmd` Actuator — replicas-only patch

**Files:**
- Modify: `pkg/actuator/actuator_llmd.go` (replace the stub)
- Create: `pkg/actuator/actuator_llmd_test.go`

**Interfaces:**
- Consumes: `backend.Actuator`, `backend.DeploymentUpdate`; `ctrl` (unused here), `k8s.io/client-go/kubernetes`.
- Produces: `type llmdActuator struct{}` implementing `backend.Actuator`; `func newLLMDActuator() *llmdActuator`; helper `func replicasPatch(n int) []byte`.

- [ ] **Step 1: Write the failing test for the replicas patch payload + fake-clientset apply**

Create `pkg/actuator/actuator_llmd_test.go`:

```go
package actuator

import (
	"context"
	"testing"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReplicasPatch(t *testing.T) {
	got := string(replicasPatch(3))
	want := `[{"op":"replace","path":"/spec/replicas","value":3}]`
	if got != want {
		t.Errorf("replicasPatch(3) = %s, want %s", got, want)
	}
}

func TestLLMDActuateScalesReplicasOnly(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "infer",
			Labels: map[string]string{"a": "1"}},
		Spec: appsv1.DeploymentSpec{Replicas: int32ptr(1)},
	}
	kc := fake.NewSimpleClientset(dep)
	a := newLLMDActuator()

	u := backend.DeploymentUpdate{
		ServerName: "qwen", DeployName: "qwen", Namespace: "infer",
		Allocation: config.AllocationData{NumReplicas: 4, Accelerator: "H100", MaxBatch: 256},
	}
	if err := a.Actuate(context.Background(), kc, u); err != nil {
		t.Fatalf("Actuate: %v", err)
	}
	out, _ := kc.AppsV1().Deployments("infer").Get(context.Background(), "qwen", metav1.GetOptions{})
	if out.Spec.Replicas == nil || *out.Spec.Replicas != 4 {
		t.Errorf("replicas = %v, want 4", out.Spec.Replicas)
	}
	// llmd must NOT write accelerator/maxbatchsize labels
	if _, ok := out.Labels["inferno.server.allocation.accelerator"]; ok {
		t.Errorf("llmd actuator wrote accelerator label; should not")
	}
}

func int32ptr(n int32) *int32 { return &n }
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/actuator/ -run 'TestReplicasPatch|TestLLMDActuate' -v`
Expected: FAIL to compile (`replicasPatch`, real `llmdActuator` undefined; stub returns `*serversimActuator`).

- [ ] **Step 3: Replace `pkg/actuator/actuator_llmd.go` with the real implementation**

```go
package actuator

import (
	"context"
	"fmt"

	"github.com/llm-inferno/control-loop/pkg/backend"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// llmdActuator scales a real llm-d variant Deployment by replicas only. m* is a
// static vLLM startup constraint (--max-num-seq) pinned via DEFAULT_MAX_BATCH_SIZE,
// so there is nothing to project onto pods and no pairing to reconcile.
type llmdActuator struct{}

func newLLMDActuator() *llmdActuator { return &llmdActuator{} }

func replicasPatch(n int) []byte {
	return []byte(fmt.Sprintf(`[{"op":"replace","path":"/spec/replicas","value":%d}]`, n))
}

func (a *llmdActuator) Actuate(ctx context.Context, kc kubernetes.Interface, u backend.DeploymentUpdate) error {
	fmt.Printf("srv=[%s/%s]: llmd scale replicas=%d\n", u.ServerName, u.Namespace, u.Allocation.NumReplicas)
	_, err := kc.AppsV1().Deployments(u.Namespace).Patch(ctx, u.DeployName,
		types.JSONPatchType, replicasPatch(u.Allocation.NumReplicas), metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			fmt.Printf("srv=[%s/%s]: deployment gone, skipping\n", u.ServerName, u.Namespace)
			return nil
		}
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/actuator/ -run 'TestReplicasPatch|TestLLMDActuate' -v && go build ./... && go vet ./...`
Expected: PASS; build/vet clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/actuator/actuator_llmd.go pkg/actuator/actuator_llmd_test.go
git commit -m "feat(actuator): llmd actuator — replicas-only patch

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: Documentation + full-suite verification

**Files:**
- Modify: `docs/env-vars.md` (add the new env vars)
- Modify: `CLAUDE.md` (one line under Environment Variables noting `INFERNO_BACKEND`)

**Interfaces:** none (docs + verification only).

- [ ] **Step 1: Add the new env vars to `docs/env-vars.md`**

Add a row/section for each (match the file's existing table format; use these descriptions):

```
| INFERNO_BACKEND | serversim | Sense/actuate backend for this process: `serversim` (default; server-sim `/latest` + label actuation) or `llmd` (all-Prometheus sense, replicas-only actuate). |
| INFERNO_PROMETHEUS_URL | http://localhost:9090 | Prometheus/Thanos query endpoint. For OpenShift user-workload metrics use the in-cluster thanos-querier HTTPS URL. |
| INFERNO_PROMETHEUS_TOKEN_PATH | (SA token path) | Bearer-token file for the Prometheus client. Read only if present; defaults to the pod's service-account token. |
| INFERNO_PROMETHEUS_CA_PATH | (SA CA path) | CA bundle trusted for an HTTPS Prometheus endpoint. |
| INFERNO_PROMETHEUS_INSECURE | false | `true` skips TLS verification for the Prometheus endpoint. |
| INFERNO_PROM_WINDOW | 1m | PromQL range window for the llmd sensor's rate/avg_over_time queries. |
```

- [ ] **Step 2: Note `INFERNO_BACKEND` in `CLAUDE.md`**

In the "Environment Variables" section's "Notable knobs" sentence, append: `, and INFERNO_BACKEND (serversim default | llmd — selects the real-llm-d all-Prometheus backend; see docs/superpowers/specs/2026-07-01-llmd-backend-integration-design.md)`.

- [ ] **Step 3: Full build, vet, test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all clean/PASS. There should be no remaining references to the old `pkg/actuator` `DeploymentUpdate` or the per-call Prometheus client.

- [ ] **Step 4: Behavior-preservation check (server-sim)**

Confirm `INFERNO_BACKEND` unset/`serversim` still selects the server-sim path: grep the two `select*` functions and run the qa or blis kind deploy locally if a cluster is available (per CLAUDE.md `scripts/qa/kind-deploy.sh`), or at minimum inspect the controller/collector/actuator logs for `using serversim sensor` / `using serversim actuator`. No cycle timing regression expected.

- [ ] **Step 5: Commit**

```bash
git add docs/env-vars.md CLAUDE.md
git commit -m "docs: document INFERNO_BACKEND + INFERNO_PROMETHEUS_* env vars

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

- [ ] **Step 6: Open the PR**

```bash
git push -u origin feat/llmd-backend
gh pr create --title "feat: Backend seam + llmd adapter (issue #64)" \
  --body "$(cat <<'BODY'
Implements the Backend seam and llmd adapter per docs/superpowers/specs/2026-07-01-llmd-backend-integration-design.md (§I1–I8).

- pkg/backend: Sensor/Actuator interfaces, DeploymentUpdate, INFERNO_BACKEND mode select
- collector: configurable token-authenticated Prometheus client; serversim sensor extracted; llmd sensor (all-Prometheus, keyed by discovered pod names, occupancy from num_requests_running, ArrivalRate := Throughput)
- actuator: serversim actuator extracted; pairing gated to serversim mode; llmd actuator replicas-only
- docs: env-vars

serversim behavior preserved (backends vllm-server/queue-analysis/blis unchanged). llmd validated by unit tests + read-only sense against the live mye Qwen3-32B; m* pinned via DEFAULT_MAX_BATCH_SIZE.

Follow-on tracks (separate): qwen3_32b model profile + calibration; stand up the dedicated Qwen3-32B variant; E1/E2 experiments.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

---

## Out of scope for this plan (separate tracks)

- **`qwen3_32b` model profile + calibration** (`inferno-data/…`): new accelerator/model/serviceclass/optimizer data; seed perfParms or rely on the online tuner ± BLIS side-sweep. Data, not code.
- **Standing up the dedicated Qwen3-32B llm-d variant + WVA** (reap-aware, done last, briefly): reuse the live `mye` modelservice + a `VariantAutoscaling` CR as templates.
- **E1 (inferno vs WVA A/B) and E2 (m\* value/validation)** experiments and their reports.

## Self-Review

- **Spec coverage:** §I1 interfaces+selection → Task 1; §I2 serversim refactor → Tasks 3,4; §I3 llmd sensor (queries, pod-name keying, occupancy gauge, ArrivalRate:=Throughput, no coherence gate, no load-label fallback) → Task 5; §I4 llmd actuator replicas-only + m* pin note → Task 6; §I5 Prometheus client (URL/token/CA/insecure, backward-compatible) → Task 2; §I6 load generation → doc/experiment track (no code); §I7 config surface → Tasks 1,7; §I8 testing without GPUs → unit tests (Tasks 1,2,5,6) + read-only integration note (Task 7 Step 4) + PR body. Covered.
- **Placeholder scan:** the one deliberate throwaway (the Task 5 Step 1 placeholder test) is explicitly replaced within the same step; the Task 3/Task 4 stubs are explicitly replaced in Tasks 5/6. No TBD/"handle errors"/"similar to" left.
- **Type consistency:** `backend.DeploymentUpdate` fields (`ServerName/UID/DeployName/Namespace/Allocation`) match all call sites; `Sensor.Sense`/`Actuator.Actuate` signatures identical in interface, impls, and callers; `llmdMetrics`/`buildLLMDSpecs` names match between test and impl; `newPromAPI`/`queryVector`/`queryScalar` names consistent across Tasks 2 and 5.
