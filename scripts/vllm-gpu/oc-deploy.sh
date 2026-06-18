#!/usr/bin/env bash
# Deploy the inferno control loop + vllm-server evaluator workload to a shared
# OpenShift cluster, running real H100 vLLM servers (Qwen2.5-14B + Llama-3.1-8B).
#
# Differences from scripts/vllm-cpu/kind-deploy.sh:
#   - No `kind load`; OpenShift pulls images from the registry on demand.
#   - Two new namespaces (inferno-system, inferno-workload) so we don't collide
#     with the other team's existing inferno/infer namespaces on the cluster.
#   - The shared manifests/common/deploy-loop.yaml and configmap-tuner.yaml
#     hard-code namespace: inferno; we sed-rewrite them at apply time.
#   - The HF_TOKEN secret is created from the local HUGGING_FACE_HUB_TOKEN
#     (or HF_TOKEN) environment variable rather than committed to git.
#
# Run from the control-loop/ repo root.
# Prerequisites:
#   - oc whoami succeeds against the target cluster.
#   - HUGGING_FACE_HUB_TOKEN (or HF_TOKEN) is exported in the local shell.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA_DIR="$REPO_ROOT/inferno-data/vllm-gpu"
COMMON="$REPO_ROOT/manifests/common"
EXP="$REPO_ROOT/manifests/vllm-gpu"

SYS_NS="inferno-system"
WORK_NS="inferno-workload"

# Models to deploy. Default "qwen llama" preserves the original two-model scenario.
# Set MODELS="qwen" for the run16 single-model concurrency surge experiment (no GPU
# split; lets qwen scale freely within the 6-H100 cap during the surge).
MODELS="${MODELS:-qwen llama}"

# Optional A/B arm selector. Unset (default) => search ON (Arm A, optimizer searches M*).
# Set ARM_MAXBATCH=128 => search OFF (Arm B): pins DEFAULT_MAX_BATCH_SIZE on the controller.
ARM_MAXBATCH="${ARM_MAXBATCH:-}"

# Rewrite hard-coded `namespace: inferno` lines in shared common YAMLs
# (deploy-loop.yaml, configmap-tuner.yaml) to target the new system namespace.
# The end-anchor on `inferno$` ensures `namespace: inferno-workload` is not
# matched. ClusterRole / ServiceAccount / RoleBinding `name: inferno` lines
# are identity references and intentionally not rewritten.
rewrite_ns() {
  sed "s/^\(  *\)namespace: inferno$/\1namespace: ${SYS_NS}/g"
}

echo "==> Pre-flight: oc whoami"
oc whoami
echo "    server: $(oc whoami --show-server)"

echo "==> Creating namespaces"
oc apply -f "$COMMON/ns-inferno-system.yaml"
oc apply -f "$COMMON/ns-inferno-workload.yaml"

echo "==> Creating HF token secret in ${WORK_NS}"
HF_TOKEN_VALUE="${HUGGING_FACE_HUB_TOKEN:-${HF_TOKEN:-}}"
if [[ -z "$HF_TOKEN_VALUE" ]]; then
  echo "ERROR: set HUGGING_FACE_HUB_TOKEN (or HF_TOKEN) before running." >&2
  echo "  e.g.  export HUGGING_FACE_HUB_TOKEN=hf_xxx" >&2
  exit 1
fi
oc create secret generic hf-token-secret -n "$WORK_NS" \
  --from-literal=token="$HF_TOKEN_VALUE" \
  --dry-run=client -o yaml | oc apply -f -

echo "==> Creating PVC + RBAC in ${WORK_NS}"
oc apply -f "$EXP/pvc-models-cache.yaml"
oc apply -f "$EXP/rbac-vllm-eval.yaml"

echo "==> Creating eval ConfigMap in ${WORK_NS}"
oc apply -f "$EXP/configmap-vllm-eval.yaml"

echo "==> Creating inferno static + dynamic data ConfigMaps in ${SYS_NS}"
oc create configmap inferno-static-data -n "$SYS_NS" \
  --from-file=accelerator-data.json="$DATA_DIR/accelerator-data.json" \
  --from-file=model-data.json="$DATA_DIR/model-data.json" \
  --from-file=serviceclass-data.json="$DATA_DIR/serviceclass-data.json" \
  --from-file=optimizer-data.json="$DATA_DIR/optimizer-data.json" \
  --save-config --dry-run=client -o yaml | oc apply -f -

oc create configmap inferno-dynamic-data -n "$SYS_NS" \
  --from-file=capacity-data.json="$DATA_DIR/capacity-data.json" \
  --save-config --dry-run=client -o yaml | oc apply -f -

echo "==> Creating tuner ConfigMap in ${SYS_NS} (namespace rewritten)"
rewrite_ns < "$COMMON/configmap-tuner.yaml" | oc apply -f -

echo "==> Deploying inferno pod (controller, collector, optimizer, actuator, tuner) into ${SYS_NS}"
# imagePullPolicy: Always — the inferno containers track the moving :latest tag
# on quay; without Always, a node that has cached an older (or wrong-arch) layer
# will silently keep using it. The kind-targeted shared deploy-loop.yaml uses
# IfNotPresent because kind preloads images via `kind load`; on a cluster we
# always want a fresh pull.
rewrite_ns < "$COMMON/deploy-loop.yaml" \
  | sed 's/imagePullPolicy: IfNotPresent/imagePullPolicy: Always/g' \
  | oc apply -f -

# Override env to match the vllm-gpu scenario:
#   - 120s control period: covers worst-case collect time (each managed deployment adds
#     a ~30s eval window) AND deliberately lengthens the scale-out reaction lag, which is
#     what exposes the concurrency-control transient in the single-model surge experiment.
#   - INFERNO_WARM_UP_TIMEOUT=10 default (perfParms are seeded; warm-up is fast)
#   - DEFAULT_MAX_BATCH_SIZE intentionally unset for the search-ON arm: the optimizer
#     searches the optimal concurrency M* (optimizer-light v0.8.0). The per-model
#     maxBatchSize in inferno-data/vllm-gpu/model-data.json (=128) is the search ceiling,
#     kept equal to the vLLM --max-num-seqs so M* can never exceed what the real server
#     honors. The vllm-server evaluator's traffic generator caps in-flight requests at M*,
#     so M* is the real running batch depth as long as M* <= --max-num-seqs.
#     For the search-OFF arm, set DEFAULT_MAX_BATCH_SIZE=128 on the controller (pins the
#     optimizer override; the server then runs at a fixed concurrency of 128).
#   - INFERNO_CYCLE_LOG=/tmp/... — OpenShift's restricted SCC makes the workdir
#     read-only, so the default relative path fails with permission denied.
oc set env deployment/inferno -n "$SYS_NS" -c controller \
  INFERNO_CONTROL_PERIOD=120 \
  INFERNO_WARM_UP_TIMEOUT=10 \
  INFERNO_CYCLE_LOG=/tmp/inferno-cycles.jsonl

# Arm B (search OFF): pin the optimizer concurrency override when ARM_MAXBATCH is set.
# Leave unset for Arm A (search ON). Either way the value is explicit per run.
if [[ -n "$ARM_MAXBATCH" ]]; then
  echo "    ARM_MAXBATCH set => Arm B (search OFF), pinning DEFAULT_MAX_BATCH_SIZE=$ARM_MAXBATCH"
  oc set env deployment/inferno -n "$SYS_NS" -c controller DEFAULT_MAX_BATCH_SIZE="$ARM_MAXBATCH"
else
  echo "    ARM_MAXBATCH unset => Arm A (search ON); DEFAULT_MAX_BATCH_SIZE not pinned"
  oc set env deployment/inferno -n "$SYS_NS" -c controller DEFAULT_MAX_BATCH_SIZE-
fi

# Collector simulate timeout > 2x maxWindowSec=30
# WATCH_NAMESPACE scopes the managed-deployment watch to inferno-workload so we
# don't iterate the other team's deployments on the shared cluster (PR #35).
oc set env deployment/inferno -n "$SYS_NS" -c collector \
  INFERNO_SIMULATE_TIMEOUT_SEC=60 \
  WATCH_NAMESPACE=inferno-workload

# Actuator's pairing reconciler also needs WATCH_NAMESPACE for the same reason.
oc set env deployment/inferno -n "$SYS_NS" -c actuator \
  WATCH_NAMESPACE=inferno-workload

oc rollout status deployment/inferno -n "$SYS_NS" --timeout=180s

# Resolve the per-model manifest files + deployment names for the selected MODELS.
vdeps=""; vnames=""; wraps=""; wnames=""
for m in $MODELS; do
  case "$m" in
    qwen)
      vdeps="$vdeps deployment-vllm-qwen.yaml";  vnames="$vnames vllm-qwen-14b-gpu"
      wraps="$wraps dep-vllm-qwen-server.yaml";  wnames="$wnames vllm-qwen-14b-server" ;;
    llama)
      vdeps="$vdeps deployment-vllm-llama.yaml"; vnames="$vnames vllm-llama-gpu"
      wraps="$wraps dep-vllm-llama-server.yaml"; wnames="$wnames vllm-llama-server" ;;
    *) echo "ERROR: unknown model '$m' in MODELS (expected qwen|llama)" >&2; exit 1 ;;
  esac
done

echo "==> Deploying vLLM servers ($MODELS) on H100"
for f in $vdeps; do oc apply -f "$EXP/$f"; done
echo "    First-run weight download to PVC may take ~15-30 min."
echo "    Waiting for vLLM Deployment(s) to become Available..."
for n in $vnames; do oc wait --for=condition=available deployment/"$n" -n "$WORK_NS" --timeout=1800s; done

echo "==> Deploying managed wrappers (server-sim + vllm-server evaluator)"
for f in $wraps; do oc apply -f "$EXP/$f"; done
for n in $wnames; do oc rollout status deployment/"$n" -n "$WORK_NS" --timeout=300s; done

echo "==> Deploying load emulator (profile from configmap-load-phases.yaml)"
oc apply -f "$EXP/configmap-load-phases.yaml"
oc delete pod load-emulator -n "$SYS_NS" --ignore-not-found
oc apply -f "$EXP/load-emulator.yaml"

echo ""
echo "==> Done."
echo ""
echo "    Watch controller logs:"
echo "      oc logs -f -n $SYS_NS deployment/inferno -c controller"
echo ""
echo "    Watch tuner EKF output:"
echo "      oc logs -f -n $SYS_NS deployment/inferno -c tuner"
echo ""
echo "    Watch the actuator pairing reconciler:"
echo "      oc logs -f -n $SYS_NS deployment/inferno -c actuator"
echo ""
echo "    Verify the evaluator resolved its paired vLLM pod:"
echo "      oc logs -n $WORK_NS deployment/vllm-qwen-14b-server -c evaluator | grep 'pairing resolved'"
echo "      oc logs -n $WORK_NS deployment/vllm-llama-server    -c evaluator | grep 'pairing resolved'"
echo ""
echo "    NOTE: control period = 120s (2 min); INFERNO_WARM_UP_TIMEOUT=10."
echo "    perfParms are seeded so the first useful cycle should appear quickly."
