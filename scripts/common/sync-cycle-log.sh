#!/usr/bin/env bash
# Sync inferno-cycles.jsonl from the controller container to a local file.
# Runs on an interval so the Dash dashboard auto-refresh picks up new records.
#
# Usage:
#   scripts/common/sync-cycle-log.sh [interval_seconds] [local_output_path] [in_pod_source_path]
#   defaults: interval=10, output=inferno-cycles.jsonl (repo root),
#             source=/inferno-cycles.jsonl (the controller's default workdir-relative path)
#
# The in-pod source path must match the controller's INFERNO_CYCLE_LOG. Deploys that
# relocate it (e.g. run19 / the vllm-gpu + blis-qwen scripts set
# INFERNO_CYCLE_LOG=/tmp/inferno-cycles.jsonl because the workdir is read-only) must
# pass the third arg, otherwise kubectl cp silently finds nothing:
#   scripts/common/sync-cycle-log.sh 10 dashboard/inferno-cycles.jsonl /tmp/inferno-cycles.jsonl
#
# Run in background:
#   scripts/common/sync-cycle-log.sh &

set -euo pipefail

INTERVAL=${1:-10}
OUTPUT=${2:-inferno-cycles.jsonl}
SOURCE=${3:-/inferno-cycles.jsonl}

echo "Syncing ${SOURCE} from controller every ${INTERVAL}s → ${OUTPUT}"

while true; do
  POD=$(kubectl get pod -n inferno -l app=inferno -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  if [ -n "$POD" ]; then
    kubectl cp "inferno/${POD}:${SOURCE}" "${OUTPUT}" -c controller 2>/dev/null || true
  fi
  sleep "${INTERVAL}"
done
