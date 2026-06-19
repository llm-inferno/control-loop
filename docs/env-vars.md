# Environment Variables

> Reference for control-loop environment variables. Linked from `CLAUDE.md`.

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
