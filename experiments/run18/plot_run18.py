#!/usr/bin/env python3
"""run18 figures: three-arm comparison + compact per-arm time series.

Backend: continuous-vllm-server evaluator (server-sim #24) + offered-load fix
(#26/#27), 90s trailing window, tuner OFF, 5x ramp on real Qwen2.5-14B / H100.

Outputs (experiments/run18/):
  run18-three-arm.png   - cross-arm: replicas / occ-per-replica / ITL vs offered RPM
  run18-armA.png        - per-arm compact 3-panel time series (search)
  run18-armBlow.png     - per-arm compact 3-panel time series (M*=32)
  run18-armBhigh.png    - per-arm compact 3-panel time series (M*=128)

Data-source note (see analyze.py): `rpm` is the emulator setpoint label (not a
window average); `throughput`/`occ` are per-pod window-averaged, summed/averaged
over replicas. ITL SLO (qwen Bronze) = 20 ms.
"""
import json, os
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

HERE = os.path.dirname(os.path.abspath(__file__))
ITL_SLO = 20.0
CAP_H100 = 8

ARMS = [
    ("A — search M*",   "armA-search-cycles.jsonl",  "#1f77b4", "o", None),
    ("B-low — M*=32",   "armB-low32-cycles.jsonl",   "#d62728", "s", 32),
    ("B-high — M*=128", "armB-high128-cycles.jsonl", "#2ca02c", "^", 128),
]

# Approximate shared phase boundaries by cycle (all arms ran the same 5-phase
# profile on a 120s period: baseline 10m, ramp 6m, hold 6m, ramp-down 4m, hold).
PHASES = [(1, "baseline 1x"), (6, "ramp↑"), (9, "hold 5x"), (12, "ramp↓"), (14, "hold 1x")]
PHASE_COLORS = ["#f0f0f0", "#d9ead3", "#fff2cc", "#d9ead3", "#f0f0f0"]


def load_rows(fn):
    p = os.path.join(HERE, fn)
    rows = []
    if not os.path.exists(p):
        return rows
    for line in open(p):
        line = line.strip()
        if not line:
            continue
        r = json.loads(line)
        s = (r.get("servers") or [{}])[0]
        rows.append({
            "cycle": r.get("cycle"), "rpm": s.get("rpm", 0), "thr": s.get("throughput", 0) or 0,
            "repl": s.get("replicas", 0), "M": s.get("maxBatch", 0),
            "occ": s.get("occPerReplica", 0), "itl": s.get("itl", 0), "ttft": s.get("ttft", 0),
        })
    return rows


def shade(ax, maxcyc):
    bounds = [p[0] for p in PHASES] + [maxcyc + 1]
    for i, (start, lbl) in enumerate(PHASES):
        ax.axvspan(bounds[i], bounds[i + 1], alpha=0.35, color=PHASE_COLORS[i], zorder=0)
        ax.axvline(start, color="#999999", lw=0.7, ls="--", zorder=1)
        ax.text(start + 0.1, 0.97, lbl, fontsize=6, ha="left", va="top",
                color="#555", transform=ax.get_xaxis_transform())
    ax.set_xlim(1, maxcyc + 1)


# ---------- Figure 1: three-arm comparison vs offered RPM ----------
fig, axes = plt.subplots(1, 3, figsize=(16, 4.6))
fig.suptitle("run18 — concurrency control A/B/B on real vLLM (Qwen2.5-14B / H100), "
             "continuous backend + offered-load fix, 90s window, tuner off, 5× ramp",
             fontsize=12, fontweight="bold")
for label, fn, color, mk, pin in ARMS:
    rows = load_rows(fn)
    if not rows:
        continue
    pts = sorted((r["rpm"], r["repl"], r["occ"], r["itl"]) for r in rows)
    rpm = [p[0] for p in pts]
    axes[0].plot(rpm, [p[1] for p in pts], marker=mk, color=color, label=label, ms=6, lw=1.4, alpha=0.85)
    axes[1].plot(rpm, [p[2] for p in pts], marker=mk, color=color, label=label, ms=6, lw=1.4, alpha=0.85)
    axes[2].plot(rpm, [p[3] for p in pts], marker=mk, color=color, label=label, ms=6, lw=1.4, alpha=0.85)
    if pin:
        axes[1].axhline(pin, color=color, ls=":", lw=1, alpha=0.5)
axes[0].set_title("Replicas allocated vs offered load")
axes[0].set_ylabel("replicas")
axes[0].axhline(CAP_H100, color="gray", ls="--", lw=1, alpha=0.6)
axes[0].text(300, CAP_H100 + 0.1, f"capacity cap = {CAP_H100} H100", fontsize=8, color="gray")
axes[1].set_title("In-service occupancy / replica\n(dotted = pinned M* ceiling)")
axes[1].set_ylabel("occ per replica")
axes[2].set_title("Attained ITL vs offered load")
axes[2].set_ylabel("ITL (ms)")
axes[2].axhline(ITL_SLO, color="black", ls="--", lw=1.2)
axes[2].text(300, ITL_SLO + 0.3, f"ITL SLO = {ITL_SLO:.0f} ms", fontsize=8)
for ax in axes:
    ax.set_xlabel("offered load (RPM)")
    ax.grid(True, alpha=0.3)
    ax.legend(fontsize=8, loc="best")
fig.tight_layout(rect=[0, 0, 1, 0.93])
fig.savefig(os.path.join(HERE, "run18-three-arm.png"), dpi=130)
plt.close(fig)
print("wrote run18-three-arm.png")


# ---------- Figures 2-4: compact per-arm time series ----------
def per_arm(label, fn, color, pin, outname):
    rows = load_rows(fn)
    if not rows:
        print("skip", outname, "(no data)")
        return
    c = [r["cycle"] for r in rows]
    maxcyc = max(c)
    fig, (a1, a2, a3) = plt.subplots(3, 1, figsize=(8.5, 7.2), sharex=True)
    fig.suptitle(f"run18 — Arm {label}  (Qwen2.5-14B / H100, 5× ramp)",
                 fontsize=12, fontweight="bold")

    # Panel 1: offered + throughput (left axis), replicas (right axis)
    a1.plot(c, [r["rpm"] for r in rows], color="#444", lw=1.6, marker=".", label="offered (RPM, setpoint)")
    a1.plot(c, [r["thr"] for r in rows], color=color, lw=1.6, marker=".", label="throughput (RPM, window-avg)")
    a1.set_ylabel("requests / min")
    a1.legend(fontsize=7, loc="upper left")
    a1r = a1.twinx()
    a1r.step(c, [r["repl"] for r in rows], color="#7030A0", lw=1.5, where="mid", label="replicas")
    a1r.set_ylabel("replicas", color="#7030A0")
    a1r.tick_params(axis="y", labelcolor="#7030A0")
    a1r.set_ylim(0, CAP_H100 + 0.5)
    a1r.axhline(CAP_H100, color="#7030A0", ls="--", lw=0.8, alpha=0.5)
    a1r.legend(fontsize=7, loc="upper right")
    shade(a1, maxcyc)

    # Panel 2: ITL + TTFT vs SLO
    a2.plot(c, [r["itl"] for r in rows], color=color, lw=1.6, marker=".", label="ITL")
    a2.axhline(ITL_SLO, color="black", ls="--", lw=1.1, label=f"ITL SLO {ITL_SLO:.0f} ms")
    a2.set_ylabel("ITL (ms)")
    a2.legend(fontsize=7, loc="upper left")
    a2t = a2.twinx()
    a2t.plot(c, [r["ttft"] for r in rows], color="#ff7f0e", lw=1.2, ls=":", marker=".", ms=3, label="TTFT")
    a2t.set_ylabel("TTFT (ms)", color="#ff7f0e")
    a2t.tick_params(axis="y", labelcolor="#ff7f0e")
    a2t.legend(fontsize=7, loc="upper right")
    shade(a2, maxcyc)

    # Panel 3: occupancy / replica vs the M* cap
    a3.plot(c, [r["occ"] for r in rows], color=color, lw=1.6, marker=".", label="occ / replica")
    if pin:
        a3.axhline(pin, color="black", ls=":", lw=1.2, label=f"M* cap = {pin}")
    else:
        a3.plot(c, [r["M"] for r in rows], color="black", ls=":", lw=1.2, marker=".", ms=3,
                label="searched M*")
    a3.set_ylabel("concurrency")
    a3.set_xlabel("control cycle")
    a3.legend(fontsize=7, loc="upper left")
    shade(a3, maxcyc)

    fig.tight_layout(rect=[0, 0, 1, 0.95])
    fig.savefig(os.path.join(HERE, outname), dpi=130)
    plt.close(fig)
    print("wrote", outname)


per_arm("A — search",   "armA-search-cycles.jsonl",  "#1f77b4", None, "run18-armA.png")
per_arm("B-low — M*=32",  "armB-low32-cycles.jsonl",   "#d62728", 32,  "run18-armBlow.png")
per_arm("B-high — M*=128","armB-high128-cycles.jsonl", "#2ca02c", 128, "run18-armBhigh.png")
print("done")
