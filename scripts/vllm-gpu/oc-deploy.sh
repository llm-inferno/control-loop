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
#   - 120s control period covers worst-case collect time (2 deployments x 30s window)
#   - INFERNO_WARM_UP_TIMEOUT=10 default (perfParms are seeded; warm-up is fast)
#   - DEFAULT_MAX_BATCH_SIZE=32 matches per-server label and per-model maxBatchSize
oc set env deployment/inferno -n "$SYS_NS" -c controller \
  INFERNO_CONTROL_PERIOD=120 \
  INFERNO_WARM_UP_TIMEOUT=10 \
  DEFAULT_MAX_BATCH_SIZE=32

# Collector simulate timeout > 2x maxWindowSec=30
oc set env deployment/inferno -n "$SYS_NS" -c collector \
  INFERNO_SIMULATE_TIMEOUT_SEC=60

oc rollout status deployment/inferno -n "$SYS_NS" --timeout=180s

echo "==> Deploying vLLM servers (Qwen2.5-14B + Llama-3.1-8B on H100)"
oc apply -f "$EXP/deployment-vllm-qwen.yaml"
oc apply -f "$EXP/deployment-vllm-llama.yaml"

echo "    First-run weight download to PVC may take ~15-30 min for both models."
echo "    Waiting for both vLLM Deployments to become Available..."
oc wait --for=condition=available deployment/vllm-qwen-14b-gpu -n "$WORK_NS" --timeout=1800s
oc wait --for=condition=available deployment/vllm-llama-gpu    -n "$WORK_NS" --timeout=1800s

echo "==> Deploying managed wrappers (server-sim + vllm-server evaluator)"
oc apply -f "$EXP/dep-vllm-qwen-server.yaml"
oc apply -f "$EXP/dep-vllm-llama-server.yaml"
oc rollout status deployment/vllm-qwen-14b-server -n "$WORK_NS" --timeout=300s
oc rollout status deployment/vllm-llama-server    -n "$WORK_NS" --timeout=300s

echo "==> Deploying load emulator (5-phase 1x->3x->1x ramp, 6 min per phase)"
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
