#!/usr/bin/env python3
"""run17 three-arm figure: allocation + occupancy + latency vs offered load.

Reads experiments/run17/<arm>-cycles.jsonl for the three arms and renders a
3-panel figure (replicas, occupancy/replica, ITL) as a function of offered RPM,
so the arms are load-aligned despite being separate runs.

Usage:  python3 experiments/run17/plot_run17.py
Output: experiments/run17/run17-three-arm.png
"""
import json, os
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

HERE = os.path.dirname(os.path.abspath(__file__))
ARMS = [
    ("A — search M*",  "armA-search-cycles.jsonl",   "#1f77b4", "o", None),
    ("B-low — M*=32",  "armB-low32-cycles.jsonl",    "#d62728", "s", 32),
    ("B-high — M*=128","armB-high128-cycles.jsonl",  "#2ca02c", "^", 128),
]
ITL_SLO = 20  # qwen Bronze

def load(fn):
    p = os.path.join(HERE, fn)
    pts = []
    if not os.path.exists(p):
        return pts
    for line in open(p):
        line = line.strip()
        if not line:
            continue
        r = json.loads(line)
        for s in r.get("servers", []):
            pts.append((s.get("rpm", 0), s.get("replicas", 0), s.get("occPerReplica", 0),
                        s.get("itl", 0), s.get("maxBatch", 0)))
    return sorted(pts)  # sort by offered rpm

fig, axes = plt.subplots(1, 3, figsize=(16, 4.6))
fig.suptitle("run17 — concurrency control A/B on real vLLM (Qwen2.5-14B / H100), tuner off, 5× ramp",
             fontsize=13, fontweight="bold")

for label, fn, color, mk, pin in ARMS:
    pts = load(fn)
    if not pts:
        continue
    rpm = [p[0] for p in pts]
    repl = [p[1] for p in pts]
    occ = [p[2] for p in pts]
    itl = [p[3] for p in pts]
    axes[0].plot(rpm, repl, marker=mk, color=color, label=label, ms=6, lw=1.4, alpha=0.85)
    axes[1].plot(rpm, occ, marker=mk, color=color, label=label, ms=6, lw=1.4, alpha=0.85)
    axes[2].plot(rpm, itl, marker=mk, color=color, label=label, ms=6, lw=1.4, alpha=0.85)
    if pin:  # draw the pinned M* ceiling on the occupancy panel
        axes[1].axhline(pin, color=color, ls=":", lw=1, alpha=0.5)

axes[0].set_title("Replicas allocated vs offered load")
axes[0].set_ylabel("replicas")
axes[0].axhline(8, color="gray", ls="--", lw=1, alpha=0.6)
axes[0].text(300, 8.1, "capacity cap = 8 H100", fontsize=8, color="gray")

axes[1].set_title("In-service occupancy / replica\n(dotted = pinned M* ceiling)")
axes[1].set_ylabel("occ per replica")

axes[2].set_title("Attained ITL vs offered load")
axes[2].set_ylabel("ITL (ms)")
axes[2].axhline(ITL_SLO, color="black", ls="--", lw=1.2)
axes[2].text(300, ITL_SLO + 0.3, f"ITL SLO = {ITL_SLO} ms", fontsize=8)

for ax in axes:
    ax.set_xlabel("offered load (RPM)")
    ax.grid(True, alpha=0.3)
    ax.legend(fontsize=8, loc="best")

fig.tight_layout(rect=[0, 0, 1, 0.94])
out = os.path.join(HERE, "run17-three-arm.png")
fig.savefig(out, dpi=130)
print("wrote", out)
