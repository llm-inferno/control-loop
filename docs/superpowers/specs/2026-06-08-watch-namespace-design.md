# WATCH_NAMESPACE — namespace-scoped deployment watch

**Date:** 2026-06-08
**Status:** approved (design)
**Related:** [`2026-06-07-vllm-gpu-experiment.md`](2026-06-07-vllm-gpu-experiment.md), memory `project_vllm_gpu_cluster_collision`

## Problem

The Collector, Load Emulator, Actuator `/update` handler, and Actuator pairing reconciler each list `Deployments("")` cluster-wide with the label selector `inferno.server.managed=true`. When two inferno setups share a cluster — as happened on 2026-06-08 between this project and another team using identical `inferno.server.*` labels — both setups iterate each other's managed deployments and pods. Observed consequences:

- The load emulator writes dynamic `inferno.server.load.*` labels onto the other setup's pods every emulation tick.
- Each controller's optimizer runs against a conflated 4-server view, with names colliding by label. In some cycles this drove allocations to zero replicas, and the actuator's pairing reconciler followed by scaling the paired vLLM Deployments to zero.

The `vllm-gpu` experiment placed its workloads in `inferno-workload` and its control plane in `inferno-system` precisely to avoid the other team's `infer/`+`inferno/` namespaces, but cluster-wide watches make namespace separation ineffective.

## Goals

1. Allow each inferno setup to scope its managed-deployment watch to a single namespace.
2. Preserve the current cluster-wide behavior as the default — kind scenarios (`qa`, `blis`, `vllm-cpu`) and any existing single-tenant cluster setups must work unchanged.
3. No new RBAC requirements introduced or removed by this change.

## Non-goals

- Comma-separated namespace lists (multi-namespace watch). Not needed today; can be added later if a use case appears.
- Defaulting the watched namespace via the downward API. The control plane and the workloads live in different namespaces (`inferno-system` vs `inferno-workload`), so the pod's own namespace is the wrong default.
- Tightening the `inferno` ClusterRole down to a Role. The cluster-wide RBAC grant remains; only the API-level list scope changes. ClusterRole→Role is a valid follow-up but is a separate concern.
- Refactoring the package-level Collector and Actuator handlers into struct-method form.

## Design

### Env var

- **Name:** `WATCH_NAMESPACE` (operator-sdk convention).
- **Semantics:** empty/unset = cluster-wide list (current behavior). Non-empty = list only that namespace. Single value; no comma-separated lists.
- Read fresh on every list call (not cached at startup), so the value is uniformly observed across long-running components without restart-time coupling.

### Helper

Add to `pkg/controller/defaults.go`:

```go
WatchNamespaceEnvName = "WATCH_NAMESPACE"
```

Add to `pkg/controller/utils.go`:

```go
// WatchNamespace returns the namespace inferno should watch for managed
// Deployments. Empty string means cluster-wide (default; backwards compatible).
func WatchNamespace() string { return os.Getenv(WatchNamespaceEnvName) }
```

### Call-site changes

Three sites get a one-line scope change; one site (`Actuator /update`) gets a small refactor instead.

| File | Line | Caller | Change |
|---|---|---|---|
| `pkg/collector/handlers.go` | 27 | Collector `/collect` | `Deployments("")` → `Deployments(ctrl.WatchNamespace())` |
| `pkg/loademulator/loademulator.go` | 68 | `LoadEmulator.Run` | `Deployments("")` → `Deployments(ctrl.WatchNamespace())` |
| `pkg/actuator/pairing.go` | 132 | `reconcileAll` (pairing reconciler) | `Deployments("")` → `Deployments(ctrl.WatchNamespace())` |
| `pkg/actuator/handlers.go` | 21–96 | Actuator `/update` | Drop the cluster-wide list; drive both loops from `info.KubeResource` (see below) |

Downstream `ReplicaSets(d.Namespace)` and `Pods(d.Namespace)` lookups in the same files are already namespace-scoped to each found deployment and need no change.

No code path in the project lists `Deployments` cluster-wide outside these four sites (verified via `grep -n 'Deployments("")' pkg/ cmd/`).

### Actuator `/update` refactor

The current handler does a cluster-wide list and uses it twice:

1. **First loop** — for each entry in `allocMap`, looks up the matching `Deployment` in the listed items by UID and patches it. The list is being used as a UID→Deployment lookup, but the handler already has `{UID, Name, Namespace}` in `info.KubeResource` (`serverMap`).
2. **Second loop** — iterates *every* listed managed Deployment and zeroes out replicas on any not touched by the first loop ("no feasible allocation"). This is the loop that, on 2026-06-08, scaled another team's Deployments toward zero because they matched the cluster-wide selector but were absent from our `allocMap`.

`serverMap` (built by the Collector and shipped in the `/update` payload) already contains every managed Deployment from the Collector's view. Once the Collector is namespace-scoped, `serverMap` is namespace-scoped automatically — and `serverMap ⊆ clusterScannedMap` always holds. So the refactor is:

- Remove the `Deployments("").List(...)` call at the top of `update`.
- Iterate `serverMap` entries directly. For each `serverName`:
  - If `serverName ∈ allocMap`, patch with the optimizer's `AllocationData`.
  - Otherwise, patch with the zero-allocation (replicas=0, accelerator="", maxBatch=0, zero load) — same "no feasible allocation" branch as today, just driven from `serverMap` instead of the cluster scan.
- The set zeroed out becomes `{serverMap − allocMap}` rather than `{clusterScannedMap − allocMap}` — an architectural correction, not a feature regression.

The `patchDeployment` call only needs `nameSpace`, `deployName`, and the `AllocationData` to construct its JSON patch; the full `v1.Deployment` value is currently used solely for a `Printf` of the deployment's prior label values. We will either drop those columns from the log line or fetch the Deployment with `Deployments(ns).Get(name)` per entry. Choice deferred to the implementation plan.

#### Behavioral consequences

- A Deployment that becomes managed *between* the Collector's `/collect` and the Actuator's `/update` will not be scaled by this `/update` call. It is picked up on the next Collect+Update cycle (≤ one control period later). This is acceptable: typical `collect→update` separation is ~265 ms, and the next cycle is at most `INFERNO_CONTROL_PERIOD` seconds away.
- A Deployment whose `inferno.server.managed` label is removed mid-cycle will no longer be touched by the Actuator at all. Today's behavior is the same — the cluster-wide list would no longer match it either.
- A Deployment deleted between the Collector's `/collect` and this `/update` is tolerated: `apierrors.IsNotFound` errors from the Patch are logged and skipped, so a single transient deletion does not strand the remaining patches in the batch. (In the pre-refactor handler this was implicit — the cluster-wide list filtered the deletion out before the loop. Driving from `serverMap` re-introduces the mid-cycle race; the explicit skip restores the prior behavior.)
- The Actuator no longer reads `inferno.server.managed` directly. This is also a step toward issue #34 (configurable managed-label key/value): once the Actuator is decoupled from the label, that follow-up only needs to touch the Collector, Load Emulator, and pairing reconciler.

### Manifests and scripts

- `manifests/common/deploy-loop.yaml` — leave `WATCH_NAMESPACE` unset. All existing scenarios (`qa`, `blis`, `vllm-cpu`) remain backwards compatible.
- This branch ships the code change and CLAUDE.md doc only. It does **not** modify any per-experiment manifest or deploy script.
- The `vllm-gpu` scenario lives on a separate branch (`feat/vllm-gpu-experiment`, pending its own PR). Wiring `WATCH_NAMESPACE=inferno-workload` into that scenario's `scripts/vllm-gpu/oc-deploy.sh` env overrides and into its `manifests/vllm-gpu/load-emulator.yaml` container spec is a follow-up commit on that branch, not part of this PR. The dependency is explicit: `vllm-gpu` cannot be safely redeployed on the shared cluster until *both* branches have merged.
- Other cluster scenarios that share a cluster with another inferno setup can adopt the same pattern (script-level `set env` for the inferno Deployment plus an explicit env entry in the standalone load-emulator Deployment).

### Documentation

Add a row to the env-var table in `CLAUDE.md`:

| Variable | Default | Description |
|---|---|---|
| `WATCH_NAMESPACE` | unset (cluster-wide) | Namespace to scope managed-deployment watches to. Set on shared clusters where another inferno setup uses the same `inferno.server.*` labels in different namespaces. Applies to the Collector, Load Emulator, and Actuator pairing reconciler. The Actuator `/update` handler is implicitly scoped via the Collector-built `serverMap` it receives. |

A short note in the existing "Managed deployments" paragraph cross-references the new env var.

### Testing

The repo has no automated tests. Verification is by:

1. Building images and redeploying the `vllm-gpu` scenario with `WATCH_NAMESPACE=inferno-workload`.
2. Confirming controller logs report only the two managed deployments in `inferno-workload` and no longer touch the other team's `infer/` workloads.
3. Confirming the load emulator's "N deployment(s) updated" line shows N=2 (our two), not N=4.

## Limitation: one-sided protection

`WATCH_NAMESPACE` is asymmetric. It stops *this* control plane from iterating the other team's deployments and pods, so our outbound writes (dynamic load labels, replica scaling, pairing UUIDs) are confined to the configured namespace. It does **not** stop the other team's control plane from iterating ours: they still watch `inferno.server.managed=true` cluster-wide and our managed pods still match that selector, so their controller will continue to scale our deployments and their load emulator will continue to overwrite our load labels.

Fully isolating two co-tenant inferno setups requires the second pod side as well — making the managed-label key (or value) configurable so each setup's pods carry a label that the other setup's selector does not match. That work is intentionally out of scope here and is tracked as a follow-up spec/PR. This change is still useful on its own:

- On single-tenant clusters it's a blast-radius reduction (faster lists, smaller RBAC surface in any future ClusterRole→Role tightening).
- On shared clusters it's the necessary first half of a two-PR fix; landing it early unblocks the rest of the work without forcing a single oversized review.

## Risks

- **Logic regression at the three env-var sites:** every change is a one-line argument swap. Risk is low. Read by inspection.
- **Empty string passed to client-go:** `Deployments("")` and `Deployments("inferno-workload")` are both legal — `""` is the documented signal for "all namespaces" in client-go. Backwards compatibility is structural, not behavioral.
- **Forgotten call site:** mitigated by the explicit grep above; if a future call site appears, the same helper is one import away.
- **Actuator `/update` refactor:** the larger change. Risk-mitigators: (a) `serverMap ⊆ clusterScannedMap` for any single `/update` call where the Collector and Actuator share the same `WATCH_NAMESPACE` and label selector — the only cases not in `serverMap` are exactly the ones we no longer want to touch; (b) the zero-allocation path is unchanged in shape, only its driving set changes; (c) verified end-to-end on a kind scenario that scale-out and scale-down still work.

## Out of scope / future work

- **Configurable managed-label key/value** so each inferno setup's pods are invisible to other setups' watches. This is the symmetric counterpart to `WATCH_NAMESPACE`; tracked as a separate spec.
- Tighten the `inferno` ClusterRole to a namespace-scoped Role when `WATCH_NAMESPACE` is set. Requires either two RBAC variants (cluster vs namespace) or templating. Not blocking the current goal of side-by-side coexistence.
- Add `WATCH_NAMESPACE` plumbing to any future component that lists managed deployments cluster-wide.
