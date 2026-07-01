#!/usr/bin/env bash
# Benchmarking-on-the-fly calibration test — deploy the inferno control loop + the single
# blis-qwen workload to kind with the TUNER ON and CALIBRATION ENABLED, seeded with deliberately
# WRONG perfParms (model-data-calib.json).
#
# What this exercises (see docs/calibration.md and the calibration plan):
#   1. Cold start: the tuner collects init observations at the load emulator's single operating
#      point. With HOLD_BACK=false the optimizer runs on the wrong static seed during collection,
#      so the early cycle log shows an over-scaled allocation.
#   2. Once init obs are collected, the init fit is ill-conditioned (single operating point →
#      kappa > TUNER_MAX_CONDITION_NUMBER): GET /calibration-status reports needsCalibration=true.
#   3. The controller asks the collector to sweep a few load points against the blis pod, POSTs the
#      batch to the tuner's /calibrate, which fits alpha/beta/gamma jointly (identifiable) and
#      stores them graduated. Warm-up clears and /merge injects the calibrated params.
#   4. The cycle log shows the allocation correct itself; GET /getparams returns alpha~12,
#      beta~0.011, gamma~1.5e-4 — the queue-analysis params that reproduce the blis backend,
#      recovered despite the wrong seed and a DIFFERENT underlying physics model (blis
#      trained-physics vs the tuner's queue-analysis fit). These differ from the run16 real-vLLM
#      fit (beta~0.042); that is expected — see experiments/calibration-blis/report-2026-06-30-calibration.md.
#
# Negative control: widen the load-phase ramp (configmap-load-phases-qwen.yaml) so natural
# excitation during collection spans operating points → the init fit is well-conditioned → no
# calibration is triggered.
#
# Run from the control-loop/ repo root. Does NOT build images (see kind-deploy-qwen.sh freshness
# gate notes; reuse already-built images).

set -euo pipefail

CLUSTER=${KIND_CLUSTER:-kind-cluster}
export KIND_EXPERIMENTAL_PROVIDER=podman
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA_DIR="$REPO_ROOT/inferno-data/blis"
COMMON="$REPO_ROOT/manifests/common"
EXP="$REPO_ROOT/manifests/blis"

echo "==> Loading images into kind cluster: $CLUSTER"
kind load docker-image quay.io/atantawi/inferno-loop:latest             --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-optimizer-light:latest  --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-tuner:latest            --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-server-sim:latest       --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-evaluator:latest        --name "$CLUSTER"

echo "==> Creating namespaces"
kubectl apply -f "$COMMON/ns-inferno.yaml"
kubectl apply -f "$COMMON/ns-infer.yaml"

echo "==> Creating inferno ConfigMaps (blis data, WRONG-seed model-data for calibration test)"
kubectl create configmap inferno-static-data -n inferno \
  --from-file=accelerator-data.json="$DATA_DIR/accelerator-data.json" \
  --from-file=model-data.json="$DATA_DIR/model-data-calib.json" \
  --from-file=serviceclass-data.json="$DATA_DIR/serviceclass-data.json" \
  --from-file=optimizer-data.json="$DATA_DIR/optimizer-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl create configmap inferno-dynamic-data -n inferno \
  --from-file=capacity-data.json="$DATA_DIR/capacity-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$COMMON/configmap-tuner.yaml"

echo "==> Deploying inferno pod (controller, collector, optimizer, actuator, tuner)"
kubectl apply -f "$COMMON/deploy-loop.yaml"

# Controller: 120s control period; deterministic cycle-log path; CALIBRATION ON.
kubectl set env deployment/inferno -n inferno -c controller \
  INFERNO_CONTROL_PERIOD=120 \
  INFERNO_WARM_UP_TIMEOUT=10 \
  INFERNO_CYCLE_LOG=/tmp/inferno-cycles.jsonl \
  INFERNO_CALIBRATION_ENABLED=true

# Collector: headroom for the blis DES solve, both for /latest and each sweep /simulate point.
kubectl set env deployment/inferno -n inferno -c collector \
  INFERNO_SIMULATE_TIMEOUT_SEC=60 \
  INFERNO_CALIB_POINT_TIMEOUT_SEC=120 \
  INFERNO_CALIB_POLL_INTERVAL_SEC=2

# Tuner ON (deploy-loop.yaml sets TUNER_HOST). Use sliding-window + HOLD_BACK=false so the
# optimizer runs on the wrong seed during collection (visible over-scale), then calibration
# corrects it. TUNER_MAX_CONDITION_NUMBER keeps its default (1000) — the ill-conditioning gate.
kubectl set env deployment/inferno -n inferno -c tuner \
  TUNER_ESTIMATOR_MODE=sliding-window \
  TUNER_INIT_HOLD_BACK=false

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
echo "==> Done. TUNER ON, CALIBRATION ON, wrong seed (model-data-calib.json)."
echo "    Watch controller:  kubectl logs -f -n inferno deployment/inferno -c controller | grep -i calib"
echo "    Watch tuner:       kubectl logs -f -n inferno deployment/inferno -c tuner | grep -i 'calibrat\\|condition'"
echo "    Calibrated params: kubectl exec -n inferno deployment/inferno -c controller -- \\"
echo "                         wget -qO- 'http://localhost:8081/getparams?model=qwen_2_5_14b&accelerator=H100'"
echo "    Trigger status:    kubectl exec -n inferno deployment/inferno -c controller -- \\"
echo "                         wget -qO- http://localhost:8081/calibration-status"
