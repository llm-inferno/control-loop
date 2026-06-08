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
Controller    → Collector → (Prometheus + k8s labels + server-sim /simulate per pod)
              → Tuner     → (EKF-based model parameter refinement: github.com/llm-inferno/model-tuner)
              → Optimizer → (external: github.com/llm-inferno/optimizer)
              → Actuator  → (k8s deployments)
LoadEmulator  → (k8s deployment + pod labels: load metrics)
```

The five components (Controller, Collector, Optimizer, Actuator, Tuner) share the network namespace of the `inferno` pod and communicate over `localhost` on ports 3300–3304 respectively.

Each managed workload pod runs two sidecars: **server-sim** (port 8080) and **evaluator** (port 8081, `queue-analysis` mode). The Collector calls `server-sim /simulate` on each running pod via the k8s API server proxy to obtain ITL, TTFT, and throughput; ITL/TTFT are aggregated (weighted by per-pod simulated throughput in RPM) into the deployment-level `curAlloc`. Per-pod `LoadSpec.ArrivalRate` and `LoadSpec.Throughput` are both set from the simulation throughput (same value for now; a TODO exists to use a separate arrival-rate metric when available). Deployment-level `LoadSpec.ArrivalRate` and `LoadSpec.Throughput` are both read from the same Prometheus query (`vllm:request_success_total`, i.e. completion rate) as a placeholder; a TODO exists to use a separate arrival-rate query when `vllm:request_arrival_total` becomes available.

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
- `ServerCollectorInfo.ReplicaSpecs`: one `config.ServerSpec` per running pod whose simulation succeeded, named `<server>/<podName>`, with per-pod ITL/TTFT and `ArrivalRate`/`Throughput` both set from the simulation throughput. If a pod's simulation result has a non-empty `Saturation` field (set by server-sim for `bandwidth`, `kv_capacity`, or `overloaded` conditions), the Collector's behaviour depends on the deployment's `inferno.server.evaluator` label: for `queue-analysis` / `blis` (or unset), it re-simulates at progressively lower load (0.90 → 0.75 → 0.60 × `MaxRPS`, up to 3 attempts) until the result is unsaturated, so the Tuner receives well-conditioned EKF observations; if saturation persists after all attempts the pod is excluded from `ReplicaSpecs` entirely. For `vllm-server` the saturated reading is passed through as-is — re-simulation against a real vLLM pod cannot manufacture a lower-load measurement, so propagating the elevated TTFT/ITL is what lets the optimizer react to the overload.
- Static data is read once at startup from `INFERNO_DATA_PATH`; in dynamic mode (`isDynamicMode=true`) it is re-read each cycle
- `capacity-data.json` is always re-read each cycle (represents current accelerator availability)
- `numReplicas` in `curAlloc` is `Spec.Replicas` from the deployment spec

**Managed deployments** are discovered by k8s label `inferno.server.managed: "true"`. On shared clusters, scope this discovery to a single namespace with `WATCH_NAMESPACE` so two inferno setups do not iterate each other's deployments. Required labels: `inferno.server.name`, `inferno.server.model`, `inferno.server.class`, `inferno.server.allocation.accelerator`. The Load Emulator sets traffic rate statistics (RPM, token counts) by writing dynamic load labels to both the deployment and its running pods; nominal load labels (`inferno.server.load.nominal.*`) must be set on each deployment as the mean-reversion target. The Collector reads these labels (or falls back to static labels `inferno.server.load.rpm`, `inferno.server.load.intokens`, `inferno.server.load.outtokens` if Prometheus is unavailable). **The Load Emulator must be running** for pods to have non-zero load labels; without it, per-pod RPM=0 causes the evaluator sidecar's `/simulate` to return HTTP 500, resulting in empty `ReplicaSpecs` (Tuner is then skipped) and all pods contributing zero weight to the aggregated `curAlloc`.

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
| `INFERNO_LOAD_SKEW` | `0.3` | Load emulator pod skew factor (0=equal, 1=fully random) |
| `INFERNO_LOAD_PHASES` | `""` (disabled) | Path to YAML phase config file for the load emulator. When set, the nominal RPM follows the configured phase sequence (linear ramp between phases). Empty = static nominal (current behavior). |
| `INFERNO_PAIRING_TICK_SEC` | `5` | Actuator pairing-reconciler tick interval (seconds). `0` disables the reconciler. |
| `INFERNO_PAIRING_LOG_LEVEL` | `info` | Pairing-reconciler log verbosity. `info` = state-change logs only (scaling, binding, errors); `debug` = additionally logs a line per tick showing how many managed deployments were found. |
| `INFERNO_STARTUP_DELAY` | `0` | Seconds after pod `StartTime` before the pod is treated as ready; filtered from both the Collector and Load Emulator during the window |
| `INFERNO_SIMULATE_TIMEOUT_SEC` | `30` | Per-pod `/simulate` timeout used by the Collector when polling the server-sim sidecar. Default suffices for `queue-analysis`/`blis` (analytical, ms-scale). For the `vllm-server` evaluator (real vLLM, sampling window `warmupSec + maxWindowSec`, typically 90–330s), set this larger than the configured window. |
| `INFERNO_WARM_UP_TIMEOUT` | `10` | Max consecutive warm-up cycles before the controller overrides the warm-up gate and proceeds with optimize+actuate using current model data; set to `0` to disable the timeout |
| `INFERNO_CYCLE_LOG` | `inferno-cycles.jsonl` | Path to JSONL cycle log written by the controller each cycle. Set to `-` to disable. |
| `WATCH_NAMESPACE` | unset (cluster-wide) | Namespace to scope managed-deployment watches to. Set on shared clusters where another inferno setup uses the same `inferno.server.*` labels in different namespaces. Applies to the Collector, Load Emulator, and Actuator pairing reconciler. The Actuator `/update` handler is implicitly scoped via the Collector-built `serverMap` it receives. |
| `KUBECONFIG` | `$HOME/.kube/config` | Kubernetes config path |

## Data Files (in `INFERNO_DATA_PATH`)

- `accelerator-data.json` — accelerator hardware specs (static)
- `model-data.json` — LLM model profiles (static)
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

**Saturated pod re-simulation**: When a pod's simulation result has a non-empty `Saturation` field (`bandwidth`, `kv_capacity`, or `overloaded`), the resulting TTFT/ITL are degenerate and not useful for EKF tuning. Behaviour is backend-aware (gated by the deployment's `inferno.server.evaluator` label):

- **`queue-analysis` / `blis` (or unset)**: the Collector re-simulates at progressively lower load — `0.95 → 0.90 → 0.85 × MaxRPS` — stopping as soon as the result is unsaturated (`overloadMaxRetries = 3`, `overloadTargetUtilization = 0.95`, `overloadRetryStep = 0.05` in `pkg/collector/collector.go`). Both `ArrivalRate` and `Throughput` in the replicaSpec are set to the adjusted rate. If saturation persists after all attempts or a re-simulation errors, the pod is excluded from `ReplicaSpecs` entirely.
- **`vllm-server`**: re-simulation is skipped. The evaluator forwards `/simulate` to a real vLLM pod and cannot manufacture a lower-load measurement on demand, so the saturated TTFT/ITL/throughput are passed through unchanged into both the deployment-level aggregate and `ReplicaSpecs`. This propagates the overload signal to the optimizer (and to the EKF) so the controller can react.

**Tuner fault tolerance**: If the tuner container is not ready or crashes, `POSTTune` fails with a connection error. The controller logs a warning (`tuner /tune warning: ...`) and continues the cycle using `currentModelData` unchanged. The tune timing column shows ~1ms (fast fail). Cycles remain uninterrupted.

**Server startup delay** (`INFERNO_STARTUP_DELAY`): When set to a positive integer (seconds), both the Collector and Load Emulator ignore pods whose `Status.StartTime` is less than that many seconds ago. This prevents collecting metrics from or assigning traffic labels to pods still loading model weights. The check uses `pod.Status.StartTime` (set by the kubelet when the pod begins running), not `CreationTimestamp`. Default is `0` (no delay, fully backward-compatible). During the delay window the pod is excluded from `ReplicaSpecs` (Tuner is skipped for it) and receives no load labels.

**Zero perfParms blocks optimizer (EKF warm-up)**: When `model-data.json` omits `perfParms` (or all three values are `0`), `optimizer-light`'s `CreateAllocation` skips that model/accelerator pair and `Solve()` returns an error listing the affected servers. The controller logs the error and skips the cycle. Under normal operation with `TUNER_INIT_HOLD_BACK=true` (default), the controller never calls the optimizer during EKF warm-up (`warmingUp=true`), so this error is unreachable in practice. The only exception is if `INFERNO_WARM_UP_TIMEOUT` fires before the EKF converges. **When using the blis evaluator and relying on the EKF to learn perfParms from scratch, set `INFERNO_WARM_UP_TIMEOUT=0`** to disable the timeout and ensure the controller waits for full EKF convergence before invoking the optimizer.

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

Targets a shared OpenShift cluster (not kind). Uses two new namespaces — `inferno-system` (replaces `inferno`) and `inferno-workload` (replaces `infer`) — to avoid colliding with another team's existing setup. Both vLLM Deployments use `vllm/vllm-openai:v0.21.0` with `--max-num-seqs 32`, mount a shared `vllm-models-cache` PVC (RWX 100Gi on `ibm-spectrum-scale-fileset`), and read `HUGGING_FACE_HUB_TOKEN` from `hf-token-secret` (copied from the existing `infer/hf-token-secret` by the deploy script, not stored in git). perfParms in `inferno-data/vllm-gpu/model-data.json` are seeded with the converged values from the existing setup so cycle 1 produces a useful allocation. Control period is 120 s (worst-case `/collect` is ~60 s with 2 deployments × 30 s eval windows). The eval config uses `uniform-bounded` token sampling on both inputs and outputs to add per-request size variation without breaking `--max-model-len`. The cluster runs a `gpu-reaper.io` controller that scales down idle GPU pods after 30 min; the experiment's 24-min active phase sequence stays under the threshold, but vLLM Deployments left idle overnight will be reaped (cold start on next deploy, the PVC keeps weights).

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
