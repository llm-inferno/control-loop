#!/usr/bin/env bash
# run19 — deploy the inferno control loop + a single blis-qwen workload to kind.
# Single arm A (M* search ON), NO_TUNER (static seeded perfParms), pass-through saturation.
# Contrasts the blis trained-physics simulator against the real-vLLM run18 arm A.
#
# Prerequisite: the user has rebuilt inferno-evaluator from current server-sim main
# (batch-aware saturation patch b2b4eca, merged 2026-06-24) before running this.
# This script does NOT build images; it loads them and asserts the evaluator is fresh.
#
# Run from the control-loop/ repo root.

set -euo pipefail

CLUSTER=${KIND_CLUSTER:-kind-cluster}
export KIND_EXPERIMENTAL_PROVIDER=podman   # images live in the podman store
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA_DIR="$REPO_ROOT/inferno-data/blis"
COMMON="$REPO_ROOT/manifests/common"
EXP="$REPO_ROOT/manifests/blis"

# --- Freshness gate: evaluator must post-date the blis saturation patch ----------
echo "==> Asserting inferno-evaluator image is newer than the blis saturation patch (2026-06-24)"
EVAL_CREATED="$(podman images --format '{{.Repository}} {{.CreatedAt}}' \
  | awk '$1=="quay.io/atantawi/inferno-evaluator"{ $1=""; sub(/^ /,""); print; exit }')"
if [[ -z "$EVAL_CREATED" ]]; then
  echo "ERROR: quay.io/atantawi/inferno-evaluator:latest not found in podman. Build it first." >&2
  exit 1
fi
# CreatedAt looks like "2026-06-25 13:25:29 +0000 UTC"; compare the YYYY-MM-DD date.
EVAL_DATE="${EVAL_CREATED%% *}"
if [[ "$EVAL_DATE" < "2026-06-24" ]]; then
  echo "ERROR: evaluator image dated $EVAL_DATE predates the blis saturation patch (2026-06-24)." >&2
  echo "       Rebuild inferno-evaluator from current server-sim main, then re-run." >&2
  exit 1
fi
echo "    evaluator image dated $EVAL_DATE — OK"

echo "==> Loading images into kind cluster: $CLUSTER"
kind load docker-image quay.io/atantawi/inferno-loop:latest             --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-optimizer-light:latest  --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-tuner:latest            --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-server-sim:latest       --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-evaluator:latest        --name "$CLUSTER"

echo "==> Creating namespaces"
kubectl apply -f "$COMMON/ns-inferno.yaml"
kubectl apply -f "$COMMON/ns-infer.yaml"

echo "==> Creating inferno ConfigMaps (blis data)"
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

# run19 controller env: 120s control period (run18 parity), seeded perfParms so
# warm-up is fast, deterministic cycle-log path for the archiver.
kubectl set env deployment/inferno -n inferno -c controller \
  INFERNO_CONTROL_PERIOD=120 \
  INFERNO_WARM_UP_TIMEOUT=10 \
  INFERNO_CYCLE_LOG=/tmp/inferno-cycles.jsonl

# Collector: bump the per-pod GET /latest timeout from the 30s default to 60s so a
# high-load blis DES solve has headroom to finish; still well under the 120s control
# period. (deploy-loop.yaml sets it on the collector container; this overrides it.)
kubectl set env deployment/inferno -n inferno -c collector \
  INFERNO_SIMULATE_TIMEOUT_SEC=60

# NO_TUNER: unset TUNER_HOST so the controller skips /tune+/merge and the optimizer
# runs on the static seeded perfParms (model-data.json) every cycle. The seed's gamma
# (5.77e-5) is blis-appropriate — the tuner, when enabled, converges to ~6e-5 — giving
# maxBatch~59 and sensible scaling. With the tuner ON, its cold-start GuessInitState
# (gamma~1.3e-3, derived from ~0.9*ITL) transiently overrides the good seed and the
# optimizer over-scales to the capacity cap (maxBatch=2) until the EKF gets
# operating-point spread. Static-and-correct beats the wild warm-up transient.
kubectl set env deployment/inferno -n inferno -c controller TUNER_HOST-

# Arm A: search ON. DEFAULT_MAX_BATCH_SIZE is not set in deploy-loop.yaml, so the
# optimizer searches M* bounded by the model-data maxBatchSize ceiling (128). Remove
# any stale value defensively (harmless if absent).
kubectl set env deployment/inferno -n inferno -c controller DEFAULT_MAX_BATCH_SIZE- || true

kubectl rollout status deployment/inferno -n inferno --timeout=120s

echo "==> Creating blis-qwen workload ConfigMap"
kubectl apply -f "$EXP/configmap-blis-qwen.yaml"

echo "==> Deploying blis-qwen workload (qwen_2_5_14b/H100 Bronze, 1 replica)"
kubectl apply -f "$EXP/dep-blis-qwen.yaml"

echo "==> Deploying load emulator (run18 5x ramp profile)"
kubectl apply -f "$EXP/configmap-load-phases-qwen.yaml"
kubectl delete pod load-emulator -n inferno --ignore-not-found
kubectl apply -f "$EXP/load-emulator.yaml"

echo ""
echo "==> Done. NO_TUNER, search ON, control period 120s."
echo "    Watch controller:  kubectl logs -f -n inferno deployment/inferno -c controller"
echo "    Watch scaling:     kubectl get deployment blis-qwen -n infer -w"
echo "    After ~30 min:     RUN=run19 scripts/blis/save-cycle-log.sh armA-search"
