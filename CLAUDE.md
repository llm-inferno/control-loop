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

The system is a **closed-loop inference optimizer** for Kubernetes. It runs as four cooperating REST microservices:

```
Controller    → Collector → (Prometheus + k8s labels + server-sim /simulate per pod)
              → Optimizer (external: github.com/llm-inferno/optimizer)
              → Actuator  → (k8s deployments)
LoadEmulator  → (k8s deployment + pod labels: load metrics)
```

Each managed workload pod runs two sidecars: **server-sim** (port 8080) and **evaluator** (port 8081, `queue-analysis` mode). The Collector calls `server-sim /simulate` on each running pod via the k8s API server proxy to obtain ITL, TTFT, and throughput; ITL/TTFT are aggregated (weighted by per-pod throughput in RPM) into the deployment-level `curAlloc`. Both per-pod and deployment-level `LoadSpec.ArrivalRate` are set to the simulated throughput (goodput) — per-pod from `simResult.Throughput * 60`, deployment-level from `totalRPM` (sum of per-pod goodput) — not the offered arrival rate.

Data/config types (`config.SystemData`, `config.AllocationData`, etc.) and `utils.FromDataToSpec` come from `github.com/llm-inferno/optimizer-light/pkg/config` and `…/pkg/utils`. The `optimizer` module depends on `optimizer-light` and re-exports its REST server; the control-loop imports `optimizer-light` directly.

**Control flow** (in `pkg/controller/controller.go:Optimize()`):
1. Controller calls `GET /collect` on the Collector to read current server state from k8s labels, Prometheus, and server-sim simulations
2. Controller calls `POST /optimizeOne` on the Optimizer with full `SystemData`
3. Controller calls `POST /update` on the Actuator with allocation decisions + k8s references
4. Actuator scales k8s deployment replicas to match the optimizer's allocation

**Data model** — `pkg/controller/`:
- `State.SystemData` (`config.SystemData` from `optimizer-light/pkg/config`): holds static files (accelerators, models, service classes, optimizer params) and dynamic server data
- `State.ServerMap`: maps server names to k8s `{uid, name, namespace}` for the Actuator to resolve deployments
- `ServerCollectorInfo.Spec`: one `config.ServerSpec` per managed deployment (aggregated ITL/TTFT/load)
- `ServerCollectorInfo.ReplicaSpecs`: one `config.ServerSpec` per running pod whose simulation succeeded, named `<server>/<podName>`, with per-pod ITL/TTFT/load
- Static data is read once at startup from `INFERNO_DATA_PATH`; in dynamic mode (`isDynamicMode=true`) it is re-read each cycle
- `capacity-data.json` is always re-read each cycle (represents current accelerator availability)
- `numReplicas` in `curAlloc` is the count of currently running pods (not `Spec.Replicas`)

**Managed deployments** are discovered by k8s label `inferno.server.managed: "true"`. Required labels: `inferno.server.name`, `inferno.server.model`, `inferno.server.class`, `inferno.server.allocation.accelerator`. The Load Emulator sets traffic rate statistics (RPM, token counts) by writing dynamic load labels to both the deployment and its running pods; nominal load labels (`inferno.server.load.nominal.*`) must be set on each deployment as the mean-reversion target. The Collector reads these labels (or falls back to static labels `inferno.server.load.rpm`, `inferno.server.load.intokens`, `inferno.server.load.outtokens` if Prometheus is unavailable).

**Controller** also exposes `GET /invoke` for on-demand (aperiodic) control cycles. Both periodic and aperiodic modes run simultaneously; the mutex in `Optimize()` serializes concurrent calls.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `CONTROLLER_HOST/PORT` | `localhost:3300` | Controller REST address |
| `COLLECTOR_HOST/PORT` | `localhost:3301` | Collector REST address |
| `INFERNO_HOST/PORT` | `localhost:3302` | Optimizer REST address |
| `ACTUATOR_HOST/PORT` | `localhost:3303` | Actuator REST address |
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
