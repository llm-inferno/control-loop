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
docker build -t inferno-loop . --load

# Set environment variables for local development
. scripts/setparms.sh
```

There are no automated tests in this repository.

## Architecture

The system is a **closed-loop inference optimizer** for Kubernetes. It runs as five cooperating REST microservices:

```
Controller    → Collector → (Prometheus + k8s labels + server-sim /simulate per pod)
              → Tuner     → (EKF-based model parameter refinement: github.com/llm-inferno/model-tuner)
              → Optimizer → (external: github.com/llm-inferno/optimizer)
              → Actuator  → (k8s deployments)
LoadEmulator  → (k8s deployment + pod labels: load metrics)
```

Each managed workload pod runs two sidecars: **server-sim** (port 8080) and **evaluator** (port 8081, `queue-analysis` mode). The Collector calls `server-sim /simulate` on each running pod via the k8s API server proxy to obtain ITL, TTFT, and throughput; ITL/TTFT are aggregated (weighted by per-pod throughput in RPM) into the deployment-level `curAlloc`. Both per-pod and deployment-level `LoadSpec.ArrivalRate` are set to the simulated throughput (goodput) — per-pod from `simResult.Throughput * 60`, deployment-level from `totalRPM` (sum of per-pod goodput) — not the offered arrival rate. If `totalRPM==0` (no running pods or all simulations failed), the deployment-level `ArrivalRate` falls back to the label-based `inferno.server.load.rpm` value to prevent the 0-replica deadlock.

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
- `ServerCollectorInfo.ReplicaSpecs`: one `config.ServerSpec` per running pod whose simulation succeeded, named `<server>/<podName>`, with per-pod ITL/TTFT/load
- Static data is read once at startup from `INFERNO_DATA_PATH`; in dynamic mode (`isDynamicMode=true`) it is re-read each cycle
- `capacity-data.json` is always re-read each cycle (represents current accelerator availability)
- `numReplicas` in `curAlloc` is the count of currently running pods (not `Spec.Replicas`)

**Managed deployments** are discovered by k8s label `inferno.server.managed: "true"`. Required labels: `inferno.server.name`, `inferno.server.model`, `inferno.server.class`, `inferno.server.allocation.accelerator`. The Load Emulator sets traffic rate statistics (RPM, token counts) by writing dynamic load labels to both the deployment and its running pods; nominal load labels (`inferno.server.load.nominal.*`) must be set on each deployment as the mean-reversion target. The Collector reads these labels (or falls back to static labels `inferno.server.load.rpm`, `inferno.server.load.intokens`, `inferno.server.load.outtokens` if Prometheus is unavailable). **The Load Emulator must be running** for pods to have non-zero load labels; without it, per-pod RPM=0 causes the evaluator sidecar's `/simulate` to return HTTP 500, resulting in empty `ReplicaSpecs` (Tuner is then skipped) and all pods contributing zero weight to the aggregated `curAlloc`.

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
| `TUNER_HOST` | unset (Tuner disabled) | Tuner client target address; set to enable tuner integration |
| `TUNER_PORT` | `8081` | Tuner client target port |
| `INFERNO_DATA_PATH` | `./` | Path to JSON data files (must end with `/`) |
| `INFERNO_CONTROL_PERIOD` | `60` | Control loop period in seconds (0 = aperiodic only) |
| `INFERNO_CONTROL_DYNAMIC` | `false` | Re-read static data each cycle |
| `INFERNO_LOAD_INTERVAL` | `60` | Load emulator update interval in seconds |
| `INFERNO_LOAD_ALPHA` | `0.1` | Load emulator noise magnitude relative to nominal |
| `INFERNO_LOAD_THETA` | `0.2` | Load emulator mean-reversion strength |
| `INFERNO_LOAD_SKEW` | `0.3` | Load emulator pod skew factor (0=equal, 1=fully random) |
| `KUBECONFIG` | `$HOME/.kube/config` | Kubernetes config path |

## Data Files (in `INFERNO_DATA_PATH`)

- `accelerator-data.json` — accelerator hardware specs (static)
- `model-data.json` — LLM model profiles (static)
- `serviceclass-data.json` — SLA/service class definitions (static)
- `optimizer-data.json` — optimizer parameters (static)
- `capacity-data.json` — current accelerator capacity counts (re-read each cycle)

Sample data is in the `sample-data/` git submodule (`sample-data/large/` has realistic-scale data).

## Known Behaviours and Operational Notes

**0-replica deadlock and fallback**: When all replicas for a managed deployment are 0 (or pods are starting up with no load labels yet), the Collector gets `totalRPM=0` from server-sim (no pods to simulate). The optimizer then sees 0 arrival rate and allocates 0 replicas, creating a permanent deadlock. **Fix**: the Collector falls back to the label-based arrival rate (`inferno.server.load.rpm`) whenever `totalRPM==0`, regardless of replica count (`pkg/collector/handlers.go`). This covers both the zero-replica case and the newly-started-pod case (labels not yet written by the load emulator).

**Tuner EKF convergence in synthetic environments**: In test environments where server-sim uses the same alpha/beta/gamma parameters it is simulating, the tuner's EKF will converge immediately to the static file values — there is no discrepancy to correct. EKF divergence from static values only occurs with real LLM servers whose actual behaviour differs from the initial parameter estimates.

**Tuner skipped when replicaSpecs empty**: The tuner block is skipped silently (tune: 0ms in timing log) when `len(collectorInfo.ReplicaSpecs) == 0`. This happens when all pod simulations fail (evaluator 500/400). Check evaluator logs if tune time is consistently 0 despite pods running.

**Evaluator 500 for missing model/accelerator**: The evaluator sidecar returns HTTP 500 when the requested model+accelerator combination is not in its `model-data.json` config. Each workload deployment's evaluator must be configured with a `model-data.json` that includes an entry for its `inferno.server.model` label paired with the accelerator assigned by the optimizer. Missing entries cause the pod's simulation to fail, resulting in empty `ReplicaSpecs` and the tuner being skipped.

**ConfigMap propagation delay in dynamic mode**: When `INFERNO_CONTROL_DYNAMIC=true`, static data files are re-read from the mounted ConfigMap each cycle. ConfigMap updates take ~30–60 seconds to propagate to mounted volumes (kubelet sync period). Changes are not reflected until the next cycle after the file is updated on disk.

**Tuner fault tolerance**: If the model-tuner service is unreachable, `POSTTune` fails with a connection error. The controller logs a warning (`tuner /tune warning: ...`) and continues the cycle using `currentModelData` unchanged. The tune timing column shows ~1ms (fast fail). Cycles remain uninterrupted.

## Integration Test Results (k3s / Rancher Desktop)

Tested with `dep1` (`premium-llama-13b`, vllm-001) and `dep2-blis` (`bronze-granite-13b`, vllm-002) workloads:

| Experiment | Observation |
|---|---|
| Tuner convergence | EKF stable at static values each cycle (expected: simulator uses same params it estimates) |
| Load variation → scaling | Raising nominal RPM from 60→300 caused scale-out 1→2 replicas at ~113 RPM; scale-in when load reverted |
| Tuner fault tolerance | Scaling model-tuner to 0 replicas: controller logs `connection refused` warning each cycle, optimize+actuate continue normally |
| Dynamic mode | `INFERNO_CONTROL_DYNAMIC=true`: ConfigMap edit (`saturationPolicy`) picked up within one cycle after kubelet sync; no errors, no restart needed |

Typical cycle timing (2 managed deployments, k3s single-node): `collect: ~220ms  tune: 2–3ms  optimize: ~30ms  actuate: ~10ms  total: ~265ms`
