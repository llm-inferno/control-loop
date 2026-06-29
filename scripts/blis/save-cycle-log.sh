#!/usr/bin/env bash
# Archive one run's cycle log + all control/workload/emulator container logs from
# the kind cluster to experiments/<RUN>/. Run AFTER the ~30-min profile completes.
#
# Usage:  RUN=run19 scripts/blis/save-cycle-log.sh armA-search
#
# View offline:  cd dashboard && INFERNO_CYCLE_LOG=../experiments/run19/<arm>-cycles.jsonl python dashboard.py

set -euo pipefail

ARM="${1:?usage: save-cycle-log.sh <arm-label>}"
SYS_NS="${SYS_NS:-inferno}"
WORK_NS="${WORK_NS:-infer}"
POD_LOG="${POD_LOG:-/tmp/inferno-cycles.jsonl}"
RUN="${RUN:-run19}"
WORKLOAD="${WORKLOAD:-blis-qwen}"
OUT="$(cd "$(dirname "$0")/../.." && pwd)/experiments/${RUN}"
LOGS="$OUT/logs"

mkdir -p "$LOGS"

# Cycle log (the dashboard JSONL) at the run-dir top level.
kubectl exec -n "$SYS_NS" deployment/inferno -c controller -- cat "$POD_LOG" > "$OUT/${ARM}-cycles.jsonl"
echo "saved $(wc -l < "$OUT/${ARM}-cycles.jsonl" | tr -d ' ') cycle records -> $OUT/${ARM}-cycles.jsonl"

# All five control-pod container logs (tuner present but idle under NO_TUNER).
for c in controller collector optimizer actuator tuner; do
  kubectl logs -n "$SYS_NS" deployment/inferno -c "$c" > "$LOGS/${ARM}-${c}.log" 2>&1 || true
  echo "saved ${c} log -> $LOGS/${ARM}-${c}.log"
done

# Workload pod sidecars: server-sim (traffic gen) + evaluator (blis).
for c in server-sim evaluator; do
  kubectl logs -n "$WORK_NS" deployment/"$WORKLOAD" -c "$c" > "$LOGS/${ARM}-${WORKLOAD}-${c}.log" 2>&1 || true
  echo "saved ${WORKLOAD}/${c} log -> $LOGS/${ARM}-${WORKLOAD}-${c}.log"
done

# Load emulator pod log.
kubectl logs -n "$SYS_NS" pod/load-emulator > "$LOGS/${ARM}-load-emulator.log" 2>&1 || true
echo "saved load-emulator log -> $LOGS/${ARM}-load-emulator.log"
