# llm-d backend integration — approach & feasibility

**Date:** 2026-07-01
**Status:** Approach doc (research exercise; not yet an implementation spec). Open questions **resolved against the live cluster 2026-07-01** — see *Cluster findings* and *Target decision* below.
**Issue:** [#64](https://github.com/llm-inferno/control-loop/issues/64)
**Author:** (draft)

## Context

We want to **run the inferno control loop on a real llm-d environment, in place of the llm-d Workload-Variant Autoscaler (WVA)**, so we can answer a research question: *does inferno's optimizer produce better SLO/cost/reaction outcomes than WVA on the same real llm-d deployment?* The near-term goal is a **research result**, not a productized component.

This started as "add an llm-d evaluator to server-sim, like the continuous-vllm evaluator." Investigation showed that framing is wrong. Under **real-serving llm-d** (real vLLM pods on GPUs) with WVA disabled, there is no simulator to query — real pods emit real metrics. So server-sim drops out of the loop entirely, and the integration work is a **control-loop sensing/actuation adapter**, not a server-sim backend.

Two integration paths were considered:

1. **Run inferno standalone instead of WVA** on real llm-d, then A/B against WVA. ← **chosen**
2. Port inferno's optimizer as a WVA `ScalingOptimizer`/`Analyzer` plugin (reusing WVA's llm-d sensing/actuation).

(1) is chosen because the goal is a research result: it de-risks the science cheaply, keeps inferno's native architecture intact, and yields a direct head-to-head artifact. The m\* actuation limitation (below) is identical in both paths, so (2)-first would pay a large integration cost without validating the differentiator. (2) remains the eventual productization path and is a non-goal here.

## Current architecture (what must change)

The control loop is 5 microservices (controller/collector/optimizer/actuator/tuner) on a periodic ticker. Two seams are tightly coupled to server-sim:

**Collector** (`pkg/collector/handlers.go: collect()`)
- Discovers managed Deployments via `inferno.server.managed=true` (`ctrl.KeyManaged`).
- Reads **load** from Prometheus, keyed `job="<deployName>"`: arrival/throughput (`vllm:request_success_total`), input tokens (`vllm:prompt_tokens_total`), output tokens (`vllm:generation_tokens_total`).
- Reads **performance** per pod from the server-sim sidecar `GET /latest` (`pkg/collector/serversim.go: getLatest()`, `pod:8080` via k8s API-proxy): `TTFT`, `ITL`, `Throughput`, `RespTime`/`WaitTime` → occupancy (Little's Law, `replicaspec.go`).
- **Coherence gate** (`replicaspec.go`): a pod's `/latest` is accepted only if `effectiveInput.MaxConcurrency == inferno.server.allocation.maxbatchsize` — i.e. the reading reflects the currently-actuated m\*.
- Prometheus endpoint is hardcoded `http://localhost:9090` (`pkg/collector/utils.go`).
- Emits `config.ServerSpec{CurrentAlloc}` per server + per-replica, in `ctrl.ServerCollectorInfo`.

**Actuator** (`pkg/actuator/handlers.go: update()`)
- Patches **replicas** directly: `PATCH /spec/replicas` (`patchDeployment()`). **Works unchanged on an llm-d variant Deployment.**
- Patches **m\*** via Deployment/pod labels `inferno.server.allocation.maxbatchsize` → downward API → server-sim sidecar reads it (`patchPodsAllocation()`, `pod_alloc.go`). **Server-sim-specific.**
- A separate **pairing reconciler** (`pkg/actuator/pairing.go`) keeps a server-sim deployment in lockstep with a paired real-vLLM deployment. Only active when `inferno.server.evaluator == "vllm-server"`.

No backend/environment abstraction exists; server-sim endpoints/schemas are referenced directly. The `inferno.server.evaluator` label (`ctrl.KeyEvaluator`, values `vllm-server`/`queue-analysis`/`blis`) exists but is unused in collect/actuate.

## Proposed design

### 1. Introduce a `Backend` seam

Extract the sense and actuate operations behind an interface with two implementations, selected per-deployment by the existing `inferno.server.evaluator` label (add value `llm-d`) — or by a process-level env var for the initial single-backend deployment.

```go
// pkg/backend (new)
type Backend interface {
    // Sense one managed Deployment into a ServerSpec (+ per-replica specs).
    Sense(ctx context.Context, dep appsv1.Deployment) (server config.ServerSpec, replicas []config.ServerSpec, ok bool, err error)
    // Actuate an optimizer allocation onto one managed Deployment.
    Actuate(ctx context.Context, u actuator.Update) error
}
```

- `serversim` backend = today's behavior (Prometheus load + `/latest` performance + coherence gate; replica patch + downward-API m\*). Refactor-in-place, no behavior change.
- `llmd` backend = new (below).

The collector's `collect()` loop and the actuator's `update()` loop keep their discovery/aggregation/patch-orchestration structure; only the per-deployment sense/actuate calls dispatch through the `Backend`. This keeps the refactor small and makes the eventual path-(2) work reusable.

### 2. `llmd` collector backend — sense everything from Prometheus

- **All** metrics from Prometheus (no `/latest`, no sidecar). Reuse the existing load queries; add performance queries against real vLLM (names **confirmed against live Thanos**, 2026-07-01):
  - TTFT: `vllm:time_to_first_token_seconds_sum / _count`
  - ITL: `vllm:request_time_per_output_token_seconds_sum / _count` — **note the `request_` prefix**; the non-prefixed `vllm:time_per_output_token_seconds` does *not* exist on the deployed vLLM.
  - occupancy: `vllm:num_requests_running` (or Little's Law from response/queue time histograms)
  - (available bonus signals, WVA's saturation inputs: `vllm:num_requests_waiting`, `vllm:kv_cache_usage_perc`)
- **Configurable Prometheus endpoint** (fix the hardcoded `localhost:9090`). The real backend is the **OpenShift user-workload monitoring stack**, reached via `thanos-querier.openshift-monitoring` — an HTTPS endpoint that requires a **service-account bearer token** (`/var/run/secrets/kubernetes.io/serviceaccount/token`) and the cluster CA, exactly as WVA reads it (`PROMETHEUS_TOKEN_PATH`). So this is not just a URL swap: add `INFERNO_PROMETHEUS_URL` **plus** bearer-token auth + CA trust on the Prometheus client. RBAC: the inferno SA needs `get`/`list` on metrics (cluster-monitoring-view or equivalent).
- **Discovery / query keying** (**confirmed**): vLLM metrics do **not** carry `job="<deployName>"`. The `job` label is `"<namespace>/<monitor-name>"` (llm-d modelservice helm chart's Pod/ServiceMonitor, e.g. `mye/vllm-qwen-qwe-…`). They *do* carry clean `namespace`, `pod`, `model_name`, `container="vllm"` labels. So the llmd backend keys per-variant on **`namespace="<ns>", pod=~"<deploy>-.*"`** (or `model_name="<id>"`), **not** `job=<deployName>`. We still stamp `inferno.server.managed=true` + `inferno.server.name`/`model` on the variant Deployment so k8s *discovery* is unchanged; only the PromQL selector changes.
- **Coherence gate is moot** once m\* is pinned (it existed to detect m\* convergence via `/latest`); the `llmd` backend does not apply it.

### 3. `llmd` actuator backend — replicas only

- **Replicas**: reuse `patchDeployment()`'s `/spec/replicas` patch. Nearly free.
- **m\***: **do not actuate.** Skip `patchPodsAllocation()`. Instead **pin** m\* as a constraint (below).
- **Pairing reconciler**: disabled for `llm-d` (not `vllm-server`).

### 4. m\* handling — pin, don't drop

inferno's optimizer decides `(accelerator, replicas, m*)` jointly every cycle; m\* optimization is its key differentiator over WVA (which hardcodes batch size). The per-server concurrency cap is **static startup config — restart to change**. (The design originally attributed this to an EPP `concurrency-detector.maxConcurrency` knob; **confirmed 2026-07-01 that the deployed gaie v1.4.0 EPP has no such plugin** — its plugins are `queue-scorer`/prefix-cache/etc. The real ceiling is **vLLM's `--max-num-seq` (`$VLLM_MAX_NUM_SEQ`, observed = 256** on the live Qwen3-32B server), set in the modelservice pod template.) Either way there is no live m\* knob today.

Therefore, for this phase we **pin** m\* rather than actuate it:

- Read the deployment's `VLLM_MAX_NUM_SEQ = M` (the vLLM server's true concurrency ceiling).
- Set inferno's existing `DEFAULT_MAX_BATCH_SIZE = M` (pins maxbatchsize on all servers when > 0).
- inferno then optimizes **replicas under the true fixed concurrency** — consistent decisions, no new code.

Letting inferno pick m\* and dropping it would be a correctness bug: replica counts would be computed for a concurrency the environment isn't running.

### 5. Optimizer & model — unchanged; online tuner for α/β/γ

- Optimizer is unchanged. It already operates per-server on **aggregate** load, so we start **single-variant** and let inferno decide at the variant level; the router's internal per-endpoint load distribution matters only for model *accuracy*, which the tuner absorbs.
- **α/β/γ source: rely on the online tuner**, calibrating from the real vLLM Prometheus metrics as the loop runs.
- **Optional side calibration with a BLIS pod**: for off-operating-point excitation the live system doesn't visit, run a standalone server-sim+BLIS pod purely for calibration sweeps and feed the tuner. This reuses the existing `pkg/collector/calibrate.go` + `runSimulate()` machinery, pointed at a BLIS-backed server-sim pod rather than a paired real server. Real metrics drive the current operating point; BLIS supplies the excitation. Deferred; enable only if the online tuner's coverage proves insufficient.

## Environment

- Deploy llm-d infra: Gateway + EPP (router/scheduler) + `InferencePool` + real vLLM variant Deployment(s) on GPUs. **WVA not deployed.**
- Prometheus scraping vLLM (metrics port `$VLLM_METRICS_PORT`, observed `:8200`, `vllm:*`) via a modelservice-generated Pod/ServiceMonitor, surfaced through user-workload monitoring → Thanos.
- inferno control-loop deployed with RBAC to list pods/deployments and `PATCH /spec/replicas` on the variant Deployment; reachable to Prometheus.
- Requests enter via the **Gateway** (OpenAI-compatible), not the EPP directly (EPP is an Envoy ext_proc plugin).

## Experiments

**E1 — control-loop A/B (the research result).** Same llm-d env + workload; one arm stock WVA, one arm inferno-standalone. Compare SLO attainment, replica count / cost, and reaction time. Both replicas-only (WVA is anyway; inferno m\* pinned) → apples-to-apples.

**E2 — m\* value / model-validation (decoupled from the loop).** One variant, replicas pinned; sweep operating concurrency (closed-loop client concurrency, or a few EPP `maxConcurrency` restart values); show an SLO-optimal m\* exists and inferno's model predicts it. Justifies later investing in dynamic EPP reconfig. **Caveat:** keep the router from shedding (hit the pod directly, or set a high `maxConcurrency`) so client concurrency ≈ server concurrency.

## Cluster findings — confirmed 2026-07-01 (pokprod001)

Investigated live against the OpenShift cluster running llm-d + WVA. Resolutions:

- **Prometheus endpoint** — user-workload monitoring via `thanos-querier.openshift-monitoring` (HTTPS + SA bearer token + cluster CA), not `localhost:9090`. Client needs token auth, not just a URL. *(§2 updated.)*
- **Metric naming** — TTFT/occupancy/throughput/tokens as assumed; **ITL is `vllm:request_time_per_output_token_seconds`** (`request_` prefix). Bonus saturation signals (`num_requests_waiting`, `kv_cache_usage_perc`) present. *(§2 updated.)*
- **Label mapping** — `job="<ns>/<monitor>"`, **not** `job=<deployName>`; but clean `namespace`/`pod`/`model_name` labels exist. Key on those. *(§2 updated.)*
- **m\* ceiling** — vLLM `--max-num-seq` (`$VLLM_MAX_NUM_SEQ`=256), **no EPP `concurrency-detector`** in gaie v1.4.0. *(§4 updated.)*
- **WVA** — v0.8.0-rc5, `saturation` analyzer (KV/queue thresholds; scaleUp 0.85 / scaleDown 0.70). Labels: `llm-d.ai/model`, `inference.optimization/acceleratorName`, `wva.llmd.ai/controller-instance`, `llm-d.ai/inference-serving`. `VariantAutoscaling` CR (`llmd.ai/v1alpha1`) is per-variant, one WVA controller per instance.

## Target decision — 2026-07-01

A/B against **a fresh, self-owned Qwen3-32B llm-d variant** (not an existing shared namespace): deploy our own modelservice + WVA (own namespace, e.g. `inferno-workload`), TP=1 / `--max-num-seq=256` on H100, so model, traffic, GPU, and WVA on/off are all under our control → clean apples-to-apples. **Consequence:** inferno-data has `qwen_2_5_14b`, not Qwen3-32B → a **new `qwen3_32b` model profile + calibration is on the critical path** (accelerator/model/serviceclass/optimizer data; seed perfParms or rely on tuner + optional BLIS side-calibration per §5).

## Risks / remaining open questions

- **Router load distribution vs. per-server model accuracy**: prefix/load-aware scheduling spreads load unevenly; the aggregate decision is fine but α/β/γ accuracy may need the per-endpoint EPP metrics or a constrained (round-robin) router for early runs. *(Still open — validate during E1.)*
- **Qwen3-32B model profile**: no existing inferno-data profile; needs calibration (live tuner ± BLIS side-sweep). *(New, from target decision.)*
- **m\* actuation wall**: the dynamic concurrency knob is the real long-term unlock for demonstrating inferno's full value; E2 builds that case without needing it yet.

## Non-goals (this phase)

- Live m\* actuation / dynamic EPP reconfiguration.
- Porting inferno into WVA as a scaling engine (path 2).
- Multi-variant / heterogeneous-accelerator optimization on llm-d.
- Any change to server-sim (it is not in this loop).
