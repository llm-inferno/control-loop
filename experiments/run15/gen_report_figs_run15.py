#!/usr/bin/env python3
"""Generate experiment report figures for Run 15 — concurrency-control A/B.

Arm A: optimal-concurrency search ON  (DEFAULT_MAX_BATCH_SIZE unset; optimizer searches M*).
Arm B: concurrency control OFF        (DEFAULT_MAX_BATCH_SIZE=256; concurrency pinned to 256).

Both arms: qa workload (granite_8b/H100/Premium + llama_13b/H100/Bronze), identical
1x->5x->1x load sweep, INFERNO_STARTUP_DELAY=45s, EKF learned from scratch.

The two arms ran at different wall-clock times but step through the same phase sequence
on the same 30s control period, and (verified) peak RPM lands at cycle 22 in both. So the
figures use the post-warm-up CYCLE NUMBER as the shared x-axis. Arm A is drawn solid,
Arm B dashed; granite is blue, llama orange.

M* (the pinned/searched concurrency) is not in the cycle log; it is scraped from the
deployment label into arm{A,B}-mstar.csv and plotted on its own elapsed-minutes axis.
"""

import csv
import json
from datetime import datetime, timezone
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

HERE = Path(__file__).parent
OUT  = HERE / 'figs'
OUT.mkdir(exist_ok=True)

GRANITE_COLOR = '#1f77b4'
LLAMA_COLOR   = '#ff7f0e'
ARM_STYLE = {'A': '-', 'B': '--'}
ARM_LABEL = {'A': 'Arm A (search M*)', 'B': 'Arm B (pinned 256)'}

# Phase boundaries by cycle (shared; both arms verified peak at cycle 22).
PHASES = [(1, 'Baseline\n1x'), (7, 'Ramp↑'), (16, 'Hold 5x'), (27, 'Ramp↓')]
PHASE_COLORS = ['#f0f0f0', '#d9ead3', '#fff2cc', '#d9ead3']


def load(arm):
    recs = []
    with open(HERE / f'arm{arm}-cycles.jsonl') as f:
        for line in f:
            line = line.strip()
            if line:
                recs.append(json.loads(line))
    return recs


ARMS = {'A': load('A'), 'B': load('B')}
MAXCYC = max(r['cycle'] for recs in ARMS.values() for r in recs)


def cyc(recs):
    return [r['cycle'] for r in recs]


def srv(recs, model, field):
    return [next((s[field] for s in r['servers'] if s['model'] == model), None) for r in recs]


def internal(recs, model, field):
    return [next((i.get(field) for i in r.get('internals', [])
                  if i['model'] == model and i['acc'] == 'H100'), None) for r in recs]


def add_phase_shading(ax):
    bounds = [p[0] for p in PHASES] + [MAXCYC + 1]
    for i, (start, lbl) in enumerate(PHASES):
        ax.axvspan(bounds[i], bounds[i + 1], alpha=0.35, color=PHASE_COLORS[i], zorder=0)
        ax.axvline(start, color='#999999', lw=0.8, ls='--', zorder=1)
        ax.text(start + 0.2, 0.97, lbl, fontsize=6, ha='left', va='top',
                color='#555555', transform=ax.get_xaxis_transform())
    ax.set_xlim(1, MAXCYC + 1)


def plot_both(ax, field, slo_g=None, slo_l=None, clip=None):
    """Plot granite+llama for both arms; optional SLO lines and clip ceiling."""
    for arm, recs in ARMS.items():
        x = cyc(recs)
        for model, color in (('granite_8b', GRANITE_COLOR), ('llama_13b', LLAMA_COLOR)):
            y = srv(recs, model, field)
            if clip is not None:
                y = [min(v, clip) if v is not None else None for v in y]
            short = 'granite' if 'granite' in model else 'llama'
            ax.plot(x, y, color=color, ls=ARM_STYLE[arm], lw=1.4,
                    label=f'{short} · {ARM_LABEL[arm]}')
    ax.grid(True, alpha=0.3)
    add_phase_shading(ax)


# Figure 1: RPM + Replicas
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Load and Autoscaling Response — Arm A (search) vs Arm B (pinned 256)',
             fontweight='bold')
plot_both(ax1, 'rpm')
ax1.set_ylabel('Arrival Rate (RPM)')
ax1.legend(fontsize=6, ncol=2, loc='upper left')
plot_both(ax2, 'replicas')
ax2.axhline(16, color='red', lw=0.8, ls=':', alpha=0.6, label='H100 capacity (16)')
ax2.set_ylabel('Replica Count')
ax2.set_xlabel('Control Cycle (post warm-up)')
ax2.legend(fontsize=6, ncol=2, loc='upper left')
plt.tight_layout()
plt.savefig(f'{OUT}/run15_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run15_load_replicas.png')

# Figure 2: ITL + TTFT vs SLO  (KEY)
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Latency vs SLO — the concurrency tradeoff (A favors ITL, B favors TTFT)',
             fontweight='bold')
plot_both(ax1, 'itl')
ax1.axhline(30, color=GRANITE_COLOR, lw=1, ls=':', label='Premium ITL SLO (30ms)')
ax1.axhline(60, color=LLAMA_COLOR, lw=1, ls=':', label='Bronze ITL SLO (60ms)')
ax1.set_ylabel('ITL (ms)')
ax1.legend(fontsize=6, ncol=2, loc='upper left')

CLIP = 2000
plot_both(ax2, 'ttft', clip=CLIP)
ax2.axhline(200, color=GRANITE_COLOR, lw=1, ls=':', label='Premium TTFT SLO (200ms)')
ax2.axhline(1000, color=LLAMA_COLOR, lw=1, ls=':', label='Bronze TTFT SLO (1000ms)')
for arm, recs in ARMS.items():
    for model, color in (('granite_8b', GRANITE_COLOR), ('llama_13b', LLAMA_COLOR)):
        for c, v in zip(cyc(recs), srv(recs, model, 'ttft')):
            if v is not None and v > CLIP:
                ax2.annotate(f'{v/1000:.1f}k', xy=(c, CLIP), fontsize=5, color=color,
                             ha='center', va='bottom', rotation=45)
ax2.set_ylabel(f'TTFT (ms, clipped {CLIP})')
ax2.set_xlabel('Control Cycle (post warm-up)')
ax2.legend(fontsize=6, ncol=2, loc='upper left')
plt.tight_layout()
plt.savefig(f'{OUT}/run15_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run15_latency.png')

# Figure 3: Concurrency M* (from CSV, elapsed-minutes axis)  (KEY headline)
def load_mstar(arm):
    rows = {'qa-granite-8b': [], 'qa-llama-13b': []}
    with open(HERE / f'arm{arm}-mstar.csv') as f:
        for row in csv.DictReader(f):
            if not row.get('maxbatchsize'):
                continue
            t = datetime.fromisoformat(row['ts'].replace('Z', '+00:00'))
            rows.setdefault(row['deployment'], []).append((t, int(row['maxbatchsize'])))
    out = {}
    for dep, pts in rows.items():
        if not pts:
            continue
        t0 = pts[0][0]
        out[dep] = ([(t - t0).total_seconds() / 60 for t, _ in pts], [m for _, m in pts])
    return out


fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Concurrency M* — searched (Arm A) vs pinned 256 (Arm B)', fontweight='bold')
for arm in ('A', 'B'):
    ms = load_mstar(arm)
    if 'qa-granite-8b' in ms:
        x, y = ms['qa-granite-8b']
        ax1.plot(x, y, color=GRANITE_COLOR, ls=ARM_STYLE[arm], lw=1.5, label=ARM_LABEL[arm])
    if 'qa-llama-13b' in ms:
        x, y = ms['qa-llama-13b']
        ax2.plot(x, y, color=LLAMA_COLOR, ls=ARM_STYLE[arm], lw=1.5, label=ARM_LABEL[arm])
ax1.set_ylabel('granite M* (max batch)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
ax2.set_ylabel('llama M* (max batch)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Elapsed minutes since first allocation')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
plt.tight_layout()
plt.savefig(f'{OUT}/run15_concurrency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run15_concurrency.png')

# Figure 4: Total cost
fig, ax = plt.subplots(figsize=(10, 3.5))
fig.suptitle('Total Allocation Cost — Arm A vs Arm B', fontweight='bold')
for arm, recs in ARMS.items():
    ax.plot(cyc(recs), [r['totalCost'] for r in recs], color='#7030A0',
            ls=ARM_STYLE[arm], lw=1.6, label=ARM_LABEL[arm])
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Control Cycle (post warm-up)')
ax.legend(fontsize=8)
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
plt.tight_layout()
plt.savefig(f'{OUT}/run15_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run15_cost.png')

# Figure 5: EKF alpha/beta/gamma (validates both arms learned identical params)
fig, axes = plt.subplots(3, 1, figsize=(10, 8), sharex=True)
fig.suptitle('EKF Parameter Convergence — both arms learn the same server physics',
             fontweight='bold')
params = [('alpha', 'α (ms)', 8.0, 12.0),
          ('beta', 'β (ms/tok)', 0.016, 0.024),
          ('gamma', 'γ (ms/tok²)', 0.0005, 0.00075)]
for ax, (field, ylab, tg, tl) in zip(axes, params):
    for arm, recs in ARMS.items():
        ax.plot(cyc(recs), internal(recs, 'granite_8b', field), color=GRANITE_COLOR,
                ls=ARM_STYLE[arm], lw=1.3, label=f'granite · {ARM_LABEL[arm]}')
        ax.plot(cyc(recs), internal(recs, 'llama_13b', field), color=LLAMA_COLOR,
                ls=ARM_STYLE[arm], lw=1.3, label=f'llama · {ARM_LABEL[arm]}')
    ax.axhline(tg, color=GRANITE_COLOR, lw=0.8, ls=':', alpha=0.7)
    ax.axhline(tl, color=LLAMA_COLOR, lw=0.8, ls=':', alpha=0.7)
    ax.set_ylabel(ylab)
    ax.grid(True, alpha=0.3)
    add_phase_shading(ax)
axes[0].legend(fontsize=6, ncol=2, loc='upper right')
axes[-1].set_xlabel('Control Cycle (post warm-up)')
plt.tight_layout()
plt.savefig(f'{OUT}/run15_ekf.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run15_ekf.png')

# Figure 6: Cycle timing (collect + tune), from the cycle log
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Control Cycle Timing — collect & tune (Arm A vs Arm B)', fontweight='bold')
for arm, recs in ARMS.items():
    ax.plot(cyc(recs), [r['timing']['collectMs'] for r in recs], color='#4472C4',
            ls=ARM_STYLE[arm], lw=1.5, label=f'collect · {ARM_LABEL[arm]}')
    ax.plot(cyc(recs), [r['timing']['tuneMs'] for r in recs], color='#ED7D31',
            ls=ARM_STYLE[arm], lw=1.3, label=f'tune · {ARM_LABEL[arm]}')
ax.set_ylabel('Time (ms)')
ax.set_xlabel('Control Cycle (post warm-up)')
ax.legend(fontsize=7, ncol=2, loc='upper left')
ax.grid(True, alpha=0.3, axis='y')
add_phase_shading(ax)
plt.tight_layout()
plt.savefig(f'{OUT}/run15_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run15_timing.png')

# Figure 7: APPROXIMATE per-replica average concurrency (Little's Law).
# L = lambda * W; lambda = rpm/60 (req/s); W = residence time (s) ~= TTFT + outTok*ITL.
# per-replica concurrency = L / replicas. Approximate: W uses logged TTFT (incl. queue
# wait) + decode time; it is a back-of-envelope estimate, NOT a measured in-flight count.
# Contrast against the CEILING: Arm A searched M* (~34 granite / ~70 llama), Arm B pinned 256.
def avg_concurrency(recs, model):
    out = []
    for r in recs:
        s = next((s for s in r['servers'] if s['model'] == model), None)
        if not s or not s.get('replicas'):
            out.append(None)
            continue
        w_sec = s['ttft'] / 1000.0 + s['avgOutTok'] * s['itl'] / 1000.0
        l_total = (s['rpm'] / 60.0) * w_sec
        out.append(l_total / s['replicas'])
    return out


fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Approx. per-replica request concurrency (Little\'s Law) vs ceiling\n'
             '— back-of-envelope from logged rpm/TTFT/ITL/outTok',
             fontweight='bold')
for arm, recs in ARMS.items():
    ax1.plot(cyc(recs), avg_concurrency(recs, 'granite_8b'), color=GRANITE_COLOR,
             ls=ARM_STYLE[arm], lw=1.5, label=ARM_LABEL[arm])
    ax2.plot(cyc(recs), avg_concurrency(recs, 'llama_13b'), color=LLAMA_COLOR,
             ls=ARM_STYLE[arm], lw=1.5, label=ARM_LABEL[arm])
ax1.set_ylabel('granite conc. (req/replica)')
ax1.set_title('granite_8b / H100  (ceilings: Arm A M*≈34, Arm B 256)', fontsize=9)
ax1.legend(fontsize=7)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)
ax2.set_ylabel('llama conc. (req/replica)')
ax2.set_title('llama_13b / H100  (ceilings: Arm A M*≈70, Arm B 256)', fontsize=9)
ax2.set_xlabel('Control Cycle (post warm-up)')
ax2.legend(fontsize=7)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
plt.tight_layout()
plt.savefig(f'{OUT}/run15_concurrency_actual.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run15_concurrency_actual.png')

print('All Run 15 figures generated.')
