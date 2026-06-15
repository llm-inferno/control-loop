# Design: `vllm-gpu` experiment scenario (real H100, Qwen2.5-14B + Llama-3.1-8B)

**Date**: 2026-06-07
**Status**: Approved (pending implementation)
**Branch**: `feat/vllm-gpu-experiment`
**Cluster**: OpenShift `https://api.pokprod001.ete14.res.ibm.com:6443`

## Goal

Add a fourth experiment scenario, `vllm-gpu`, that exercises the inferno control loop against **real vLLM servers on H100 GPUs**, using the existing `vllm-server` evaluator backend. The two existing real-vLLM scenarios (`vllm-cpu`) cover correctness on slow CPU-only servers; this scenario validates the loop at GPU latencies and on a shared OpenShift cluster, with a workload shape similar to `qa/run10` (one Premium model, one Bronze model, run10-style 1×→3×→1× ramp).

The PR ships only the manifests/data/scripts/docs for the scenario. **No experiment report.** The first run on the cluster will produce that report in a follow-up PR after parameters are tuned.

## Non-goals

- Multi-cluster parameterization of the deploy script (targets the current `oc whoami` context).
- A templating system for namespace overrides; the single sed substitution in the deploy script is enough for one scenario.
- Changing any existing scenario (`qa`, `blis`, `vllm-cpu`) or any common asset other than adding new namespace YAMLs.
- Validating cold-start EKF on real GPUs — perfParms are seeded with the converged values from the existing setup so cycle 1 produces useful allocation. (Cold-start is a follow-up experiment.)

## Architecture

Reuses the existing four-component control-loop architecture verbatim — same `Controller → Collector → Tuner → Optimizer → Actuator` flow, same `vllm-server` evaluator pairing model documented in `2026-05-29-actuator-vllm-pairing-design.md`, same JSONL cycle log. The scenario is purely a configuration delta: new manifests, new data files, new deploy script, plus two new namespace YAMLs.

### Namespace layout

The shared cluster already has `infer` and `inferno` namespaces in use by another team. To avoid collision and shared mutable state:

| New namespace | Replaces (in this scenario only) | Hosts |
|---|---|---|
| `inferno-system` | `inferno` | inferno pod (5 containers), load-emulator, tuner ConfigMap, inferno-static-data + inferno-dynamic-data ConfigMaps |
| `inferno-workload` | `infer` | Managed Deployments (server-sim + evaluator), paired vLLM Deployments, eval ConfigMap, RBAC, PVC, HF secret |

The existing scenarios continue to use `inferno` and `infer` and are not affected.

### File-level deliverables

**New shared assets (`manifests/common/`)**:

```
ns-inferno-system.yaml          NEW   namespace only
ns-inferno-workload.yaml        NEW   namespace only
```

The existing `ns-inferno.yaml` and `ns-infer.yaml` are unchanged. The existing `deploy-loop.yaml` and `configmap-tuner.yaml` are reused as-is; the deploy script applies them through a sed substitution (see Deploy script section).

**New scenario assets (`manifests/vllm-gpu/`)**:

```
pvc-models-cache.yaml           NEW   100Gi RWX, ibm-spectrum-scale-fileset
deployment-vllm-qwen.yaml       NEW   Qwen2.5-14B-Instruct on H100
deployment-vllm-llama.yaml      NEW   unsloth/Meta-Llama-3.1-8B-Instruct on H100
dep-vllm-qwen-server.yaml       NEW   managed Deployment, Bronze, paired with vllm-qwen
dep-vllm-llama-server.yaml      NEW   managed Deployment, Premium, paired with vllm-llama
configmap-vllm-eval.yaml        NEW   both H100 model entries, uniform-bounded sampling
configmap-load-phases.yaml      NEW   five-phase 1×→3×→1× ramp, 6 min each
load-emulator.yaml              NEW   same pattern as vllm-cpu's
rbac-vllm-eval.yaml             NEW   ServiceAccount + Role + RoleBinding for evaluator
```

**New scenario data (`inferno-data/vllm-gpu/`)**:

```
accelerator-data.json           NEW   H100 only, cost=1.0
model-data.json                 NEW   qwen_2_5_14b + llama_3_1_8b, perfParms seeded
serviceclass-data.json          NEW   Premium llama, Bronze qwen
optimizer-data.json             NEW   saturationPolicy: None
capacity-data.json              NEW   H100 count = 6
```

**New deploy script (`scripts/vllm-gpu/`)**:

```
oc-deploy.sh                    NEW   OpenShift deploy (no kind load), HF secret copy, sed namespace rewrite
```

**Documentation update**:

- `CLAUDE.md` — adds a "vllm-gpu workloads" subsection under "Workloads", a row in the workloads table for both servers, and a one-paragraph operational note (HF secret copy, gpu-reaper idle threshold).

## Component design

### Namespaces

`ns-inferno-system.yaml` and `ns-inferno-workload.yaml` are minimal — just `kind: Namespace` with the name. No labels or annotations needed; OpenShift's default Pod Security Admission level (`restricted-v2`) is acceptable since all our pods set `runAsNonRoot: true` and drop ALL capabilities.

### vLLM Deployments

Two Deployments, each running one vLLM container, mirroring the structure of the existing `infer/vllm-qwen-14b-gpu` and `infer/vllm-llama-gpu` (which are known to work on this cluster):

**Common fields:**
- Image: `vllm/vllm-openai:v0.21.0` (pinned, matches existing).
- Strategy: `Recreate` (single GPU per replica, can't run two side-by-side during update).
- `nvidia.com/gpu: "1"` request and limit.
- `securityContext`: `runAsNonRoot: true`, drops ALL capabilities, `seccompProfile: RuntimeDefault`.
- Tolerations: `nvidia.com/gpu: NoSchedule`.
- Node affinity: `NotIn` for the 8 reserved node names from the existing setup (preserves cluster etiquette).
- `--max-num-seqs 32` for both (matches `maxBatchSize` declared in `inferno-data/vllm-gpu/model-data.json`).
- HF token via `HUGGING_FACE_HUB_TOKEN` env, `secretKeyRef` to `hf-token-secret/token`.
- Models cache mounted from PVC `vllm-models-cache` at `/models-cache`.
- `/dev/shm` as `emptyDir.Memory` 4Gi.
- Probes: liveness + readiness + startup (startup `failureThreshold` × `periodSeconds` allows for 15-min model load on first deploy).
- Prometheus annotations: `scrape: true`, `port: 8000`, `path: /metrics`.

**Per-deployment specifics:**

| Deployment | Image arg | `--max-model-len` | `--gpu-memory-utilization` | `requests` | `limits` |
|---|---|---|---|---|---|
| `vllm-qwen-14b-gpu` | `vllm serve Qwen/Qwen2.5-14B-Instruct --served-model-name qwen ... --max-model-len 4096 --max-num-seqs 32 ...` | 4096 | 0.90 | cpu 6 / mem 48Gi / gpu 1 | cpu 8 / mem 64Gi / gpu 1 |
| `vllm-llama-gpu` | `vllm serve unsloth/Meta-Llama-3.1-8B-Instruct --served-model-name llama ... --max-model-len 8192 --max-num-seqs 32 ...` | 8192 | 0.90 | cpu 6 / mem 32Gi / gpu 1 | cpu 8 / mem 48Gi / gpu 1 |

Both use `--no-enable-prefix-caching --dtype bfloat16 --trust-remote-code --download-dir /models-cache --port 8000 --generation-config vllm`.

The Llama deployment uses the **`unsloth/Meta-Llama-3.1-8B-Instruct` non-gated fork**. Original Meta upstream is gated; `unsloth` is a verbatim fork that doesn't require HF gate approval. The HF token is still wired in (`HUGGING_FACE_HUB_TOKEN` env) so a future swap to a gated model only requires an args change.

Initial replicas: 1 each. The optimizer scales out from there.

Pairing labels (read by the actuator's vLLM-pairing reconciler):

- vLLM Deployment pod template carries `inferno.vllm.model: qwen` (or `llama`) and `inferno.vllm.accelerator: H100`. The model label is **required** for pairing (see `2026-05-29-actuator-vllm-pairing-design.md`).

### Managed Deployments (server-sim + evaluator)

Two Deployments, one per model, each with the standard two-sidecar pattern:

**Per-deployment labels** (drive the controller and the pairing reconciler):

| Server | model label | class | accelerator | `vllm-deployment` | `vllm-namespace` | nominal RPM | inTok | outTok | `maxbatchsize` |
|---|---|---|---|---|---|---|---|---|---|
| `vllm-qwen-14b-server` | `qwen_2_5_14b` | Bronze | H100 | `vllm-qwen-14b-gpu` | `inferno-workload` | 60 | 1024 | 512 | 32 |
| `vllm-llama-server` | `llama_3_1_8b` | Premium | H100 | `vllm-llama-gpu` | `inferno-workload` | 90 | 768 | 2048 | 32 |

The two deployments are deliberately asymmetric in token shape: Qwen is **prefill-heavy** (in:out = 2:1) on the larger 14B model, while Llama is **decode-heavy** (in:out ≈ 0.375) on the smaller 8B model. This exercises the optimizer and EKF across distinct workload regimes within a single experiment.

Both also carry: `inferno.server.managed: "true"`, `inferno.server.evaluator: "vllm-server"`, `inferno.server.allocation.maxqueuesize: "64"`, and the matching `inferno.server.load.nominal.*` triplet.

**Container layout** (identical to existing `infer/vllm-*-server` pattern):

- `server-sim` (`quay.io/atantawi/inferno-server-sim:latest`, port 8080, `EVALUATOR_URL=http://localhost:8081`, `NOISE_ENABLED=false`).
- `evaluator` (`quay.io/atantawi/inferno-evaluator:latest`, args `["vllm-server"]`, port 8081). Mounts the eval ConfigMap and a downward-API volume for the `pair-id` and `vllm-deployment` labels.
- `serviceAccountName: vllm-server-evaluator` (RBAC defined below).
- `VLLM_NAMESPACE: inferno-workload` env on the evaluator (the namespace where the paired vLLM lives).

Initial replicas: 1 each.

### Eval ConfigMap

`vllm-server-eval-config` (in `inferno-workload`):

```json
{
  "configs": [
    {
      "accelerator": "H100",
      "model": "qwen_2_5_14b",
      "vllmServedModelName": "qwen",
      "vllmPort": 8000,
      "warmupSec": 0,
      "minWindowSec": 0,
      "maxWindowSec": 30,
      "targetSamples": 0,
      "minSamples": 3,
      "ignoreEOS": true,
      "queueTimeMetric": "vllm:request_queue_time_seconds",
      "inputTokenDistribution":  "uniform-bounded",
      "outputTokenDistribution": "uniform-bounded"
    },
    {
      "accelerator": "H100",
      "model": "llama_3_1_8b",
      "vllmServedModelName": "llama",
      "vllmPort": 8000,
      "warmupSec": 0,
      "minWindowSec": 0,
      "maxWindowSec": 30,
      "targetSamples": 0,
      "minSamples": 3,
      "ignoreEOS": true,
      "queueTimeMetric": "vllm:request_queue_time_seconds",
      "inputTokenDistribution":  "uniform-bounded",
      "outputTokenDistribution": "uniform-bounded"
    }
  ]
}
```

Notes:

- `passiveMode` is **not** included. Inspecting `server-sim/vllm-server-evaluator/config.go` shows the field is silently ignored (no Go field maps to it); the existing setup's `passiveMode: true` is a no-op. Including it would document a feature that does not exist.
- `uniform-bounded` produces token counts in `[avg/2, (3·avg+1)/2]`, preserving the mean while exercising batch-step variability:

| Field | avg | sampled range |
|---|---|---|
| Qwen inTok | 1024 | [512, 1537] |
| Qwen outTok | 512 | [256, 769] |
| Llama inTok | 768 | [384, 1153] |
| Llama outTok | 2048 | [1024, 3073] |

Worst-case in+out per request stays strictly inside vLLM's `--max-model-len`: Qwen 1537+769=2306 < 4096; Llama 1153+3073=4226 < 8192.

- `maxWindowSec=30` is a starting point. With 2 deployments serialized in the controller's `/collect` loop, this gives ~60 s of evaluator wall-clock per cycle; control period is set to 120 s (see Control-loop tuning).

### Load emulator

Same pattern as `manifests/vllm-cpu/load-emulator.yaml`:

- Pod in `inferno-system`, `inferno` ServiceAccount.
- Image: `quay.io/atantawi/inferno-loop:latest`, command `loademulator`.
- Mounts `load-phases-config` ConfigMap at `/etc/loadphases/`.
- Env: `INFERNO_LOAD_INTERVAL=30` (loademulator update interval; finer than control period so the controller sees ramps cleanly), `INFERNO_LOAD_THETA=0.9`, `INFERNO_LOAD_ALPHA=0.1`, `INFERNO_LOAD_SKEW=0.0`, `INFERNO_LOAD_PHASES=/etc/loadphases/phases.yaml`.

### Load phases

`configmap-load-phases.yaml`, namespaced to `inferno-system`:

```yaml
phases:
  - duration: 6m   ratio: 1.0    # hold 1×
  - duration: 6m   ratio: 3.0    # linear ramp 1×→3×
  - duration: 6m   ratio: 1.0    # hold 3×
  - duration: 6m   ratio: 0.333  # linear ramp 3×→1×
  - duration: 0s                 # hold 1× forever
```

Total experiment ≈ 24 min of dynamic phase sequence + indefinite tail at 1×. Ratios are chained-multiplicative (per `2026-04-10-loademulator-phases-design.md` and existing scenario configs). Hold phases use `ratio: 1.0`; ramp phases carry the multiplier change. Stays under the 30-min `gpu-reaper` idle threshold.

Resulting per-server RPM:

| Server | At 1× | At peak 3× |
|---|---|---|
| `vllm-qwen-14b` (Bronze) | 60 | 180 |
| `vllm-llama` (Premium) | 90 | 270 |

### Inferno data files

**`accelerator-data.json`**:

```json
{ "accelerators": [ { "name": "H100", "type": "H100", "multiplicity": 1, "cost": 1.0 } ] }
```

**`model-data.json`** (perfParms seeded from existing converged values; same values the other team's setup uses today):

```json
{
  "models": [
    { "name": "qwen_2_5_14b", "acc": "H100", "accCount": 1, "maxBatchSize": 32, "atTokens": 1024,
      "perfParms": { "alpha": 10.645377, "beta": 0.041760195, "gamma": 0.000057705090 } },
    { "name": "llama_3_1_8b", "acc": "H100", "accCount": 1, "maxBatchSize": 32, "atTokens": 512,
      "perfParms": { "alpha": 6.49, "beta": 0.0219, "gamma": 0.0000496 } }
  ]
}
```

`maxBatchSize` is **uniform across models** (32), matching `--max-num-seqs 32` on both vLLM Deployments and the `inferno.server.allocation.maxbatchsize: "32"` labels on both managed Deployments.

**`serviceclass-data.json`** (copies the existing setup's targets):

```json
{
  "serviceClasses": [
    { "name": "Premium", "priority": 1,
      "modelTargets": [ { "model": "llama_3_1_8b", "slo-itl": 9.5,  "slo-ttft": 50  } ] },
    { "name": "Bronze",  "priority": 2,
      "modelTargets": [ { "model": "qwen_2_5_14b", "slo-itl": 25.0, "slo-ttft": 100 } ] }
  ]
}
```

**`optimizer-data.json`**:

```json
{ "optimizer": { "unlimited": true, "heterogeneous": false, "milpsolver": false,
                 "useCplex": false, "delayedBestEffort": false, "saturationPolicy": "None" } }
```

**`capacity-data.json`**: `H100` count = 6 (matches existing setup; cluster has more but they're shared).

### RBAC

`rbac-vllm-eval.yaml` (in `inferno-workload`): the standard ServiceAccount + Role + RoleBinding from `manifests/vllm-cpu/rbac-vllm-eval.yaml`, scoped to the new namespace. Verbs: `get`, `list`, `watch` on pods. Used by the evaluator to discover its paired vLLM pod IP.

The cluster-wide RBAC for the inferno ServiceAccount (ClusterRole `inferno`) lives in `manifests/common/deploy-loop.yaml` and is reused — the script's sed substitution rewrites the ClusterRoleBinding subject namespace to `inferno-system`.

### PVC

`pvc-models-cache.yaml`:

- Namespace: `inferno-workload`.
- AccessMode: `ReadWriteMany`.
- Storage: 100Gi.
- StorageClassName: `ibm-spectrum-scale-fileset` (cluster default; supports RWX).

Fresh PVC, isolated from the other team's `infer/vllm-models-cache`. First deploy will trigger a ~44 GB model download (Qwen 14B ~28 GB bf16 + Llama 3.1 8B ~16 GB bf16). Subsequent deploys reuse the cached weights.

### HF token secret

There is no static manifest for the Secret. The deploy script creates it directly from a local environment variable, so the token never lives on disk or in git. The Secret contract is fixed by the vLLM Deployments' `secretKeyRef`: name `hf-token-secret`, namespace `inferno-workload`, key `token`.

```bash
HF_TOKEN_VALUE="${HUGGING_FACE_HUB_TOKEN:-${HF_TOKEN:-}}"
if [[ -z "$HF_TOKEN_VALUE" ]]; then
  echo "ERROR: set HUGGING_FACE_HUB_TOKEN (or HF_TOKEN) before running." >&2
  exit 1
fi
oc create secret generic hf-token-secret -n "$WORK_NS" \
  --from-literal=token="$HF_TOKEN_VALUE" \
  --dry-run=client -o yaml | oc apply -f -
```

Rationale for this approach over copying from `infer/hf-token-secret` (the original design): the cross-namespace copy depended on another team's secret existing under a stable name, plus `get secrets` on `infer/` for the script-runner. The env-var approach removes both couplings — the runner just needs their own HF token in their shell.

### Deploy script

`scripts/vllm-gpu/oc-deploy.sh`:

1. Pre-flight checks: `oc whoami` succeeds, target context is the OpenShift cluster.
2. Apply namespaces (`oc apply -f manifests/common/ns-inferno-system.yaml`, `ns-inferno-workload.yaml`).
3. Apply RBAC (`manifests/vllm-gpu/rbac-vllm-eval.yaml`).
4. Apply PVC (`manifests/vllm-gpu/pvc-models-cache.yaml`).
5. Copy HF token secret from `infer/` to `inferno-workload/` (via the sed pipeline above).
6. Apply the eval ConfigMap.
7. Create `inferno-static-data` and `inferno-dynamic-data` ConfigMaps in `inferno-system` from `inferno-data/vllm-gpu/`.
8. Apply the tuner ConfigMap (`manifests/common/configmap-tuner.yaml` with namespace rewritten via sed).
9. Apply the inferno deployment (`manifests/common/deploy-loop.yaml` with namespace rewritten to `inferno-system` via sed for both the Deployment metadata and the ClusterRoleBinding subject).
10. Override controller env: `INFERNO_CONTROL_PERIOD=120`, `INFERNO_WARM_UP_TIMEOUT=10`, `DEFAULT_MAX_BATCH_SIZE=32`. Override collector env: `INFERNO_SIMULATE_TIMEOUT_SEC=60`. Wait for inferno rollout.
11. Apply the two vLLM Deployments. `oc wait --for=condition=available --timeout=1800s` (allow 30 min for first-run weight download).
12. Apply the two managed Deployments. `oc rollout status --timeout=300s` for each.
13. Apply the load-phases ConfigMap.
14. Delete any pre-existing `load-emulator` pod, then apply the new `load-emulator.yaml`.
15. Print the standard "watch logs with..." footer.

The script is idempotent (all `apply` operations) and safe to re-run.

### Control-loop tuning (env overrides applied by deploy script)

| Env | Value | Reason |
|---|---|---|
| `INFERNO_CONTROL_PERIOD` | `120` | 2× the worst-case `/collect` time (2 deployments × 30 s eval window). Leaves ~60 s of margin. |
| `INFERNO_WARM_UP_TIMEOUT` | `10` | perfParms are seeded; warm-up is expected to be fast. |
| `INFERNO_SIMULATE_TIMEOUT_SEC` | `60` | > 2× `maxWindowSec=30`. |
| `DEFAULT_MAX_BATCH_SIZE` | `32` | Matches per-server label and per-model `maxBatchSize`. |
| `INFERNO_STARTUP_DELAY` | `0` | (default) — vLLM is ready by the time the managed Deployment is. |

## Risks and operational call-outs

1. **Cross-team friction on the shared cluster.** Mitigated by separate namespaces, separate PVC, separate HF secret, separate image tags, and node affinity excluding the 8 reserved nodes.
2. **`gpu-reaper.io` idle scale-down (30 min).** Our 24-min experiment stays under the threshold while phases are active, but the workload will not survive overnight idle. Cold start on next deploy is acceptable — first-run download is amortized by the PVC; subsequent re-spins are limited by AOT compile + load weights into GPU memory.
3. **First-run weight download (~44 GB).** Realistic 15–30 min. The deploy script's `oc wait --timeout=1800s` covers this.
4. **KV-cache pressure at `--max-num-seqs 32` × heavy tokens.** Back-of-envelope says we have headroom (~10 GB needed of ~56 GB available on H100 after weights). If vLLM logs `KV cache` / `preempted` / `out of memory`, ladder: (a) lower `--max-num-seqs`, (b) lower `--max-model-len`, (c) shrink per-request token range.
5. **EKF noise during overload.** The Collector skips re-simulation on saturation for `vllm-server` (per CLAUDE.md), so degenerate TTFT/ITL are passed through. With seeded perfParms and `saturationPolicy: None`, the optimizer reacts (scales out) on overload; the EKF gets noisy observations during overload but recovers when load drops. If divergence is observed, switch `INFERNO_WARM_UP_TIMEOUT=0` and rely on warm-up gating.
6. **Control period at the edge.** With 2 deployments × `maxWindowSec=30 s`, collect is ~60 s. 120 s leaves ~60 s margin. Replicas inside a deployment run in parallel so wall-clock doesn't grow with replica count; adding a third managed Deployment would require re-checking.
7. **`oc cp` HF secret copy.** Requires read access to `infer/`. If the user lacks it, the script fails fast with a clear message and exits.

## Validation (pre-PR-merge)

Performed on the cluster on the working branch before requesting review:

- [ ] `scripts/vllm-gpu/oc-deploy.sh` completes without error (exit 0).
- [ ] `oc get pvc -n inferno-workload` shows `vllm-models-cache` Bound.
- [ ] `oc get deploy -n inferno-workload` shows both `vllm-*-gpu` and both `vllm-*-server` Available, `READY` matches replicas.
- [ ] Evaluator log contains `pairing resolved` for both managed Deployments: `oc logs -n inferno-workload deploy/vllm-qwen-14b-server -c evaluator | grep "pairing resolved"`.
- [ ] At least one controller cycle completes: `oc logs -n inferno-system deploy/inferno -c controller | grep "cycle"`.
- [ ] At least one record written to `inferno-cycles.jsonl`: `oc exec -n inferno-system deploy/inferno -c controller -- wc -l /inferno-cycles.jsonl`.

If any check fails, fix on the branch with additional commits before merging.

## Things deliberately not in this PR

- Experiment report — separate PR after first successful run and parameter tuning.
- A way to override load-phase ratios from the CLI.
- Multi-cluster `oc-deploy.sh` parameterization.
- A templating system for namespace overrides (sed substitution suffices for one scenario).
- Cold-start EKF validation on real GPUs (perfParms are seeded; cold-start is a follow-up).

## References

- `2026-05-29-actuator-vllm-pairing-design.md` — actuator pairing reconciler contract (four invariants).
- `2026-04-10-loademulator-phases-design.md` — load-emulator phase semantics (chained-multiplicative ratios, linear ramps).
- `2026-04-22-slo-driven-autoscaler-design.md` — SLO-driven autoscaler design (background context on how the loop reasons about SLOs).
- `manifests/vllm-cpu/` — closest sibling scenario; this design is a delta on top of it.
- Existing cluster state at `infer/` and `inferno/` namespaces — informed image tags, vLLM args, PVC sizing, eval config shape, and SLO targets.
