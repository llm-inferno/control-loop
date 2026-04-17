#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl — Run 5.
Run 5: queue-analysis evaluator, unlimited=true, relaxed SLOs (granite 20ms, llama 60ms).
"""

import json
import sys
from datetime import datetime, timedelta, timezone
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import matplotlib.dates as mdates
import numpy as np

JSONL = '/tmp/inferno-cycles-run5.jsonl'
OUT   = '/Users/tantawi/Projects/llm-inferno/control-loop/docs/figs'

records = []
with open(JSONL) as f:
    for line in f:
        line = line.strip()
        if line:
            records.append(json.loads(line))

# ── helpers ──────────────────────────────────────────────────────────────────
def ts(r):
    return datetime.fromisoformat(r['ts'].replace('Z', '+00:00'))

times = [ts(r) for r in records]

def server_series(field, model):
    return [
        next((s[field] for s in r['servers'] if s['model'] == model), None)
        for r in records
    ]

def internal_series(model, acc, field):
    return [
        next((i[field] for i in r.get('internals', [])
              if i['model'] == model and i['acc'] == acc), None)
        for r in records
    ]

def phase_boundaries():
    """Phase offsets for Run 5: 6-min hold, 5-min ramp, 5-min hold, 5-min ramp, hold.
    First cycle is at ~t=3.4 min into the load emulator run (phase 4 confirmed at
    17:44:31 = first_cycle + 12.58 min = t=16 min → first_cycle at t=3.42 min).
    Offsets from first cycle: phase1_start=-3.42, p2=+2.58, p3=+7.58, p4=+12.58, p5=+17.58."""
    start = times[0]
    offsets_min = [-3.42, 2.58, 7.58, 12.58, 17.58]
    return [start + timedelta(minutes=m) for m in offsets_min]

phase_ts  = phase_boundaries()
phase_lbl = ['Ph1\nBaseline', 'Ph2\nRamp↑', 'Ph3\nHold\n5×', 'Ph4\nRamp↓', 'Ph5\nHold']

GRANITE_COLOR = '#1f77b4'
LLAMA_COLOR   = '#ff7f0e'

def add_phase_shading(ax):
    colors = ['#f0f0f0', '#d9ead3', '#fff2cc', '#d9ead3', '#f0f0f0']
    for i in range(len(phase_ts) - 1):
        ax.axvspan(phase_ts[i], phase_ts[i+1], alpha=0.35, color=colors[i], zorder=0)
    ax.set_xlim(times[0], times[-1] + timedelta(seconds=60))
    # Use mixed transform: x in data coords, y in axes fraction (0=bottom, 1=top).
    # Placing text at ymax*0.97 in data coords breaks on nearly-flat plots (e.g. EKF
    # alpha ≈ 8.000 ms) because ymax*0.97 ≈ 7.76 falls hundreds of axis-heights below
    # the visible range, causing bbox_inches='tight' to produce a ~200K-pixel-tall figure.
    trans = ax.get_xaxis_transform()
    for i, (t, lbl) in enumerate(zip(phase_ts, phase_lbl)):
        ax.axvline(t, color='#999999', lw=0.8, ls='--', zorder=1)
        ax.text(t, 0.97, lbl, fontsize=6, ha='left', va='top', color='#555555',
                transform=trans)

def fmt_xaxis(ax):
    ax.xaxis.set_major_formatter(mdates.DateFormatter('%H:%M', tz=timezone.utc))
    ax.xaxis.set_major_locator(mdates.MinuteLocator(byminute=range(0,60,2)))
    plt.setp(ax.xaxis.get_majorticklabels(), rotation=30, ha='right', fontsize=7)

# ── Figure 1: RPM + Replicas ──────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 5 — Load and Autoscaling Response (unlimited=true, relaxed SLOs)',
             fontweight='bold')

granite_rpm      = server_series('rpm',      'granite_8b')
llama_rpm        = server_series('rpm',      'llama_13b')
granite_replicas = server_series('replicas', 'granite_8b')
llama_replicas   = server_series('replicas', 'llama_13b')

ax1.plot(times, granite_rpm, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax1.plot(times, llama_rpm,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax1.set_ylabel('Arrival Rate (RPM)')
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.step(times, granite_replicas, where='post', color=GRANITE_COLOR, lw=1.5,
         label='granite_8b')
ax2.step(times, llama_replicas,   where='post', color=LLAMA_COLOR,   lw=1.5,
         label='llama_13b')
# mark the unlimited spike
peak_reps = max(r for r in granite_replicas if r is not None)
if peak_reps > 16:
    peak_idx = granite_replicas.index(peak_reps)
    ax2.annotate(f'unlimited spike\n({peak_reps} reps)',
                 xy=(times[peak_idx], peak_reps),
                 xytext=(times[peak_idx], peak_reps + 1.5),
                 fontsize=7, color='red',
                 arrowprops=dict(arrowstyle='->', color='red', lw=1))
ax2.axhline(16, color='red', lw=0.8, ls=':', alpha=0.5, label='H100 capacity (16)')
ax2.set_ylabel('Replica Count')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run5_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run5_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 5 — Latency vs SLO Targets', fontweight='bold')

granite_itl  = server_series('itl',  'granite_8b')
llama_itl    = server_series('itl',  'llama_13b')
granite_ttft = server_series('ttft', 'granite_8b')
llama_ttft   = server_series('ttft', 'llama_13b')

granite_slo_itl  = 20    # ms
llama_slo_itl    = 60    # ms
granite_slo_ttft = 200   # ms
llama_slo_ttft   = 1000  # ms

ax1.plot(times, granite_itl, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax1.plot(times, llama_itl,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax1.axhline(granite_slo_itl, color=GRANITE_COLOR, lw=1.2, ls='--',
            label=f'Premium ITL SLO ({granite_slo_itl}ms)')
ax1.axhline(llama_slo_itl, color=LLAMA_COLOR, lw=1.2, ls='--',
            label=f'Bronze ITL SLO ({llama_slo_itl}ms)')
ax1.set_ylabel('ITL (ms)')
ax1.legend(fontsize=7)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, granite_ttft, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax2.plot(times, llama_ttft,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax2.axhline(granite_slo_ttft, color=GRANITE_COLOR, lw=1.2, ls='--',
            label=f'Premium TTFT SLO ({granite_slo_ttft}ms)')
ax2.set_ylabel('TTFT (ms)')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=7)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run5_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run5_latency.png')

# ── Figure 3: EKF α convergence ───────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 5 — EKF α Parameter Convergence (H100 accelerator)', fontweight='bold')

granite_alpha = internal_series('granite_8b', 'H100', 'alpha')
llama_alpha   = internal_series('llama_13b',  'H100', 'alpha')

GRANITE_TARGET = 8.0   # ms
LLAMA_TARGET   = 12.0  # ms

ax1.plot(times, granite_alpha, color=GRANITE_COLOR, lw=1.5, label='granite_8b α (tuned)')
ax1.axhline(GRANITE_TARGET, color=GRANITE_COLOR, lw=1, ls=':',
            label=f'Target ({GRANITE_TARGET}ms)')
ax1.set_ylabel('α (ms)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_alpha, color=LLAMA_COLOR, lw=1.5, label='llama_13b α (tuned)')
ax2.axhline(LLAMA_TARGET, color=LLAMA_COLOR, lw=1, ls=':',
            label=f'Target ({LLAMA_TARGET}ms)')
ax2.set_ylabel('α (ms)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run5_ekf_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run5_ekf_alpha.png')

# ── Figure 4: Cycle timing ────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Run 5 — Control Cycle Timing Breakdown', fontweight='bold')

collect_ms  = [r['timing']['collectMs']  for r in records]
tune_ms     = [r['timing']['tuneMs']     for r in records]
optimize_ms = [r['timing']['optimizeMs'] for r in records]
actuate_ms  = [r['timing']['actuateMs']  for r in records]

ax.stackplot(times,
             collect_ms, tune_ms, optimize_ms, actuate_ms,
             labels=['collect', 'tune', 'optimize', 'actuate'],
             colors=['#4472C4', '#ED7D31', '#A9D18E', '#FF0000'],
             alpha=0.8)
ax.set_ylabel('Time (ms)')
ax.set_xlabel('Time (UTC)')
ax.legend(loc='upper right', fontsize=8)
ax.grid(True, alpha=0.3, axis='y')
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run5_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run5_timing.png')

# ── Figure 5: Total cost ──────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 3))
fig.suptitle('Run 5 — Total Allocation Cost Over Time', fontweight='bold')

total_cost = [r['totalCost'] for r in records]
ax.plot(times, total_cost, color='#7030A0', lw=1.5)
ax.fill_between(times, total_cost, alpha=0.2, color='#7030A0')
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Time (UTC)')
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run5_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run5_cost.png')

print('All Run 5 figures generated.')
