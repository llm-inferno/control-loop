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

Each managed workload pod runs two sidecars: **server-sim** (port 8080) and **evaluator** (port 8081, `queue-analysis` mode). In continuous mode the server-sim sidecar runs a background traffic loop and caches each completed result; the Collector reads each pod's latest completed result via `GET /latest` (non-blocking) rather than driving `/simulate` per pod. (For the **simulator backends** `queue-analysis`/`blis`, `SERVERSIM_CONTINUOUS=false`: there is no background loop — server-sim's `GET /latest` computes the result on demand from the current labels each time the Collector reads it. The Collector path is identical and mode-oblivious; see the non-continuous note in [`docs/operational-notes.md`](docs/operational-notes.md).) The `/latest` response is a self-describing envelope: `effectiveInput` (the load parameters actually run), `result` (ITL, TTFT, throughput, saturation), and `completedAt` (timestamp). ITL/TTFT are aggregated (weighted by per-pod throughput in RPM) into the deployment-level `curAlloc`. Per-pod `LoadSpec.ArrivalRate` is set from `effectiveInput.RPS` (the offered load actually run by the server-sim loop) and `LoadSpec.Throughput` from `result.Throughput` (the completion rate measured for that window); the two diverge whenever the window is saturated (offered > completed), which is the intended signal under the `pass-through` policy. For the `continuous-vllm-server` backend (persistent arrival loop), `effectiveInput.RPS` is the **window-averaged** offered load — arrivals counted over the same trailing window as throughput/latency (before the concurrency limiter, so limiter-dropped arrivals still count as offered demand) — not the instantaneous setpoint of the latest `/solve`; this keeps the `(ArrivalRate, Throughput, ITL/TTFT)` triple the optimizer and tuner consume temporally consistent (server-sim `AnalysisData.OfferedRPS`, server-sim#26). Deployment-level `LoadSpec.ArrivalRate` is the **Σ over reporting pods** (when every replica reports — see the partial-reporting fallback below) of each pod's offered load (per-pod `ArrivalRate` = `effectiveInput.RPS × 60`), and `LoadSpec.Throughput` is the **Σ** of each pod's completion — both drawn from the same reporting set, so the deployment-level `(ArrivalRate, Throughput)` pair the optimizer consumes is sourced the same way the per-pod pair is. (For `continuous-vllm-server` the per-pod offered is the trailing-window average, so the deployment sum is too; for windowed `vllm-server` it is the per-window setpoint, and for `queue-analysis`/`blis` the retry-reduced load — the "same-source" consistency holds for every backend, but the window-averaging is specific to the continuous one.) When reporting is partial or empty — some pods coherence-gated (fresh-pod `maxbatchsize` label skew) or none reporting (cold start) — i.e. `numReporting < numReplicas`, `ArrivalRate` falls back to the Load Emulator's offered setpoint label (`inferno.server.load.rpm`), an offered-meaning quantity, instead of the Σ-over-reporting-pods sum (which would under-count by the missing pods' offered share and drive a spurious scale-down). The measured Σ is used only when every replica reports (`numReporting == numReplicas`). The Prometheus completion-rate query is kept only as a last-resort backup when the label is absent. A TODO exists to use a dedicated arrival-rate query when `vllm:request_arrival_total` becomes available.

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
- `ServerCollectorInfo.ReplicaSpecs`: one `config.ServerSpec` per running pod with a valid `/latest` envelope, named `<server>/<podName>`. Per-pod ITL/TTFT come from the envelope's `result`; `ArrivalRate` is set from `effectiveInput.RPS` (the offered load actually run by the server-sim loop) and `Throughput` from `result.Throughput` (the measured completion rate) — these diverge under saturation. (For the `continuous-vllm-server` backend, `effectiveInput.RPS` is the window-averaged offered load measured over the trailing window, not the instantaneous `/solve` setpoint — see the data-model description above.) Saturation handling now lives inside server-sim — the background loop in continuous mode, or the on-demand `/latest` solve in non-continuous mode — governed by `SERVERSIM_SATURATION_POLICY`: for `queue-analysis` / `blis` the policy is `retry-at-lower-load` (the loop retries at progressively lower load — `0.95 → 0.90 → 0.85 × MaxRPS` — until the result is unsaturated before caching it); for `vllm-server` the policy is `pass-through` (the saturated result is cached and propagated so the optimizer can react to overload). The Collector itself does no saturation re-simulation. The Collector additionally applies a causal-coherence check: a pod whose `effectiveInput` concurrency differs from the currently in-force `maxbatchsize` label is skipped (stale result from a prior allocation), so it does not contribute to `ReplicaSpecs` or the deployment-level aggregate. (In non-continuous mode this check passes by construction — the on-demand `/latest` solve uses the in-force label, so `effectiveInput` concurrency always matches.)
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

The full environment-variable reference (defaults and descriptions) is in [`docs/env-vars.md`](docs/env-vars.md). Notable knobs: `INFERNO_CONTROL_PERIOD`, `INFERNO_CONTROL_DYNAMIC`, `TUNER_HOST` (enables the Tuner), `SERVERSIM_CONTINUOUS` / `SERVERSIM_SATURATION_POLICY` (continuous traffic generator; `SERVERSIM_CONTINUOUS=false` is non-continuous on-demand `/latest` for simulator backends), `DEFAULT_MAX_BATCH_SIZE` (concurrency-search switch — see [`docs/concurrency-control.md`](docs/concurrency-control.md)), and `WATCH_NAMESPACE` (shared-cluster scoping).

## Data Files (in `INFERNO_DATA_PATH`)

- `accelerator-data.json` — accelerator hardware specs (static)
- `model-data.json` — LLM model profiles (static). `maxBatchSize` is the **search ceiling** for the optimizer's optimal-concurrency search (`0` ⇒ `DefaultConcurrencyCeiling` = 256), not a fixed batch size. For real-vLLM experiments keep it equal to the pod's `--max-num-seqs` so the searched `M*` can never exceed what the server honors. (The retired `atTokens` field is no longer read.)
- `serviceclass-data.json` — SLA/service class definitions (static)
- `optimizer-data.json` — optimizer parameters (static)
- `capacity-data.json` — current accelerator capacity counts (re-read each cycle)

Sample data is in the `sample-data/` git submodule (`sample-data/large/` has realistic-scale data).

The load emulator phase sequence is configured per-experiment via `manifests/{qa,blis,vllm-cpu}/configmap-load-phases.yaml`, delivered to the pod as the `load-phases-config` ConfigMap mounted at `/etc/loadphases/`.

## Known Behaviours and Operational Notes

Operational gotchas and failure modes — Tuner EKF convergence/skips, evaluator 500s, ConfigMap propagation delay, saturated-pod re-simulation, startup delay, zero-perfParms warm-up, EKF identifiability, and the **continuous traffic generator** (`SERVERSIM_CONTINUOUS`) including the causal-coherence check and the vllm-server control-period invariant — are documented in [`docs/operational-notes.md`](docs/operational-notes.md).

Concurrency control — the optimizer's optimal-concurrency `M*` search, the `maxConcurrency` resolution contract, and how the `DEFAULT_MAX_BATCH_SIZE` switch enables/disables the search (plus the four different fields named `maxBatchSize`) — is documented in [`docs/concurrency-control.md`](docs/concurrency-control.md).

## Visualization

The controller emits one JSON line per completed cycle to `INFERNO_CYCLE_LOG` (default: `inferno-cycles.jsonl` relative to the working directory). Warm-up cycles (tuner not yet converged) do not produce a record.

Each record contains: timestamp, cycle counter, per-server workload (RPM, tokens), per-server attained ITL/TTFT with SLO targets, per-server in-service occupancy (occPerReplica/occTotal, Little's-Law: throughput × in-service time), per-server allocation (replicas, cost, accelerator), total cost, EKF model parameters (alpha/beta/gamma), and cycle phase timings.

The `pkg/monitor/` package handles all logging:
- `record.go` — `CycleRecord` and sub-struct definitions (the JSON schema)
- `builder.go` — `BuildRecord()` assembles a record from controller data; SLO targets are looked up by matching server class → service class → model target
- `monitor.go` — `CycleRecorder` writes records; nil-receiver pattern makes all methods no-ops when logging is disabled

The `dashboard/` directory contains a standalone Python Dash app (`dashboard.py`) that reads the JSONL file and displays auto-refreshing panels: Workload, Performance, Controls, Occupancy, Capacity, and EKF Internals. The internals panel is filtered to only show model/accelerator pairs actively assigned to deployed servers. See `dashboard/requirements.txt` for Python dependencies and README for run instructions.

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

**blis-qwen single-model autoscaling workload** (`scripts/blis/kind-deploy-qwen.sh`, the run19 experiment):

| Deployment | Model | Accelerator | Evaluator | Class |
|---|---|---|---|---|
| `dep-blis-qwen.yaml` | `qwen_2_5_14b` | H100 | blis/trained-physics | Bronze |

A single managed Deployment that uses the blis simulator to stand in for the real vLLM/H100 server from the `vllm-gpu` set, so the autoscaling loop can be exercised without GPUs. Uses `configmap-blis-qwen.yaml` (KV-calibrated) + the `qwen_2_5_14b` entries in `inferno-data/blis/`, and runs the run18 5× ramp profile (`configmap-load-phases-qwen.yaml`). The deploy script sets **NO_TUNER** (seeded perfParms — the EKF converges right back to the static seed, so a slightly-off seed beats the tuner's wild warm-up transient), M\* search ON, on-demand `/latest` (`SERVERSIM_CONTINUOUS=false`), `pass-through` saturation, capacity H100=8, and a 120 s control period; the controller logs to `/tmp/inferno-cycles.jsonl` because the workdir is read-only. `scripts/blis/save-cycle-log.sh` archives the cycle log + all container logs to `experiments/<RUN>/`. See [`experiments/run19/experiment-report-2026-06-26-run19.md`](experiments/run19/experiment-report-2026-06-26-run19.md).

**vllm-gpu workloads** (`scripts/vllm-gpu/oc-deploy.sh`):

| Deployment | Model | Accelerator | Evaluator | Class |
|---|---|---|---|---|
| `dep-vllm-qwen-server.yaml` | `qwen_2_5_14b` (Qwen2.5-14B-Instruct) | H100 | vllm-server | Bronze |
| `dep-vllm-llama-server.yaml` | `llama_3_1_8b` (unsloth/Meta-Llama-3.1-8B-Instruct, non-gated) | H100 | vllm-server | Premium |

Targets a shared OpenShift cluster (not kind). Uses two new namespaces — `inferno-system` (replaces `inferno`) and `inferno-workload` (replaces `infer`) — to avoid colliding with another team's existing setup. Both vLLM Deployments use `vllm/vllm-openai:v0.21.0` with `--max-num-seqs 128` (kept equal to `maxBatchSize` in `model-data.json` — the M\* search ceiling), mount a shared `vllm-models-cache` PVC (RWX 100Gi on `ibm-spectrum-scale-fileset`), and read `HUGGING_FACE_HUB_TOKEN` from `hf-token-secret` (created by the deploy script from the local `HUGGING_FACE_HUB_TOKEN` / `HF_TOKEN` env var, not stored in git). perfParms in `inferno-data/vllm-gpu/model-data.json` are seeded with the converged values from the existing setup so cycle 1 produces a useful allocation. The seeding is an accelerator, not a requirement — the tuner's EKF learns these from observations, and the controller's warm-up gate prevents the optimizer from running on uninitialised values; seeding just skips the warm-up wait. Control period is 120 s (worst-case `/collect` is ~60 s with 2 deployments × 30 s eval windows). The eval config uses `uniform-bounded` token sampling on both inputs and outputs to add per-request size variation without breaking `--max-model-len`. The cluster runs a `gpu-reaper.io` controller that scales down idle GPU pods after 30 min; the experiment's 24-min active phase sequence stays under the threshold, but vLLM Deployments left idle overnight will be reaped (cold start on next deploy, the PVC keeps weights).

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
