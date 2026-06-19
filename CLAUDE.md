# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build all binaries
go build ./...

# Run a specific component
go run cmd/controller/main.go [controlPeriodInSec] [isDynamicMode]
go run cmd/collector/main.go
go run cmd/actuator/main.go
go run cmd/optimizer/main.go
go run cmd/loademulator/main.go

# Build Docker image
docker build -t quay.io/atantawi/inferno-loop:latest . --load

# Set environment variables for local development
. scripts/common/setparms.sh
```

There are no automated tests in this repository.

## Repository layout

```
control-loop/
├── cmd/                # Go binaries (one per microservice)
├── pkg/                # Internal packages
├── manifests/          # K8s YAMLs, organized per experiment
│   ├── common/         # used by all experiments (namespaces, deploy-loop, configmap-tuner)
│   ├── qa/             # queue-analysis evaluator workload + load emulator
│   ├── blis/           # blis/trained-physics evaluator workload + load emulator
│   ├── vllm-cpu/       # vllm-server evaluator workload + paired vLLM (CPU)
│   └── samples/        # demo workloads referenced from README §II walkthrough
├── inferno-data/       # optimizer/SLO config files, per experiment
│   ├── qa/             # ... matches manifests/qa
│   ├── blis/
│   └── vllm-cpu/
├── scripts/
│   ├── common/         # kind-teardown, sync-cycle-log, local-dev helpers
│   ├── qa/             # kind-deploy.sh for queue-analysis
│   ├── blis/           # kind-deploy.sh for blis
│   └── vllm-cpu/       # kind-deploy.sh for vllm-server
├── dashboard/          # Python Dash visualization app
├── docs/               # design specs (historical record)
├── experiments/        # experiment reports (historical record)
└── sample-data/        # git submodule (realistic-scale data)
```

To add a new experiment `X`: create matching `manifests/X/`, `inferno-data/X/`, and `scripts/X/kind-deploy.sh`.

## Architecture

The system is a **closed-loop inference optimizer** for Kubernetes. It runs as five cooperating REST microservices, all deployed as containers in a single `inferno` pod (plus a separate `LoadEmulator` deployment):

```
Controller    → Collector → (Prometheus + k8s labels + server-sim GET /latest per pod)
              → Tuner     → (EKF-based model parameter refinement: github.com/llm-inferno/model-tuner)
              → Optimizer → (external: github.com/llm-inferno/optimizer)
              → Actuator  → (k8s deployments)
LoadEmulator  → (k8s deployment + pod labels: load metrics)
```

The five components (Controller, Collector, Optimizer, Actuator, Tuner) share the network namespace of the `inferno` pod and communicate over `localhost` on ports 3300–3304 respectively.

Each managed workload pod runs two sidecars: **server-sim** (port 8080) and **evaluator** (port 8081, `queue-analysis` mode). In continuous mode the server-sim sidecar runs a background traffic loop and caches each completed result; the Collector reads each pod's latest completed result via `GET /latest` (non-blocking) rather than driving `/simulate` per pod. The `/latest` response is a self-describing envelope: `effectiveInput` (the load parameters actually run), `result` (ITL, TTFT, throughput, saturation), and `completedAt` (timestamp). ITL/TTFT are aggregated (weighted by per-pod throughput in RPM) into the deployment-level `curAlloc`. Per-pod `LoadSpec.ArrivalRate` is set from `effectiveInput.RPS` (the offered load actually run by the server-sim loop) and `LoadSpec.Throughput` from `result.Throughput` (the completion rate measured for that window); the two diverge whenever the window is saturated (offered > completed), which is the intended signal under the `pass-through` policy. Deployment-level `LoadSpec.ArrivalRate` and `LoadSpec.Throughput` are both read from the same Prometheus query (`vllm:request_success_total`, i.e. completion rate) as a placeholder; a TODO exists to use a separate arrival-rate query when `vllm:request_arrival_total` becomes available.

Data/config types (`config.SystemData`, `config.AllocationData`, etc.) and `utils.FromDataToSpec` come from `github.com/llm-inferno/optimizer-light/pkg/config` and `…/pkg/utils`. The `optimizer` module depends on `optimizer-light` and re-exports its REST server; the control-loop imports `optimizer-light` directly.

**Control flow** (in `pkg/controller/controller.go:Optimize()`):
1. Controller calls `GET /collect` on the Collector to read current server state from k8s labels, Prometheus, and server-sim simulations
2. Controller calls `POST /tune` then `POST /merge` on the Tuner (if `TUNER_HOST` is set), passing `replicaSpecs` to refine model performance parameters via EKF; the merged `ModelData` replaces `State.currentModelData` and is injected into `SystemData` before the optimizer call
3. Controller calls `POST /optimizeOne` on the Optimizer with full `SystemData` (including tuned model data)
4. Controller calls `POST /update` on the Actuator with allocation decisions + k8s references
5. Actuator scales k8s deployment replicas to match the optimizer's allocation

**Data model** — `pkg/controller/`:
- `State.SystemData` (`config.SystemData` from `optimizer-light/pkg/config`): holds static files (accelerators, models, service classes, optimizer params) and dynamic server data
- `State.ServerMap`: maps server names to k8s `{uid, name, namespace}` for the Actuator to resolve deployments
- `State.originalModelData`: `ModelData` read from `model-data.json` at startup; reset each cycle in dynamic mode
- `State.currentModelData`: starts as a copy of `originalModelData`; updated each cycle with the tuner's merged output and fed into `SystemData.Spec.Models` before the optimizer call
- `ServerCollectorInfo.Spec`: one `config.ServerSpec` per managed deployment (aggregated ITL/TTFT/load)
- `ServerCollectorInfo.ReplicaSpecs`: one `config.ServerSpec` per running pod with a valid `/latest` envelope, named `<server>/<podName>`. Per-pod ITL/TTFT come from the envelope's `result`; `ArrivalRate` is set from `effectiveInput.RPS` (the offered load actually run by the server-sim loop) and `Throughput` from `result.Throughput` (the measured completion rate) — these diverge under saturation. Saturation handling now lives entirely inside the server-sim background loop, governed by `SERVERSIM_SATURATION_POLICY`: for `queue-analysis` / `blis` the policy is `retry-at-lower-load` (the loop retries at progressively lower load — `0.95 → 0.90 → 0.85 × MaxRPS` — until the result is unsaturated before caching it); for `vllm-server` the policy is `pass-through` (the saturated result is cached and propagated so the optimizer can react to overload). The Collector itself does no saturation re-simulation. The Collector additionally applies a causal-coherence check: a pod whose `effectiveInput` concurrency differs from the currently in-force `maxbatchsize` label is skipped (stale result from a prior allocation), so it does not contribute to `ReplicaSpecs` or the deployment-level aggregate.
- Static data is read once at startup from `INFERNO_DATA_PATH`; in dynamic mode (`isDynamicMode=true`) it is re-read each cycle
- `capacity-data.json` is always re-read each cycle (represents current accelerator availability)
- `numReplicas` in `curAlloc` is `Spec.Replicas` from the deployment spec

**Managed deployments** are discovered by k8s label `inferno.server.managed: "true"`. On shared clusters, scope this discovery to a single namespace with `WATCH_NAMESPACE` so two inferno setups do not iterate each other's deployments. Required labels: `inferno.server.name`, `inferno.server.model`, `inferno.server.class`, `inferno.server.allocation.accelerator`. The Load Emulator sets traffic rate statistics (RPM, token counts) by writing dynamic load labels to both the deployment and its running pods; nominal load labels (`inferno.server.load.nominal.*`) must be set on each deployment as the mean-reversion target. The Collector reads these labels (or falls back to static labels `inferno.server.load.rpm`, `inferno.server.load.intokens`, `inferno.server.load.outtokens` if Prometheus is unavailable). **The Load Emulator must be running** for pods to carry load labels; without it the server-sim traffic generator has no workload, so the Collector's `GET /latest` returns no usable result (cold-start 404 or empty window), those pods contribute nothing to `ReplicaSpecs` or `curAlloc`, and the Tuner is skipped.

**vllm-server pairing labels** (only relevant when using the `vllm-server` evaluator backend from `server-sim`):

- `inferno.server.evaluator` — evaluator backend (`vllm-server`, `queue-analysis`, or `blis`). Only `vllm-server` triggers the pairing reconciler.
- `inferno.server.vllm-deployment` — name of the paired vLLM Deployment that the Actuator will keep replica-locked with the managed Deployment.
- `inferno.server.vllm-namespace` — namespace of the vLLM Deployment; defaults to the managed Deployment's namespace.
- `inferno.server.pair-id` — UUID written by the Actuator on one managed pod and one vLLM pod per replica. Read at startup by the `vllm-server` evaluator sidecar (via the downward API) to resolve its paired vLLM pod IP.

The vLLM Deployment's **pod template** must carry `inferno.vllm.model` (any non-empty value, e.g. `granite`). The evaluator uses this label as a disambiguator when resolving its paired vLLM pod — without it the pod selector matches both the managed and vLLM pods and pairing fails. The Actuator does not set this label; it must be present in the vLLM Deployment manifest.

See [`docs/superpowers/specs/2026-05-29-actuator-vllm-pairing-design.md`](docs/superpowers/specs/2026-05-29-actuator-vllm-pairing-design.md) for the four-invariant contract.

**Tuner ConfigMap requirement**: The Tuner container requires a `model-tuner-config` ConfigMap in the `inferno` namespace, mounted at `/etc/tuner/config` and referenced via `CONFIG_DATA_DIR`. This ConfigMap holds the EKF filter and model parameter configuration (see `github.com/llm-inferno/model-tuner/config-data/` for examples). Without it the tuner container will fail to start.

**Collector RBAC requirements**: The `inferno` ClusterRole must include `replicasets` in the `apps` API group (to find pods owned by a deployment via its ReplicaSet) and a `pods/proxy` rule with `get, create` verbs (to reach pod sidecars through the k8s API server proxy). Without `replicasets`, the Collector cannot discover running pods. Without `pods/proxy`, the `/simulate` calls to server-sim fail with 403.

**Controller** also exposes `GET /invoke` for on-demand (aperiodic) control cycles. Both periodic and aperiodic modes run simultaneously; the mutex in `Optimize()` serializes concurrent calls.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `CONTROLLER_HOST` | `""` (all interfaces) | Controller server listen address |
| `CONTROLLER_PORT` | `8080` | Controller server listen port |
| `COLLECTOR_HOST` | `""` (all interfaces) | Collector server listen address; `localhost` when used as client target |
| `COLLECTOR_PORT` | `8080` | Collector server listen port |
| `INFERNO_HOST` | `localhost` | Optimizer client target address |
| `INFERNO_PORT` | `8080` | Optimizer client target port |
| `ACTUATOR_HOST` | `""` (all interfaces) | Actuator server listen address; `localhost` when used as client target |
| `ACTUATOR_PORT` | `8080` | Actuator server listen port |
| `TUNER_HOST` | unset (Tuner disabled) | Tuner client target address; set to `localhost` when Tuner runs as a sidecar in the same pod |
| `TUNER_PORT` | `8081` | Tuner client target port (`3304` in the inferno pod deployment) |
| `TUNER_WARM_UP_CYCLES` | `5` | Number of accepted EKF updates during which the NIS gate is disabled; set to `0` to disable warm-up |
| `TUNER_INIT_OBS` | `5` | Observations to accumulate before the multi-observation Nelder-Mead fit; set to `1` to revert to single-observation `guessInitState` behaviour (`3` in deploy-loop.yaml) |
| `TUNER_INIT_HOLD_BACK` | `true` | If `true`, the tuner reports `warmingUp=true` during collection so the controller skips optimize+actuate (Option B). Set `false` to let the controller proceed with static model-data during collection (Option A). |
| `INFERNO_DATA_PATH` | `./` | Path to JSON data files (must end with `/`) |
| `INFERNO_CONTROL_PERIOD` | `60` | Control loop period in seconds (0 = aperiodic only) |
| `INFERNO_CONTROL_DYNAMIC` | `false` | Re-read static data each cycle |
| `INFERNO_LOAD_INTERVAL` | `20` | Load emulator update interval in seconds |
| `INFERNO_LOAD_ALPHA` | `0.1` | Load emulator noise magnitude relative to nominal |
| `INFERNO_LOAD_THETA` | `0.2` | Load emulator mean-reversion strength |
| `INFERNO_LOAD_SKEW` | `0.3` | Load emulator pod skew factor. Splits RPM across a deployment's pods (0=equal split, 1=fully random) **and** perturbs each pod's input/output token counts by ±skew around the deployment nominal (independent draws, clamped to the `LoadRangeFactor` band). The per-replica token spread gives the tuner's EKF a range of operating points within a single cycle so it can identify the per-token (beta) and batch (gamma) coefficients separately rather than collapsing them into alpha. `0` reproduces the legacy behaviour (equal split, identical token counts broadcast to all pods). |
| `INFERNO_LOAD_PHASES` | `""` (disabled) | Path to YAML phase config file for the load emulator. When set, the nominal RPM follows the configured phase sequence (linear ramp between phases). Empty = static nominal (current behavior). |
| `INFERNO_PAIRING_TICK_SEC` | `5` | Actuator pairing-reconciler tick interval (seconds). `0` disables the reconciler. |
| `INFERNO_PAIRING_LOG_LEVEL` | `info` | Pairing-reconciler log verbosity. `info` = state-change logs only (scaling, binding, errors); `debug` = additionally logs a line per tick showing how many managed deployments were found. |
| `INFERNO_STARTUP_DELAY` | `0` | Seconds after pod `StartTime` before the pod is treated as ready; filtered from both the Collector and Load Emulator during the window |
| `INFERNO_SIMULATE_TIMEOUT_SEC` | `30` | Per-pod `GET /latest` timeout used by the Collector when reading from the server-sim sidecar via the k8s API-server proxy. In continuous mode the Collector does a non-blocking `GET /latest` read; the timeout bounds the k8s proxy round-trip only (not an evaluation window). Increase if the proxy path is slow; the default of 30s is ample for a in-cluster proxy call. |
| `SERVERSIM_CONTINUOUS` | `false` | Enable the server-sim continuous traffic generator. When `true`, server-sim starts a background ticker loop that runs evaluation windows back-to-back and stores the latest completed result; the Collector reads it via `GET /latest`. When `false`, server-sim uses the legacy on-demand `/simulate` POST path. |
| `SERVERSIM_TICK_SECONDS` | `5` | Tick interval for the server-sim continuous generator loop (seconds). Must be ≥ 1. For instantaneous backends (`queue-analysis`, `blis`), each window completes in milliseconds and the tick caps recompute rate. For `vllm-server`, the window (`warmupSec + maxWindowSec`) dominates and the tick interval is effectively the window length. |
| `SERVERSIM_SATURATION_POLICY` | (backend default) | Saturation handling policy for the continuous generator. `retry-at-lower-load`: re-run at `0.95 → 0.90 → 0.85 × MaxRPS` until unsaturated (use for `queue-analysis` / `blis`). `pass-through`: publish the saturated measurement as-is (use for `vllm-server`, where a lower-load re-run is not meaningful). Set on the server-sim sidecar container in the workload manifest. |
| `SERVERSIM_LABELS_DIR` | `/etc/podinfo` | Directory where the server-sim continuous generator reads the pod's downward-API labels file (`labels`). The Load Emulator writes `rpm`/`intokens`/`outtokens` to pod labels; the Actuator writes `maxbatchsize` to pod labels; the downward-API volume projects both into this directory so the generator picks up live updates without an API round-trip. |
| `INFERNO_WARM_UP_TIMEOUT` | `10` | Max consecutive warm-up cycles before the controller overrides the warm-up gate and proceeds with optimize+actuate using current model data; set to `0` to disable the timeout |
| `DEFAULT_MAX_BATCH_SIZE` | unset (search enabled) | Optional escape hatch. When > 0, the controller pins `ServerSpec.MaxBatchSize` on every server, which the optimizer treats as an explicit concurrency **override** — skipping the optimal-concurrency search entirely. Leave unset to let `optimizer-light` v0.8.0 search the optimal concurrency `M*` per (server, accelerator). |
| `INFERNO_CYCLE_LOG` | `inferno-cycles.jsonl` | Path to JSONL cycle log written by the controller each cycle. Set to `-` to disable. |
| `WATCH_NAMESPACE` | unset (cluster-wide) | Namespace to scope managed-deployment watches to. Set on shared clusters where another inferno setup uses the same `inferno.server.*` labels in different namespaces. Applies to the Collector, Load Emulator, and Actuator pairing reconciler. The Actuator `/update` handler is implicitly scoped via the Collector-built `serverMap` it receives. |
| `KUBECONFIG` | `$HOME/.kube/config` | Kubernetes config path |

## Data Files (in `INFERNO_DATA_PATH`)

- `accelerator-data.json` — accelerator hardware specs (static)
- `model-data.json` — LLM model profiles (static). `maxBatchSize` is the **search ceiling** for the optimizer's optimal-concurrency search (`0` ⇒ `DefaultConcurrencyCeiling` = 256), not a fixed batch size. For real-vLLM experiments keep it equal to the pod's `--max-num-seqs` so the searched `M*` can never exceed what the server honors. (The retired `atTokens` field is no longer read.)
- `serviceclass-data.json` — SLA/service class definitions (static)
- `optimizer-data.json` — optimizer parameters (static)
- `capacity-data.json` — current accelerator capacity counts (re-read each cycle)

Sample data is in the `sample-data/` git submodule (`sample-data/large/` has realistic-scale data).

The load emulator phase sequence is configured per-experiment via `manifests/{qa,blis,vllm-cpu}/configmap-load-phases.yaml`, delivered to the pod as the `load-phases-config` ConfigMap mounted at `/etc/loadphases/`.

## Known Behaviours and Operational Notes

**Tuner EKF convergence in synthetic environments**: In test environments where server-sim uses the same alpha/beta/gamma parameters it is simulating, the tuner's EKF will converge immediately to the static file values — there is no discrepancy to correct. EKF divergence from static values only occurs with real LLM servers whose actual behaviour differs from the initial parameter estimates.

**Tuner skipped when replicaSpecs empty**: The tuner block is skipped silently (tune: 0ms in timing log) when `len(collectorInfo.ReplicaSpecs) == 0`. This happens when all pod simulations fail (evaluator 500/400). Check evaluator logs if tune time is consistently 0 despite pods running.

**Evaluator 500 for missing model/accelerator**: The evaluator sidecar returns HTTP 500 when the requested model+accelerator combination is not in its `model-data.json` config. Each workload deployment's evaluator must be configured with a `model-data.json` that includes an entry for its `inferno.server.model` label paired with the accelerator assigned by the optimizer. Missing entries cause the pod's simulation to fail, resulting in empty `ReplicaSpecs` and the tuner being skipped.

**ConfigMap propagation delay in dynamic mode**: When `INFERNO_CONTROL_DYNAMIC=true`, static data files are re-read from the mounted ConfigMap each cycle. ConfigMap updates take ~30–60 seconds to propagate to mounted volumes (kubelet sync period). Changes are not reflected until the next cycle after the file is updated on disk.

**Saturated pod re-simulation** (legacy on-demand path; continuous mode moves this to the server-sim loop via `SERVERSIM_SATURATION_POLICY`): When a pod's simulation result has a non-empty `Saturation` field (`bandwidth`, `kv_capacity`, or `overloaded`), the resulting TTFT/ITL are degenerate and not useful for EKF tuning. In the legacy on-demand `/simulate` path, behaviour is backend-aware (gated by the deployment's `inferno.server.evaluator` label):

- **`queue-analysis` / `blis` (or unset)**: the Collector re-simulates at progressively lower load — `0.95 → 0.90 → 0.85 × MaxRPS` — stopping as soon as the result is unsaturated (`overloadMaxRetries = 3`, `overloadTargetUtilization = 0.95`, `overloadRetryStep = 0.05` in `pkg/collector/collector.go`). Both `ArrivalRate` and `Throughput` in the replicaSpec are set to the adjusted rate. If saturation persists after all attempts or a re-simulation errors, the pod is excluded from `ReplicaSpecs` entirely.
- **`vllm-server`**: re-simulation is skipped. The evaluator forwards `/simulate` to a real vLLM pod and cannot manufacture a lower-load measurement on demand, so the saturated TTFT/ITL/throughput are passed through unchanged into both the deployment-level aggregate and `ReplicaSpecs`. This propagates the overload signal to the optimizer (and to the EKF) so the controller can react.

In continuous mode (`SERVERSIM_CONTINUOUS=true`), these Collector branches are removed; the equivalent logic runs inside the server-sim ticker loop under `SERVERSIM_SATURATION_POLICY`. See the **Continuous traffic generator** note below.

**`maxConcurrency` resolution contract (server-sim)**: The Collector sends `maxConcurrency` on every `/simulate` request, sourced from the deployment's `inferno.server.allocation.maxbatchsize` label (`pkg/collector/handlers.go`); a missing label yields `0`. As of server-sim PR #18, all evaluator backends resolve `maxConcurrency == 0` uniformly: **request value > 0 → per-model/per-server configured default > 0 → `evaluator.DefaultMaxConcurrency` (256, logged loudly)**. So an absent `maxbatchsize` label no longer silently uses a backend-specific number — it falls through to each backend's config default, then to the 256 backstop. Implications for this repo:
  - **vllm-server**: the evaluator's `vllm-eval-config.json` (e.g. `manifests/vllm-gpu/configmap-vllm-eval.yaml`) accepts an optional `defaultConcurrency` field that should match the paired vLLM deployment's `--max-num-seqs`. We do **not** set it: every managed vllm workload here already carries a `maxbatchsize` label equal to `--max-num-seqs` (gpu=`32`, cpu=`8`), so the request value always wins and the 256 backstop is unreachable. If that label is ever dropped from a vllm-server deployment, add `defaultConcurrency` to the eval configmap so the evaluator falls back to the real `--max-num-seqs` rather than 256.
  - **queue-analysis / blis**: unaffected in practice — both already fall back to their per-model `maxBatchSize` / `maxRunningReqs` config when the request omits the value, and every managed deployment here sets the `maxbatchsize` label anyway.
  - server-sim is consumed only via the `/simulate` REST contract and the server-sim + evaluator **container images** (not a Go-module dependency), and PR #18 left the request/response schema unchanged — so picking up this behaviour requires only rebuilding/redeploying the `:latest` server-sim + evaluator images, with no control-loop code change.

**Optimal-concurrency batch sizing (optimizer-light v0.8.0)**: For each (server, accelerator) candidate, the optimizer asks the queue analyzer for the minimum concurrency `M*` that reaches near-peak throughput under the SLO (`queue-analysis` `OptimalConcurrency`), replacing the old `maxBatchSize × atTokens / K` linear heuristic (`atTokens` is retired). `perf.MaxBatchSize` in `model-data.json` is the search **ceiling** (`0` ⇒ 256); the searched `M*` is emitted as `AllocationData.MaxBatch`. The control-loop is already shaped for this — **no Go code change in the Collector/Actuator/Tuner/Load Emulator**:
  - **Actuator** writes `M*` to the `inferno.server.allocation.maxbatchsize` label each cycle (`pkg/actuator/handlers.go`).
  - **Collector** reads that label back into `CurrentAlloc.MaxBatch` (informational + the `/simulate` `maxConcurrency`); it never sets the optimizer *override* (`ServerSpec.MaxBatchSize`), so the search runs every cycle.
  - **Tuner** gets concurrency purely from the per-pod replicaSpecs the controller POSTs to `/tune` — `CurrentAlloc.MaxBatch` (`model-tuner/pkg/service/utils.go`), **not** from `model-tuner-config`, which holds only EKF filter params and α/β/γ init state. So the EKF observes at whatever `M*` the optimizer last chose, keeping the fit consistent.
  - The single knob that disables the feature is `DEFAULT_MAX_BATCH_SIZE` (see env table): when set it pins the override and the search never runs. It is left unset in `deploy-loop.yaml` and the deploy scripts.
  - Runtime behaviour lives in the `inferno-optimizer-light:latest` image; rebuild it (and `inferno-tuner:latest`, which also dropped `atTokens`) from the v0.8.0 modules. The control-loop's `optimizer`/`optimizer-light` go.mod pins are bumped to v0.8.0 for shared-config-type consistency.

**Enabling / disabling concurrency control**: The feature is the optimizer's per-`(server, accelerator)` optimal-concurrency *search* (above). The single switch is the `DEFAULT_MAX_BATCH_SIZE` env var on the **controller** container:

- **Unset / empty / `0` → search ENABLED** (default; not present in `deploy-loop.yaml`). The optimizer searches `M*` every cycle.
- **`> 0` → search DISABLED.** The controller pins `ServerSpec.MaxBatchSize` on every server (`pkg/controller/controller.go:241`, applied only when not already set), which the optimizer treats as an explicit concurrency **override**, skipping the search.

The switch is *not* any of the several other fields also named `maxBatchSize` — these are ceiling / fallback / seed values that do not toggle the feature:

| Where you see it | Example | What it actually is | Toggles the feature? |
|---|---|---|---|
| `DEFAULT_MAX_BATCH_SIZE` env on the **controller** | `"128"` | The on/off switch — pins the optimizer override when `> 0` | **Yes — this is the knob** |
| `maxBatchSize` in `inferno-data/*/model-data.json` | `128` | Search **ceiling** (`0` ⇒ 256); bounds `M*` from above | No |
| `maxBatchSize` in the evaluator config (`manifests/qa/configmap-qa-small.yaml`) | `128` | The **server-sim/evaluator sidecar's** per-model concurrency, used as the `/simulate` `maxConcurrency` fallback when the request sends `0` | No |
| `inferno.server.allocation.maxbatchsize` label on `dep-qa-*.yaml` | `"128"` | **Seed** value; the Actuator overwrites it with the searched `M*` each cycle, and the Collector reads it back (informational + the `/simulate` `maxConcurrency`) | No |

When the search is enabled the deployment label changes cycle-to-cycle (Actuator writes `M*`); when disabled it stays pinned to whatever fixed value you set. To run an A/B contrast of the feature: Arm A leaves `DEFAULT_MAX_BATCH_SIZE` unset (search on); Arm B sets it to a fixed value (e.g. `128`, matching the seeds) on the controller container (search off, legacy fixed-batch behaviour).

**Tuner fault tolerance**: If the tuner container is not ready or crashes, `POSTTune` fails with a connection error. The controller logs a warning (`tuner /tune warning: ...`) and continues the cycle using `currentModelData` unchanged. The tune timing column shows ~1ms (fast fail). Cycles remain uninterrupted.

**Server startup delay** (`INFERNO_STARTUP_DELAY`): When set to a positive integer (seconds), both the Collector and Load Emulator ignore pods whose `Status.StartTime` is less than that many seconds ago. This prevents collecting metrics from or assigning traffic labels to pods still loading model weights. The check uses `pod.Status.StartTime` (set by the kubelet when the pod begins running), not `CreationTimestamp`. Default is `0` (no delay, fully backward-compatible). During the delay window the pod is excluded from `ReplicaSpecs` (Tuner is skipped for it) and receives no load labels.

**Zero perfParms blocks optimizer (EKF warm-up)**: When `model-data.json` omits `perfParms` (or all three values are `0`), `optimizer-light`'s `CreateAllocation` skips that model/accelerator pair and `Solve()` returns an error listing the affected servers. The controller logs the error and skips the cycle. Under normal operation with `TUNER_INIT_HOLD_BACK=true` (default), the controller never calls the optimizer during EKF warm-up (`warmingUp=true`), so this error is unreachable in practice. The only exception is if `INFERNO_WARM_UP_TIMEOUT` fires before the EKF converges. **When using the blis evaluator and relying on the EKF to learn perfParms from scratch, set `INFERNO_WARM_UP_TIMEOUT=0`** to disable the timeout and ensure the controller waits for full EKF convergence before invoking the optimizer.

**EKF identifiability and operating-point spread**: The tuner fits α/β/γ from per-replica observations. When all observations for a `(model, accelerator)` pair sit at one operating point — a single-replica deployment, or (before this change) every replica sharing the same broadcast token counts — β and γ are unidentifiable and the fit can collapse them toward zero while inflating α. Two mitigations work together:
- **Load Emulator** perturbs each replica's token counts by ±`INFERNO_LOAD_SKEW` around the deployment nominal (independent draws), giving the EKF within-cycle operating-point spread. This only helps **multi-replica** deployments — a single-replica deployment has one operating point per cycle regardless.
- **Tuner identifiability guard** (`TUNER_MAX_CONDITION_NUMBER`, default 1000 in `model-tuner`): rejects a fit whose Jacobian condition number is too high (degenerate/unidentifiable), holding the last good fit or the analytical `GuessInitState` instead of emitting collapsed β/γ. This prevents the optimizer-blocking 404 cascade and the EKF lock-in.

Known limitation: at **cold start** (no prior good fit) for a **single-replica** pair under **heavy load**, the `GuessInitState` fallback derives α ≈ 0.9·ITL from a single queue-inflated observation, so α can be ~10× too high — non-degenerate, but inaccurate enough that the optimizer may find the pair infeasible (`404 Not Found`, a feasibility failure rather than missing perfParms). Tracked in `model-tuner` #17; see that issue for follow-ups (load-aware guess, seeded priors, warm-up min-replica floor).

**Continuous traffic generator** (`SERVERSIM_CONTINUOUS=true`): When enabled, each server-sim sidecar runs a background ticker loop that drives evaluation windows back-to-back without waiting for the Collector to kick them. One window at a time; the evaluator's internal mutex serializes concurrent `/solve` calls. Each iteration reads the effective input (RPS, token sizes, concurrency `M*`) from the pod's downward-API labels volume (`SERVERSIM_LABELS_DIR`), runs the evaluator, applies the configured saturation policy (`SERVERSIM_SATURATION_POLICY`), and stores the completed result — `{effectiveInput, result, completedAt}` — as the latest completed job.

- **`GET /latest` envelope**: The Collector reads results via a non-blocking `GET /latest` call to the server-sim sidecar. The response is a self-describing envelope carrying `effectiveInput` (the operating point actually run, including any saturation-driven load reduction), `result` (ITL, TTFT, throughput, saturation), and `completedAt`. Before the first window completes, `GET /latest` returns `404`; the Collector treats this identically to a failed simulation — the pod is excluded from `ReplicaSpecs` and contributes no weight to `curAlloc`.
- **Branchless Collector path**: With continuous mode, the per-pod Collector path collapses to a single uniform operation: `GET /latest` → parse envelope → build replicaSpec. The backend-specific saturation branches (`queue-analysis` / `blis` retry-at-lower-load; `vllm-server` pass-through) are removed from the Collector and replaced by `SERVERSIM_SATURATION_POLICY` in the server-sim loop. The Collector has no remaining per-backend code paths.
- **Saturation policy in server-sim**: `retry-at-lower-load` re-runs at `0.95 → 0.90 → 0.85 × MaxRPS` (matching the former Collector constants `overloadMaxRetries = 3`, `overloadRetryStep = 0.05`) until unsaturated; the reduced rate is reported in `effectiveInput` so the EKF observes a coherent `(load, perf)` pair. `pass-through` publishes the saturated reading unchanged — appropriate for `vllm-server` where a re-run at lower load cannot be manufactured.
- **Actuator writes allocation to running pods**: In continuous mode, the Actuator patches `maxbatchsize` (and `accelerator`) onto each running pod each cycle (in addition to the deployment). Pods are filtered to those owned by the deployment's current ReplicaSets (the same ownership discipline the Collector uses), so a draining old-rollout pod — or a foreign deployment that happens to share the label selector — is not relabelled. The downward-API labels volume propagates these to the server-sim sidecar, which reads them on each window start. This is the same channel used for load labels (`rpm`/`intokens`/`outtokens`) written by the Load Emulator; no new RBAC is required (the shared `inferno` ClusterRole already grants `apps/replicasets` list and `pods` patch). Per-pod patch failures are best-effort — the cycle is not aborted — but are surfaced via a logged warning (`pod allocation patch warning: …`) rather than swallowed: a silently dropped `maxbatchsize` patch would otherwise leave the pod's label stale and the coherence check would exclude it from `ReplicaSpecs` every cycle with no diagnostic.
- **Causal coherence check**: When the Collector reads a `/latest` envelope, it compares `effectiveInput.concurrency` against the `M*` that was in force at the previous decision (read from the pod's `inferno.server.allocation.maxbatchsize` label). A match means the window completed under the current allocation and the observation is fed to the tuner/optimizer. A mismatch means the window ran under an older allocation (not yet replaced by a fresh post-decision window); the pod's observation is treated as stale, logged, and excluded from `ReplicaSpecs` for this cycle. Staleness is detected, not silent. This is the causal gating that restores the `decision → observation → decision` chain without blocking the control loop.
- **Allocation edge-detection**: The generator restarts its current window immediately when `M*` changes (detected by re-reading the labels volume at each window start), so a fresh post-decision observation is available within the next window rather than one full window length later. Load-label changes (OU noise from the Load Emulator) do **not** restart the window — only allocation changes gate it, so continuously jittering load labels do not prevent window completion.
- **Cold-start 404**: On first deploy (or after a pod restart), the first window has not yet completed. The Collector's `GET /latest` returns 404; the pod is skipped. The system is fully operational once the first window completes (`warmupSec + maxWindowSec` for `vllm-server`; one tick interval for `queue-analysis`/`blis`).

**Control-period invariant for vllm-server** (continuous mode): For the `vllm-server` backend, `INFERNO_CONTROL_PERIOD` must exceed `warmupSec + maxWindowSec` (from the eval config) plus collect/decide/actuate slack. This ensures a post-decision evaluation window completes within the cycle so the Collector can consume a coherent, non-stale result at cycle N+1. If the control period is shorter than the window, the Collector will repeatedly detect stale readings (the `effectiveInput.concurrency` mismatch) and report the pod as not contributing until a full window under the new allocation finishes. The period is not enforced at config load time; the coherence check is the safety valve.

## Visualization

The controller emits one JSON line per completed cycle to `INFERNO_CYCLE_LOG` (default: `inferno-cycles.jsonl` relative to the working directory). Warm-up cycles (tuner not yet converged) do not produce a record.

Each record contains: timestamp, cycle counter, per-server workload (RPM, tokens), per-server attained ITL/TTFT with SLO targets, per-server allocation (replicas, cost, accelerator), total cost, EKF model parameters (alpha/beta/gamma), and cycle phase timings.

The `pkg/monitor/` package handles all logging:
- `record.go` — `CycleRecord` and sub-struct definitions (the JSON schema)
- `builder.go` — `BuildRecord()` assembles a record from controller data; SLO targets are looked up by matching server class → service class → model target
- `monitor.go` — `CycleRecorder` writes records; nil-receiver pattern makes all methods no-ops when logging is disabled

The `dashboard/` directory contains a standalone Python Dash app (`dashboard.py`) that reads the JSONL file and displays four auto-refreshing panels: Workload, Performance, Controls, and EKF Internals. The internals panel is filtered to only show model/accelerator pairs actively assigned to deployed servers. See `dashboard/requirements.txt` for Python dependencies and README for run instructions.

## Local kind Cluster: Build and Deploy

### Prerequisites

- [kind](https://kind.sigs.k8s.io/) cluster running (`kind create cluster --name kind-cluster`)
- Docker runtime (images built with `docker build` and loaded via `kind load docker-image`)
- Sibling repos checked out under the same parent directory as `control-loop`:
  - `../optimizer-light`, `../model-tuner`, `../server-sim`
- `sample-data` submodule initialized (`git submodule update --init`)

### Step 1: Build images (run in parallel)

```bash
# From control-loop/
docker build -t quay.io/atantawi/inferno-loop:latest .

# From ../optimizer-light/
docker build -t quay.io/atantawi/inferno-optimizer-light:latest .

# From ../model-tuner/
docker build -t quay.io/atantawi/inferno-tuner:latest .

# From ../server-sim/
docker build -f Dockerfile.server-sim -t quay.io/atantawi/inferno-server-sim:latest .
docker build -f Dockerfile.evaluator  -t quay.io/atantawi/inferno-evaluator:latest .
```

All YAML files use `imagePullPolicy: IfNotPresent`, so kind will use locally-loaded images and never pull from quay.io.

### Step 2: Load images + deploy

```bash
# From control-loop/
scripts/qa/kind-deploy.sh
```

See `scripts/qa/kind-deploy.sh` for the full deploy sequence (load images → namespaces → ConfigMaps → inferno pod → workloads → load-emulator).

### Workloads

**queue-analysis workloads** (`scripts/qa/kind-deploy.sh`):

| Deployment | Model | Accelerator | Evaluator | Class |
|---|---|---|---|---|
| `dep-qa-granite.yaml` | `granite_8b` | H100 | queue-analysis | Premium |
| `dep-qa-llama.yaml` | `llama_13b` | H100 | queue-analysis | Bronze |

Both use `configmap-qa-small.yaml` and `inferno-data/qa/` for optimizer/SLO config.

**blis/trained-physics workloads** (`scripts/blis/kind-deploy.sh`):

| Deployment | Model | Accelerator | Evaluator | Class |
|---|---|---|---|---|
| `dep-blis-granite.yaml` | `granite_8b` | H100 | blis/trained-physics | Premium |
| `dep-blis-llama.yaml` | `llama_13b` | H100 | blis/trained-physics | Bronze |

Both use `configmap-blis-small.yaml` (betaCoeffs/alphaCoeffs for trained-physics) and `inferno-data/blis/` for optimizer/SLO config. `INFERNO_WARM_UP_TIMEOUT=0` is set so the optimizer waits for full EKF convergence before running.

**vllm-gpu workloads** (`scripts/vllm-gpu/oc-deploy.sh`):

| Deployment | Model | Accelerator | Evaluator | Class |
|---|---|---|---|---|
| `dep-vllm-qwen-server.yaml` | `qwen_2_5_14b` (Qwen2.5-14B-Instruct) | H100 | vllm-server | Bronze |
| `dep-vllm-llama-server.yaml` | `llama_3_1_8b` (unsloth/Meta-Llama-3.1-8B-Instruct, non-gated) | H100 | vllm-server | Premium |

Targets a shared OpenShift cluster (not kind). Uses two new namespaces — `inferno-system` (replaces `inferno`) and `inferno-workload` (replaces `infer`) — to avoid colliding with another team's existing setup. Both vLLM Deployments use `vllm/vllm-openai:v0.21.0` with `--max-num-seqs 32`, mount a shared `vllm-models-cache` PVC (RWX 100Gi on `ibm-spectrum-scale-fileset`), and read `HUGGING_FACE_HUB_TOKEN` from `hf-token-secret` (created by the deploy script from the local `HUGGING_FACE_HUB_TOKEN` / `HF_TOKEN` env var, not stored in git). perfParms in `inferno-data/vllm-gpu/model-data.json` are seeded with the converged values from the existing setup so cycle 1 produces a useful allocation. The seeding is an accelerator, not a requirement — the tuner's EKF learns these from observations, and the controller's warm-up gate prevents the optimizer from running on uninitialised values; seeding just skips the warm-up wait. Control period is 120 s (worst-case `/collect` is ~60 s with 2 deployments × 30 s eval windows). The eval config uses `uniform-bounded` token sampling on both inputs and outputs to add per-request size variation without breaking `--max-model-len`. The cluster runs a `gpu-reaper.io` controller that scales down idle GPU pods after 30 min; the experiment's 24-min active phase sequence stays under the threshold, but vLLM Deployments left idle overnight will be reaped (cold start on next deploy, the PVC keeps weights).

### Useful commands after deploy

```bash
# Watch controller logs (cycle timing, tune/optimize/actuate)
kubectl logs -f -n inferno deployment/inferno -c controller

# Watch tuner EKF output (alpha/beta/gamma per cycle)
kubectl logs -f -n inferno deployment/inferno -c tuner

# Watch load emulator (RPM updates)
kubectl logs -f -n inferno pod/load-emulator

# Trigger an on-demand control cycle
kubectl exec -n inferno deployment/inferno -c controller -- \
  wget -qO- http://localhost:3300/invoke

# Check pod simulation directly (replace <pod> and <ns>)
kubectl get --raw /api/v1/namespaces/<ns>/pods/<pod>/proxy/simulate

# Patch nominal RPM on a deployment (triggers load change experiment)
kubectl patch deployment <name> -n infer \
  --type=json -p='[{"op":"replace","path":"/metadata/labels/inferno.server.load.nominal.rpm","value":"300"}]'
```

## Integration Test Results (k3s / Rancher Desktop)

Tested with the queue-analysis workload set (`dep-qa-granite`, `dep-qa-llama`):

| Experiment | Observation |
|---|---|
| Tuner convergence | EKF stable at static values each cycle (expected: simulator uses same params it estimates) |
| Load variation → scaling | Raising nominal RPM from 60→300 caused scale-out 1→2 replicas at ~113 RPM; scale-in when load reverted |
| Tuner fault tolerance | Killing the tuner container: controller logs `connection refused` warning each cycle, optimize+actuate continue normally |
| Dynamic mode | `INFERNO_CONTROL_DYNAMIC=true`: ConfigMap edit (`saturationPolicy`) picked up within one cycle after kubelet sync; no errors, no restart needed |

Typical cycle timing (2 managed deployments, k3s single-node): `collect: ~220ms  tune: 2–3ms  optimize: ~30ms  actuate: ~10ms  total: ~265ms`
