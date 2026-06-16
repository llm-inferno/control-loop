#!/usr/bin/env bash
# Deploy the inferno control loop + a single vllm-server-evaluator workload to
# a local kind cluster. The workload pairs a managed Deployment (server-sim +
# evaluator sidecar) with a CPU-only vLLM Deployment running Qwen2.5-0.5B-Instruct.
#
# Uses inferno-data/vllm-cpu/ for optimizer config (cpu accelerator, qwen_0_5b
# model with no perfParms — EKF learns from scratch). 5-minute control period
# matches the evaluator's max measurement window. INFERNO_WARM_UP_TIMEOUT=0 so
# the optimizer waits for full EKF convergence.
#
# Run from the control-loop/ repo root.
# Prerequisites:
#   - inferno images built (see CLAUDE.md Step 1)
#   - quay.io/atantawi/inferno-server-sim:latest and
#     quay.io/atantawi/inferno-evaluator:latest images built locally from the
#     server-sim repo (see ../server-sim/deploy/k8s/LOCAL-KIND-TESTING.md)
#   - vllm/vllm-openai-cpu:latest-arm64 pulled locally (~8 GB)
#   - kind node has at least 12 GB of memory available (vLLM AOT compile peaks)

set -euo pipefail

CLUSTER=${KIND_CLUSTER:-kind-cluster}
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA_DIR="$REPO_ROOT/inferno-data/vllm-cpu"
COMMON="$REPO_ROOT/manifests/common"
EXP="$REPO_ROOT/manifests/vllm-cpu"

echo "==> Loading inferno images into kind cluster: $CLUSTER"
kind load docker-image quay.io/atantawi/inferno-loop:latest      --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-optimizer-light:latest --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-tuner:latest     --name "$CLUSTER"

echo "==> Loading server-sim/evaluator images into kind cluster: $CLUSTER"
kind load docker-image quay.io/atantawi/inferno-server-sim:latest --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-evaluator:latest  --name "$CLUSTER"

echo "==> Loading vLLM CPU image into kind cluster: $CLUSTER (this can take a minute)"
kind load docker-image vllm/vllm-openai-cpu:latest-arm64 --name "$CLUSTER"

echo "==> Creating namespaces"
kubectl apply -f "$COMMON/ns-inferno.yaml"
kubectl apply -f "$COMMON/ns-infer.yaml"

echo "==> Creating inferno ConfigMaps (cpu/qwen data, no perfParms)"
kubectl create configmap inferno-static-data -n inferno \
  --from-file=accelerator-data.json="$DATA_DIR/accelerator-data.json" \
  --from-file=model-data.json="$DATA_DIR/model-data.json" \
  --from-file=serviceclass-data.json="$DATA_DIR/serviceclass-data.json" \
  --from-file=optimizer-data.json="$DATA_DIR/optimizer-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl create configmap inferno-dynamic-data -n inferno \
  --from-file=capacity-data.json="$DATA_DIR/capacity-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$COMMON/configmap-tuner.yaml"

echo "==> Deploying inferno pod (controller, collector, optimizer, actuator, tuner)"
kubectl apply -f "$COMMON/deploy-loop.yaml"
# 2.5-minute control period matches evaluator max measurement window.
# DEFAULT_MAX_BATCH_SIZE intentionally unset: the optimizer searches the optimal
# concurrency M* (optimizer-light v0.8.0). The per-model maxBatchSize in
# inferno-data/vllm-cpu/model-data.json (=8) is the search ceiling, kept equal to
# the vLLM --max-num-seqs so M* can never exceed what the real server honors.
kubectl set env deployment/inferno -n inferno -c controller \
  INFERNO_CONTROL_PERIOD=150 \
  INFERNO_WARM_UP_TIMEOUT=0 \
  INFERNO_STARTUP_DELAY=0
# vllm-server evaluator drives a real vLLM pod; sampling window can reach
# warmupSec + maxWindowSec (production: 30 + 300 = 330s). Override the default
# 30s /simulate timeout so /collect doesn't abort while polling.
kubectl set env deployment/inferno -n inferno -c collector \
  INFERNO_SIMULATE_TIMEOUT_SEC=360
kubectl rollout status deployment/inferno -n inferno --timeout=120s

echo "==> Creating evaluator RBAC + ConfigMap in infer namespace"
kubectl apply -f "$EXP/rbac-vllm-eval.yaml"
kubectl apply -f "$EXP/configmap-vllm-eval.yaml"

echo "==> Deploying paired vLLM Deployment (CPU, Qwen2.5-0.5B-Instruct)"
kubectl apply -f "$EXP/deployment-vllm-cpu.yaml"
echo "    Waiting for vLLM to become ready (model download + AOT compile, up to 8 minutes)..."
kubectl wait --for=condition=available deployment/vllm-qwen-cpu -n infer --timeout=600s

echo "==> Deploying managed workload (server-sim + vllm-server evaluator)"
kubectl apply -f "$EXP/dep-vllm-qwen.yaml"
kubectl rollout status deployment/vllm-qwen-server -n infer --timeout=120s

echo "==> Deploying load emulator with vllm phase sequence (0.5 -> 1.0 -> 0.5 RPS, 20 min each)"
kubectl apply -f "$EXP/configmap-load-phases.yaml"
kubectl delete pod load-emulator -n inferno --ignore-not-found
kubectl apply -f "$EXP/load-emulator.yaml"

echo ""
echo "==> Done. Watch controller logs:"
echo "    kubectl logs -f -n inferno deployment/inferno -c controller"
echo ""
echo "    Watch tuner EKF output (alpha/beta/gamma):"
echo "    kubectl logs -f -n inferno deployment/inferno -c tuner"
echo ""
echo "    Watch the actuator pairing reconciler:"
echo "    kubectl logs -f -n inferno deployment/inferno -c actuator"
echo ""
echo "    Verify the evaluator resolved its paired vLLM pod:"
echo "    kubectl logs -n infer deployment/vllm-qwen-server -c evaluator | grep 'pairing resolved'"
echo ""
echo "    NOTE: INFERNO_WARM_UP_TIMEOUT=0, INFERNO_CONTROL_PERIOD=150 (2.5 min)."
echo "    The controller will wait for full EKF convergence before invoking the optimizer."
