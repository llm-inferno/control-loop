# Concurrency Control (optimal-concurrency search)

> Reference for how `maxConcurrency` / `maxBatchSize` resolve and how the optimizer's
> optimal-concurrency search is enabled or disabled. Linked from `CLAUDE.md`.
> Env-var details are in [`env-vars.md`](env-vars.md).

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
