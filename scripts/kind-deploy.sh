#!/usr/bin/env bash
# Deploy the inferno control loop + workloads to a local kind cluster.
# Run from the control-loop/ repo root.
# Prerequisites: images already built and Docker available (see CLAUDE.md Step 1).

set -euo pipefail

CLUSTER=${KIND_CLUSTER:-kind-cluster}
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$REPO_ROOT/sample-data/large"
MODEL_TUNER_DIR="$REPO_ROOT/../model-tuner"

echo "==> Loading images into kind cluster: $CLUSTER"
kind load docker-image quay.io/atantawi/inferno-loop:latest      --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-optimizer:latest --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-tuner:latest     --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-server-sim:latest --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-evaluator:latest  --name "$CLUSTER"

echo "==> Creating namespaces"
kubectl apply -f "$REPO_ROOT/yamls/deploy/ns.yaml"
kubectl apply -f "$REPO_ROOT/yamls/workload/ns.yaml"

echo "==> Creating inferno ConfigMaps"
kubectl create configmap inferno-static-data -n inferno \
  --from-file=accelerator-data.json="$DATA_DIR/accelerator-data.json" \
  --from-file=model-data.json="$DATA_DIR/model-data.json" \
  --from-file=serviceclass-data.json="$DATA_DIR/serviceclass-data.json" \
  --from-file=optimizer-data.json="$DATA_DIR/optimizer-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl create configmap inferno-dynamic-data -n inferno \
  --from-file=capacity-data.json="$DATA_DIR/capacity-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$MODEL_TUNER_DIR/deploy/configmap.yaml"

echo "==> Deploying inferno pod (controller, collector, optimizer, actuator, tuner)"
kubectl apply -f "$REPO_ROOT/yamls/deploy/deploy-loop.yaml"
kubectl rollout restart deployment/inferno -n inferno
kubectl rollout status  deployment/inferno -n inferno --timeout=120s

echo "==> Creating workload ConfigMaps"
kubectl create configmap server-sim-model-data -n infer \
  --from-file=model-data.json="$DATA_DIR/model-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

echo "==> Deploying workloads (dep1: premium-llama-13b, dep2: bronze-granite-13b)"
kubectl apply -f "$REPO_ROOT/yamls/workload/dep1.yaml"
kubectl apply -f "$REPO_ROOT/yamls/workload/dep2.yaml"

echo "==> Deploying load emulator"
PHASES_FILE="$REPO_ROOT/sample-data/load-phases.yaml"
if [ -f "$PHASES_FILE" ]; then
  echo "    Creating load-phases-config ConfigMap from $PHASES_FILE"
  kubectl create configmap load-phases-config -n inferno \
    --from-file=phases.yaml="$PHASES_FILE" \
    --save-config --dry-run=client -o yaml | kubectl apply -f -
else
  echo "    No load-phases.yaml found; skipping load-phases-config ConfigMap (phases disabled)"
fi
kubectl apply -f "$REPO_ROOT/yamls/deploy/load-emulator.yaml"

echo ""
echo "==> Done. Watch controller logs with:"
echo "    kubectl logs -f -n inferno deployment/inferno -c controller"
