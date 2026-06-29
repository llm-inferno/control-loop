#!/usr/bin/env python3
"""run19 figures.

Part A  (run19-autoscaling.png)   blis autoscaling: replicas/throughput, ITL/TTFT, occ + searched M*.
Part B  (run19-vs-run18.png)      blis (run19) vs real-vLLM (run18 arm A) 2x2 overlay.
Bonus   (run19-arrival-fix.png)   before/after the partial-reporting arrival-undercount fix (PR #59):
                                  the 5<->3 peak oscillation and arrival collapse vs the clean rerun.

Backend: on-demand /latest blis trained-physics evaluator (server-sim #37/#38), NO_TUNER (seeded
run16 alpha/beta/gamma), pass-through saturation, M* search ON, 120s period, capacity H100=8, 5x ramp.
Schema matches run18 (rpm, throughput, replicas, maxBatch, occPerReplica, itl, ttft); ITL SLO = 20 ms,
TTFT SLO = 1500 ms (qwen_2_5_14b Bronze).
"""
import json, os
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

HERE = os.path.dirname(os.path.abspath(__file__))
FIGS = os.path.join(HERE, "figs"); os.makedirs(FIGS, exist_ok=True)
ITL_SLO = 20.0
TTFT_SLO = 1500.0
CAP_H100 = 8

# run19 ran the 5-phase profile on a 120s period; map phase starts to cycle index
# from the captured trajectory (baseline 1x, ramp, hold 5x, ramp-down, hold 1x).
PHASES = [(1, "baseline 1x"), (6, "ramp↑"), (8, "hold 5x"), (12, "ramp↓"), (14, "hold 1x")]
PHASE_COLORS = ["#f0f0f0", "#d9ead3", "#fff2cc", "#d9ead3", "#f0f0f0"]


def load_rows(path):
    rows = []
    if not os.path.exists(path):
        return rows
    for line in open(path):
        line = line.strip()
        if not line:
            continue
        r = json.loads(line)
        s = (r.get("servers") or [{}])[0]
        rows.append({
            "cycle": r.get("cycle"), "rpm": s.get("rpm", 0) or 0, "thr": s.get("throughput", 0) or 0,
            "repl": s.get("replicas", 0) or 0, "M": s.get("maxBatch", 0) or 0,
            "occ": s.get("occPerReplica", 0) or 0, "itl": s.get("itl", 0) or 0,
            "ttft": s.get("ttft", 0) or 0,
        })
    return rows


def shade(ax, maxcyc, phases=PHASES):
    bounds = [p[0] for p in phases] + [maxcyc + 1]
    for i, (start, lbl) in enumerate(phases):
        ax.axvspan(bounds[i], bounds[i + 1], alpha=0.35, color=PHASE_COLORS[i], zorder=0)
        ax.axvline(start, color="#999999", lw=0.7, ls="--", zorder=1)
        ax.text(start + 0.1, 0.97, lbl, fontsize=6, ha="left", va="top",
                color="#555", transform=ax.get_xaxis_transform())
    ax.set_xlim(1, maxcyc + 1)


run19 = load_rows(os.path.join(HERE, "armA-search-cycles.jsonl"))
run18 = load_rows(os.path.join(HERE, "..", "run18", "armA-search-cycles.jsonl"))
before = load_rows(os.path.join(HERE, "before-arrival-fix", "armA-search-cycles.jsonl"))

# ---------- Part A: run19 autoscaling time series ----------
c = [r["cycle"] for r in run19]
maxcyc = max(c)
fig, (a1, a2, a3) = plt.subplots(3, 1, figsize=(9, 8), sharex=True)
fig.suptitle("run19 Part A — autoscaling with the blis simulator "
             "(qwen_2_5_14b / H100 / Bronze, 5× ramp)", fontsize=12, fontweight="bold")

a1.plot(c, [r["rpm"] for r in run19], color="#444", lw=1.6, marker=".", label="arrival (RPM)")
a1.plot(c, [r["thr"] for r in run19], color="#1f77b4", lw=1.6, marker=".", label="throughput (RPM)")
a1.set_ylabel("requests / min")
a1.legend(fontsize=7, loc="upper left")
a1r = a1.twinx()
a1r.step(c, [r["repl"] for r in run19], color="#7030A0", lw=1.8, where="mid", label="replicas")
a1r.set_ylabel("replicas", color="#7030A0")
a1r.tick_params(axis="y", labelcolor="#7030A0")
a1r.set_ylim(0, CAP_H100 + 0.5)
a1r.axhline(CAP_H100, color="#7030A0", ls="--", lw=0.8, alpha=0.5)
a1r.text(1.2, CAP_H100 - 0.5, f"cap = {CAP_H100}", fontsize=7, color="#7030A0")
a1r.legend(fontsize=7, loc="upper right")
shade(a1, maxcyc)

a2.plot(c, [r["itl"] for r in run19], color="#1f77b4", lw=1.6, marker=".", label="ITL")
a2.axhline(ITL_SLO, color="black", ls="--", lw=1.1, label=f"ITL SLO {ITL_SLO:.0f} ms")
a2.set_ylabel("ITL (ms)")
a2.legend(fontsize=7, loc="upper left")
a2t = a2.twinx()
a2t.plot(c, [r["ttft"] for r in run19], color="#ff7f0e", lw=1.2, ls=":", marker=".", ms=3, label="TTFT")
a2t.axhline(TTFT_SLO, color="#ff7f0e", ls="--", lw=0.8, alpha=0.6)
a2t.set_ylabel("TTFT (ms, log)", color="#ff7f0e")
a2t.set_yscale("log")
a2t.tick_params(axis="y", labelcolor="#ff7f0e")
a2t.legend(fontsize=7, loc="upper right")
shade(a2, maxcyc)

a3.plot(c, [r["occ"] for r in run19], color="#1f77b4", lw=1.6, marker=".", label="occ / replica")
a3.plot(c, [r["M"] for r in run19], color="black", ls=":", lw=1.2, marker=".", ms=3, label="searched M*")
a3.set_ylabel("concurrency")
a3.set_xlabel("control cycle")
a3.legend(fontsize=7, loc="upper left")
shade(a3, maxcyc)

fig.tight_layout(rect=[0, 0, 1, 0.95])
fig.savefig(os.path.join(FIGS, "run19-autoscaling.png"), dpi=130)
plt.close(fig)
print("wrote figs/run19-autoscaling.png")

# ---------- Part B: run19 (blis) vs run18 arm A (real vLLM) ----------
fig, axes = plt.subplots(2, 2, figsize=(13, 8))
fig.suptitle("run19 Part B — blis simulator (run19) vs real vLLM (run18 arm A), "
             "tuner OFF ⇒ shared queueing model drives allocation", fontsize=12, fontweight="bold")
series = [
    (axes[0][0], "repl", "replicas", False),
    (axes[0][1], "itl", "ITL (ms)", False),
    (axes[1][0], "ttft", "TTFT (ms, log)", True),
    (axes[1][1], "thr", "throughput (RPM)", False),
]
for ax, key, ylabel, logy in series:
    ax.plot([r["cycle"] for r in run19], [r[key] for r in run19],
            color="#1f77b4", lw=1.6, marker="o", ms=4, label="run19 blis")
    ax.plot([r["cycle"] for r in run18], [r[key] for r in run18],
            color="#2ca02c", lw=1.6, marker="^", ms=4, label="run18 real vLLM")
    ax.set_ylabel(ylabel); ax.set_xlabel("control cycle")
    ax.grid(True, alpha=0.3); ax.legend(fontsize=8, loc="best")
    if logy:
        ax.set_yscale("log")
axes[0][0].axhline(CAP_H100, color="gray", ls="--", lw=1, alpha=0.6)
axes[0][1].axhline(ITL_SLO, color="black", ls="--", lw=1, label=f"ITL SLO {ITL_SLO:.0f} ms")
axes[1][0].axhline(TTFT_SLO, color="black", ls="--", lw=1, label=f"TTFT SLO {TTFT_SLO:.0f} ms")
fig.tight_layout(rect=[0, 0, 1, 0.94])
fig.savefig(os.path.join(FIGS, "run19-vs-run18.png"), dpi=130)
plt.close(fig)
print("wrote figs/run19-vs-run18.png")

# ---------- Bonus: before/after the arrival-undercount fix (PR #59) ----------
if before:
    fig, (b1, b2) = plt.subplots(2, 1, figsize=(9, 7), sharex=True)
    fig.suptitle("run19 — partial-reporting arrival-undercount fix (PR #59): "
                 "before vs after at the 5× peak", fontsize=12, fontweight="bold")
    # Replicas
    b1.step([r["cycle"] for r in before], [r["repl"] for r in before],
            color="#d62728", lw=1.8, where="mid", marker="s", ms=4, label="before (5↔ 3 oscillation)")
    b1.step([r["cycle"] for r in run19], [r["repl"] for r in run19],
            color="#1f77b4", lw=1.8, where="mid", marker="o", ms=4, label="after (monotonic 5→6)")
    b1.axhline(CAP_H100, color="gray", ls="--", lw=1, alpha=0.6)
    b1.set_ylabel("replicas"); b1.legend(fontsize=8, loc="upper right"); b1.grid(True, alpha=0.3)
    # Arrival (rpm) — before collapses to ~600 at peak (under-count); after holds the setpoint
    b2.plot([r["cycle"] for r in before], [r["rpm"] for r in before],
            color="#d62728", lw=1.6, marker="s", ms=4, label="before: arrival (collapses ~600)")
    b2.plot([r["cycle"] for r in run19], [r["rpm"] for r in run19],
            color="#1f77b4", lw=1.6, marker="o", ms=4, label="after: arrival (tracks setpoint)")
    b2.set_ylabel("arrival (RPM)"); b2.set_xlabel("control cycle")
    b2.legend(fontsize=8, loc="upper right"); b2.grid(True, alpha=0.3)
    fig.tight_layout(rect=[0, 0, 1, 0.95])
    fig.savefig(os.path.join(FIGS, "run19-arrival-fix.png"), dpi=130)
    plt.close(fig)
    print("wrote figs/run19-arrival-fix.png")
else:
    print("skip run19-arrival-fix.png (no before-arrival-fix archive)")
print("done")
