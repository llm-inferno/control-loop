# Actuator vLLM-Pairing Extension — Design

**Status:** Draft for review
**Date:** 2026-05-29
**Issue:** [llm-inferno/control-loop#15](https://github.com/llm-inferno/inferno/issues/15)
**Companion spec:** [`server-sim/docs/superpowers/specs/2026-05-28-vllm-server-evaluator-design.md`](https://github.com/llm-inferno/server-sim/blob/main/docs/superpowers/specs/2026-05-28-vllm-server-evaluator-design.md)

## 1. Motivation

The sister repo [`server-sim`](https://github.com/llm-inferno/server-sim) is adding a new evaluator backend, `vllm-server`, which drives a real vLLM Deployment with synthetic Poisson traffic and reports measured TTFT, ITL, response time, and queue time via the existing `POST /solve(ProblemData) → AnalysisData` contract.

Unlike the existing `dummy`, `queue-analysis`, and `blis` evaluators, `vllm-server` is **not** a pure function of `ProblemData`: it requires a real vLLM pod paired 1:1 with the managed pod hosting the evaluator sidecar. Per §6 of the server-sim design doc, pairing is established by **this** repo's Actuator via labels and is verified by the evaluator at `/solve` time.

This spec covers the four-invariant Actuator extension required to satisfy that contract.

## 2. Scope

### In scope

- A new background **pairing reconciler** inside the existing Actuator process.
- New label keys for evaluator type, vLLM Deployment reference, vLLM namespace, and pair-id.
- RBAC additions: `pods` `patch` verb (cluster-scoped, alongside existing `get`/`list`/`watch`).
- Two new env vars: `INFERNO_PAIRING_TICK_SEC`, `INFERNO_PAIRING_LOG_LEVEL`.
- Unit tests for the pairing logic core via a fake K8s client.

### Out of scope

- Implementing the `vllm-server` evaluator (separate work in `server-sim`).
- vLLM Deployment templating, operator, or CRD — the workload author still authors the vLLM Deployment YAML.
- K8s informers / watch-driven reconciliation — a periodic tick is sufficient for v1.
- Changes to `queue-analysis` or `blis` evaluator paths.
- A kind-based end-to-end smoke test (deferred until `server-sim`'s `vllm-server-evaluator` is buildable).

## 3. Architecture

The Actuator gains a second responsibility alongside its existing `POST /update` REST handler: a background **pairing reconciler** running every `INFERNO_PAIRING_TICK_SEC` (default `5`). Both share one `*kubernetes.Clientset`.

```
                ┌──────────────────────────── Actuator ────────────────────────────┐
                │                                                                  │
Controller─/update►│  REST handler (existing)                                      │
                │     ├── patches managed Deployment replicas/labels per optimizer │
                │     └── (unchanged for non-vllm-server evaluators)               │
                │                                                                  │
                │  Pairing reconciler (NEW; goroutine, ticker)                     │
                │     for each managed Dep with evaluator=vllm-server:             │
                │        1. mirror vLLM Deployment.spec.replicas ← managed         │
                │        2. list Ready pods on each side                           │
                │        3. prune stale pair-id labels                             │
                │        4. greedy-pair unpaired pods with fresh UUIDs             │
                │                                                                  │
                └──────────────────────────────────────────────────────────────────┘
```

The reconciler is **self-healing and idempotent**: every tick is a full state computation; nothing is remembered between ticks except the cached kube client. There is no informer, no workqueue, no event handler — just a ticker.

### Decisions and rationale

| Decision | Rationale |
|---|---|
| Reconciler lives in Actuator binary, not a sixth process | One binary, one RBAC, one log stream. The work is small; a separate process would be over-engineered. |
| Periodic tick (default 5 s), not informer-driven | Simpler code, naturally idempotent. Reaction latency of a few seconds is negligible against vLLM startup time (tens of seconds). |
| Managed Deployment is the **only** source of truth for replica count | If anyone manually edits either side, the next tick corrects it. No drift between optimizer cycles. |
| Greedy pair-unpaired-with-unpaired strategy | Existing healthy pairings are never disturbed. Idempotent at steady state. Avoids the label churn of a deterministic re-shuffle. |
| Same-binary kube client | Reuses the existing `ctrl.GetKubeClient()` already wired up by `actuator.go`. |

## 4. Components

New files in `pkg/actuator/`:

| File | Purpose |
|---|---|
| `pairing.go` | Reconciler entry point. Owns the ticker goroutine, top-level error logging, and the per-Deployment loop. |
| `pairing_logic.go` | Pure functions: given `(managedPods, vllmPods)` snapshots, return label-patch decisions (`prunes []podRef`, `bindings []pairing`). No K8s I/O. The unit-tested core. |
| `pairing_kube.go` | K8s I/O wrappers: list managed Deployments by selector, list pods owned by a Deployment (via ReplicaSet chain), patch a pod label, patch Deployment replicas. Thin layer over `clientset`. |
| `pairing_logic_test.go` | Table-driven tests using `client-go/kubernetes/fake` for the patch-application path. |

Modified:

| File | Change |
|---|---|
| `actuator.go` | `NewActuator()` reads `INFERNO_PAIRING_TICK_SEC` and starts the reconciler goroutine when the value is positive. |
| `cmd/actuator/main.go` | No change — `Run()` blocks as today; goroutine lives for the binary's lifetime. |
| `pkg/controller/defaults.go` | Four new label key constants: `KeyEvaluator`, `KeyVLLMDeployment`, `KeyVLLMNamespace`, `KeyPairID`. |
| `CLAUDE.md` | Document new env vars and labels. |
| `yamls/deploy/clusterrole.yaml` (or equivalent) | Add `pods` `patch` verb. |

## 5. Labels (the contract)

Set on the **managed Deployment** by the workload author:

| Label | Required when | Value |
|---|---|---|
| `inferno.server.evaluator` | always (explicit opt-in) | `vllm-server`, `queue-analysis`, or `blis` |
| `inferno.server.vllm-deployment` | evaluator = `vllm-server` | name of the paired vLLM Deployment |
| `inferno.server.vllm-namespace` | optional | namespace of the vLLM Deployment; defaults to managed Deployment's namespace |

Written by the **Actuator** at runtime:

| Label | Where | Value |
|---|---|---|
| `inferno.server.pair-id` | one managed pod **and** one vLLM pod | matching UUID per pair |

The vLLM Deployment itself needs no inferno labels; only its pods are labelled. The reconciler finds the Deployment by name + namespace.

## 6. Reconcile-tick algorithm (one Deployment)

```
managed = GetDeployment(managedNs, managedName)
if managed.Labels["inferno.server.evaluator"] != "vllm-server":
    return                                              # legacy path; not our concern

vName = managed.Labels["inferno.server.vllm-deployment"]
vNs   = managed.Labels["inferno.server.vllm-namespace"] or managed.Namespace
vllm  = GetDeployment(vNs, vName)
if vllm == nil:
    log.Warn("vllm Deployment not found"); return      # graceful — evaluator returns 503

# (1) Replica lockstep — invariant 1, ordering invariant 4
if *vllm.Spec.Replicas != *managed.Spec.Replicas:
    PatchReplicas(vllm, *managed.Spec.Replicas)
    return                                              # let next tick observe Ready transitions

# (2) Snapshot Ready pods on each side
mPods = ListReadyPods(managedNs, ownedBy: managed)
vPods = ListReadyPods(vNs,       ownedBy: vllm)

# (3) Compute prune + binding decisions (pure)
prunes, bindings = ComputePairingPatches(mPods, vPods)

# (4) Apply — prunes first, then bindings
for p in prunes:    PatchPodRemoveLabel(p, "inferno.server.pair-id")
for b in bindings:  PatchPodSetLabel(b.managedPod, "inferno.server.pair-id", b.uuid)
                    PatchPodSetLabel(b.vllmPod,    "inferno.server.pair-id", b.uuid)
```

`ComputePairingPatches` — the unit-tested core — implements the greedy strategy:

1. Build a map `pairID → {managedPod?, vllmPod?}` from each pod's `pair-id` label.
2. For any entry where either side is missing, NotReady, or has no peer (orphaned UUID), add the pod(s) carrying the id to `prunes`.
3. For each managed pod still unpaired (Ready, no `pair-id`), pair with any unpaired-Ready vLLM pod; emit a `binding` carrying a fresh UUID.
4. Excess pods on either side stay unpaired — that's the steady-state tail of an in-progress scale.

**Idempotency.** A tick where every Ready pod already has a valid mutual pair-id produces zero patches.

**Pod ownership** is verified via `OwnerReferences → ReplicaSet → Deployment`, the same chain the Collector already uses to discover pods owned by a Deployment.

This algorithm naturally implements the four invariants from §6 of the server-sim design:

| Invariant | Implementation |
|---|---|
| 1. Replica lockstep | Step (1) before any pod work |
| 2. Pairing labels (one-to-one, unique UUID) | Greedy step in `ComputePairingPatches` |
| 3. Reconciliation on pod replacement | Stale-prune step → fresh re-pair on next tick |
| 4. vLLM scaled first; labels only after Ready | Step (1) returns early; step (2) filters by Ready |

## 7. Failure modes

| Condition | Behavior |
|---|---|
| Managed Deployment has `evaluator=vllm-server` but no `vllm-deployment` label | Log warning at `info` level (rate-limited per Deployment per minute); skip. Evaluator returns 503. |
| `vllm-deployment` label set but Deployment doesn't exist | Same — log + skip. Self-heals when the Deployment is created. |
| vLLM Deployment exists but `spec.replicas == nil` | Treat as 0; patch to managed's replicas. |
| Managed Deployment scaled to 0 | Patch vLLM to 0; no pods to label. |
| Pod becomes NotReady mid-cycle (between list and patch) | Patch may target a pod that's now NotReady; the patch still succeeds, but next tick detects peer-NotReady and prunes. Self-corrects. |
| Pod deleted between list and patch | Patch returns 404; reconciler logs at debug, moves on. Next tick re-pairs. |
| Two ticks could overlap if a tick exceeds the interval | Reconciler runs ticks serially in one goroutine — no overlap by construction. |
| K8s API transient error (5xx on List/Get) | Skip this Deployment this tick; log warning. Next tick retries. No state to corrupt. |
| Manual edit removes `pair-id` from a pod | Treated as unpaired-Ready; partner's label is pruned next tick (orphan UUID), both get a fresh UUID. ~1 tick of evaluator 503. |
| Manual scale of vLLM Deployment | Replica-lockstep step overwrites it on next tick. Managed-side is single source of truth. |

**No retries inside a tick** — state is unconstrained, the next tick is the retry.

## 8. RBAC

Extend the `inferno` ClusterRole with:

```yaml
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "patch"]      # patch is new
```

`get`, `list`, `watch` on `pods` are already required by the Collector. The Actuator stays cluster-scoped, which enables the optional cross-namespace `vllm-namespace` label.

## 9. Configuration

| Env var | Default | Purpose |
|---|---|---|
| `INFERNO_PAIRING_TICK_SEC` | `5` | Reconciler tick interval. `0` disables the reconciler entirely (legacy mode). |
| `INFERNO_PAIRING_LOG_LEVEL` | `info` | Per-Deployment per-tick logging granularity (`info` = state changes only, `debug` = every tick). |

Both are documented in `CLAUDE.md` under Environment Variables.

## 10. Tests

Unit (`pairing_logic_test.go`), table-driven over `(mPods, vPods) → (prunes, bindings)`:

| Scenario | Expectation |
|---|---|
| Cold start: both sides have N Ready pods, none labelled | N bindings with N distinct UUIDs |
| Steady state: all pods already mutually paired | Zero patches (idempotent) |
| Scale-up: managed has N, vLLM has N, M pods Ready on each side, M < N | M pairings, others stay unpaired |
| Pod replacement: one managed pod replaced (old gone, new no label) | Old vLLM pod's pair-id pruned; new pair created |
| Asymmetric: managed has 3 Ready pods, vLLM has 2 Ready pods | 2 pairings, 1 managed pod stays unpaired |
| Orphaned UUID: managed has pair-id `X`, no vLLM pod has `X` | Managed label pruned; pod re-paired if a vLLM pod is available |
| Mismatched UUIDs: managed pod has `X`, vLLM pod has `Y` (no peer for either) | Both pruned, then re-paired with a single new UUID |
| NotReady carrier: pod has pair-id but is NotReady | Pruned from peer; peer becomes available for re-pairing |
| `evaluator` label absent | Reconciler does nothing |

A separate `pairing_kube_test.go` exercises `pairing_kube.go` against `client-go/kubernetes/fake` to verify patch payload shape.

A kind smoke test will be added to `scripts/` in a follow-up once the `vllm-server-evaluator` is buildable in `server-sim`.

## 11. Risks

1. **Tick-interval lag.** A new pod waits up to `tick_sec` before first being labelled, so the evaluator returns 503 for that long. With `tick_sec=5` and vLLM startup of tens of seconds, this is negligible.
2. **Label-patch storm under churn.** Continuous pod replacement (e.g. failing Readiness probe) means continuous patch traffic. Ceiling is `2 × replicas` patches per tick. At realistic scale (≤10 pods/Deployment) this is fine.
3. **Cross-namespace ambiguity.** Two managed Deployments in different namespaces could (mis)point to the same vLLM Deployment name+namespace. The reconciler treats each managed Deployment independently and would happily over-pair. Mitigation: log a warning if `>1` managed Deployment references the same vLLM target. Not enforced — the workload author owns uniqueness.

## 12. Open issues

- Whether to expose a `GET /pairing` debug endpoint on the Actuator listing current pair-id assignments. Likely useful for kind-cluster debugging; defer until needed.
- Whether reconciler errors should bump a Prometheus counter exposed by the Actuator. None of the existing components expose Prometheus metrics today; defer to a separate observability pass.
