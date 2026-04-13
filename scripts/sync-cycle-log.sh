#!/usr/bin/env bash
# Sync inferno-cycles.jsonl from the controller container to a local file.
# Runs on an interval so the Dash dashboard auto-refresh picks up new records.
#
# Usage:
#   scripts/sync-cycle-log.sh [interval_seconds] [local_output_path]
#   defaults: interval=10, output=inferno-cycles.jsonl (repo root)
#
# Run in background:
#   scripts/sync-cycle-log.sh &

set -euo pipefail

INTERVAL=${1:-10}
OUTPUT=${2:-inferno-cycles.jsonl}

echo "Syncing /inferno-cycles.jsonl from controller every ${INTERVAL}s → ${OUTPUT}"

while true; do
  POD=$(kubectl get pod -n inferno -l app=inferno -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  if [ -n "$POD" ]; then
    kubectl cp "inferno/${POD}:/inferno-cycles.jsonl" "${OUTPUT}" -c controller 2>/dev/null || true
  fi
  sleep "${INTERVAL}"
done
