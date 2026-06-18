#!/usr/bin/env python3
"""Plot the run16 concurrency-control surge A/B for qwen.

Reads armA-surge-cycles.jsonl (search ON) and armB-surge-cycles.jsonl
(pinned M*=128) and produces a 3-panel figure focused on the surge window:
  1. Offered load (RPM) vs cycle           — the step surge stimulus
  2. ITL (ms) vs cycle, with SLO line       — the contrast (Arm B should spike)
  3. Replicas + maxBatch (M*) vs cycle      — the control response

Usage: python3 plot_surge.py [armA.jsonl armB.jsonl outfile.png]
"""
import json
import sys

import matplotlib.pyplot as plt

MODEL = "qwen_2_5_14b"


def load(path):
    """Return list of (cycle, rpm, itl, ttft, sloItl, replicas, maxBatch) for qwen."""
    rows = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            rec = json.loads(line)
            srv = next((s for s in rec.get("servers", []) if s["model"] == MODEL), None)
            if srv is None:
                continue
            rows.append(
                dict(
                    cycle=rec["cycle"],
                    rpm=srv["rpm"],
                    itl=srv["itl"],
                    ttft=srv["ttft"],
                    sloItl=srv["sloItl"],
                    sloTtft=srv["sloTtft"],
                    replicas=srv["replicas"],
                    maxBatch=srv["maxBatch"],
                )
            )
    return rows


def col(rows, key):
    return [r[key] for r in rows]


def main():
    armA = sys.argv[1] if len(sys.argv) > 1 else "armA-surge-cycles.jsonl"
    armB = sys.argv[2] if len(sys.argv) > 2 else "armB-surge-cycles.jsonl"
    out = sys.argv[3] if len(sys.argv) > 3 else "run16-surge-ab.png"

    A = load(armA)
    B = load(armB)
    slo = (A or B)[0]["sloItl"]

    # Re-index each arm to "cycle since first record" so the two runs (which start
    # at different controller cycle counters) align on a common x-axis.
    def rel(rows):
        c0 = rows[0]["cycle"]
        return [r["cycle"] - c0 for r in rows]

    fig, (ax0, ax1, ax2, ax3) = plt.subplots(4, 1, figsize=(10, 14))

    # Panel 1: offered RPM vs relative cycle
    ax0.plot(rel(A), col(A, "rpm"), "o-", color="C0", label="Arm A (search M*)")
    ax0.plot(rel(B), col(B, "rpm"), "s--", color="C1", label="Arm B (pinned 128)")
    ax0.set_ylabel("Offered RPM")
    ax0.set_xlabel("Cycle since sweep start")
    ax0.set_title("run16 — qwen concurrency-control surge A/B (real vLLM / H100, 1024/512)")
    ax0.legend(loc="best")
    ax0.grid(alpha=0.3)

    # Panel 2: ITL vs relative cycle, with SLO
    ax1.plot(rel(A), col(A, "itl"), "o-", color="C0", label="Arm A (search M*)")
    ax1.plot(rel(B), col(B, "itl"), "s--", color="C1", label="Arm B (pinned 128)")
    ax1.axhline(slo, color="red", ls=":", label=f"ITL SLO = {slo} ms")
    ax1.set_ylabel("ITL (ms)")
    ax1.set_xlabel("Cycle since sweep start")
    ax1.legend(loc="best")
    ax1.grid(alpha=0.3)

    # Panel 3: ITL vs offered RPM scatter — the honest "is there a contrast?" view.
    # If concurrency control mattered, Arm B (high cap) would sit ABOVE Arm A at a
    # given RPM. Overlapping clouds => null result.
    ax2.scatter(col(A, "rpm"), col(A, "itl"), color="C0", marker="o", label="Arm A (search M*)")
    ax2.scatter(col(B, "rpm"), col(B, "itl"), color="C1", marker="s", label="Arm B (pinned 128)")
    ax2.axhline(slo, color="red", ls=":", label=f"ITL SLO = {slo} ms")
    ax2.set_ylabel("ITL (ms)")
    ax2.set_xlabel("Offered RPM")
    ax2.legend(loc="best")
    ax2.grid(alpha=0.3)

    # Panel 4: replicas + maxBatch vs relative cycle
    ax3.plot(rel(A), col(A, "replicas"), "o-", color="C0", label="Arm A replicas")
    ax3.plot(rel(B), col(B, "replicas"), "s--", color="C1", label="Arm B replicas")
    ax3b = ax3.twinx()
    ax3b.plot(rel(A), col(A, "maxBatch"), "^:", color="C2", label="Arm A M* (searched)")
    ax3b.plot(rel(B), col(B, "maxBatch"), "v:", color="C3", label="Arm B M* (pinned 128)")
    ax3.set_ylabel("Replicas")
    ax3b.set_ylabel("maxBatch (M*)")
    ax3.set_xlabel("Cycle since sweep start")
    lines, labels = ax3.get_legend_handles_labels()
    lines2, labels2 = ax3b.get_legend_handles_labels()
    ax3.legend(lines + lines2, labels + labels2, loc="best")
    ax3.grid(alpha=0.3)

    fig.tight_layout()
    fig.savefig(out, dpi=130)
    print(f"wrote {out}  (Arm A: {len(A)} cycles, Arm B: {len(B)} cycles)")


if __name__ == "__main__":
    main()
