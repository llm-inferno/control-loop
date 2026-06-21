#!/usr/bin/env bash
# Archive one experiment arm's cycle log + controller log from the inferno pod to
# experiments/run17/. Run this AFTER an arm's 30-min sequence completes and BEFORE
# redeploying the next arm — redeploy rolls a fresh controller pod and the in-pod
# /tmp/inferno-cycles.jsonl starts empty, so the data is lost otherwise.
#
# Usage:
#   scripts/vllm-gpu/save-cycle-log.sh <arm-label>
#   e.g.  scripts/vllm-gpu/save-cycle-log.sh armA-search
#         scripts/vllm-gpu/save-cycle-log.sh armB-low32
#         scripts/vllm-gpu/save-cycle-log.sh armB-high128
#
# View any saved arm offline (no live cluster needed):
#   cd dashboard && INFERNO_CYCLE_LOG=../experiments/run17/<arm-label>-cycles.jsonl python dashboard.py

set -euo pipefail

ARM="${1:?usage: save-cycle-log.sh <arm-label>}"
SYS_NS="${SYS_NS:-inferno-system}"
POD_LOG="${POD_LOG:-/tmp/inferno-cycles.jsonl}"
OUT="$(cd "$(dirname "$0")/../.." && pwd)/experiments/run17"

mkdir -p "$OUT"

oc exec -n "$SYS_NS" deployment/inferno -c controller -- cat "$POD_LOG" > "$OUT/${ARM}-cycles.jsonl"
oc logs -n "$SYS_NS" deployment/inferno -c controller > "$OUT/${ARM}-controller.log" 2>&1 || true

echo "saved $(wc -l < "$OUT/${ARM}-cycles.jsonl" | tr -d ' ') cycle records -> $OUT/${ARM}-cycles.jsonl"
echo "saved controller log                 -> $OUT/${ARM}-controller.log"
