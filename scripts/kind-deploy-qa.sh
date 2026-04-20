#!/usr/bin/env bash
# Deploy the inferno control loop + queue-analysis workloads to a local kind cluster.
# Uses inferno-data/ for optimizer config (no perfParms — EKF learns from scratch)
# and queue-analysis evaluator for all workloads.
# Run from the control-loop/ repo root.
# Prerequisites: images already built and Docker available (see CLAUDE.md Step 1).

set -euo pipefail

CLUSTER=${KIND_CLUSTER:-kind-cluster}
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$REPO_ROOT/inferno-data"

echo "==> Loading images into kind cluster: $CLUSTER"
kind load docker-image quay.io/atantawi/inferno-loop:latest       --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-optimizer:latest  --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-tuner:latest      --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-server-sim:latest --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-evaluator:latest  --name "$CLUSTER"

echo "==> Creating namespaces"
kubectl apply -f "$REPO_ROOT/yamls/deploy/ns.yaml"
kubectl apply -f "$REPO_ROOT/yamls/workload/ns.yaml"

echo "==> Creating inferno ConfigMaps (blis data, no perfParms — EKF learns from scratch)"
kubectl create configmap inferno-static-data -n inferno \
  --from-file=accelerator-data.json="$DATA_DIR/accelerator-data.json" \
  --from-file=model-data.json="$DATA_DIR/model-data.json" \
  --from-file=serviceclass-data.json="$DATA_DIR/serviceclass-data.json" \
  --from-file=optimizer-data.json="$DATA_DIR/optimizer-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl create configmap inferno-dynamic-data -n inferno \
  --from-file=capacity-data.json="$DATA_DIR/capacity-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$REPO_ROOT/yamls/deploy/configmap-tuner.yaml"

echo "==> Deploying inferno pod (controller, collector, optimizer, actuator, tuner)"
kubectl apply -f "$REPO_ROOT/yamls/deploy/deploy-loop.yaml"
# EKF must fully converge before optimizer runs; disable warm-up timeout
kubectl set env deployment/inferno -n inferno -c controller INFERNO_WARM_UP_TIMEOUT=0
kubectl rollout status deployment/inferno -n inferno --timeout=120s

echo "==> Creating queue-analysis workload ConfigMap"
kubectl apply -f "$REPO_ROOT/yamls/workload/configmap-qa-small.yaml"

echo "==> Deploying queue-analysis workloads (granite_8b/H100 Premium, llama_13b/H100 Bronze)"
kubectl apply -f "$REPO_ROOT/yamls/workload/dep-qa-granite.yaml"
kubectl apply -f "$REPO_ROOT/yamls/workload/dep-qa-llama.yaml"

echo "==> Deploying load emulator"
kubectl apply -f "$REPO_ROOT/yamls/deploy/configmap-load-phases.yaml"
kubectl delete pod load-emulator -n inferno --ignore-not-found
kubectl apply -f "$REPO_ROOT/yamls/deploy/load-emulator.yaml"

echo ""
echo "==> Done. Watch controller logs with:"
echo "    kubectl logs -f -n inferno deployment/inferno -c controller"
echo ""
echo "    Watch tuner EKF output (alpha/beta/gamma convergence):"
echo "    kubectl logs -f -n inferno deployment/inferno -c tuner"
echo ""
echo "    NOTE: INFERNO_WARM_UP_TIMEOUT=0 — controller will wait for full EKF"
echo "    convergence before invoking the optimizer (no timeout override)."
echo ""
echo "    EKF target values (queue-analysis evaluator):"
echo "      granite_8b/H100: alpha=8.0ms  beta=0.016  gamma=0.0005"
echo "      llama_13b/H100:  alpha=12.0ms beta=0.024  gamma=0.00075"
