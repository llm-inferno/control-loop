#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl."""

import json
import sys
from datetime import datetime, timezone
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import matplotlib.dates as mdates
import numpy as np

HERE  = Path(__file__).parent
JSONL = HERE / 'inferno-cycles.jsonl'
OUT   = HERE / 'figs'

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
    """Return approximate UTC times for each phase transition."""
    start = times[0]
    offsets_min = [0, 2, 7, 9, 14]
    from datetime import timedelta
    return [start + timedelta(minutes=m) for m in offsets_min]

phase_ts  = phase_boundaries()
phase_lbl = ['Ph1\nBaseline', 'Ph2\nRamp↑', 'Ph3\nHold\n3×', 'Ph4\nRamp↓', 'Ph5\nHold']

GRANITE_COLOR = '#1f77b4'
LLAMA_COLOR   = '#ff7f0e'
SLO_COLOR     = '#d62728'
PHASE_COLOR   = '#cccccc'

def add_phase_shading(ax):
    colors = ['#f0f0f0', '#d9ead3', '#fff2cc', '#d9ead3', '#f0f0f0']
    for i in range(len(phase_ts) - 1):
        ax.axvspan(phase_ts[i], phase_ts[i+1], alpha=0.35, color=colors[i], zorder=0)
    for i, (t, lbl) in enumerate(zip(phase_ts, phase_lbl)):
        ax.axvline(t, color='#999999', lw=0.8, ls='--', zorder=1)
    ymin, ymax = ax.get_ylim()
    for i, (t, lbl) in enumerate(zip(phase_ts, phase_lbl)):
        ax.text(t, ymax * 0.97, lbl, fontsize=6, ha='left', va='top', color='#555555')

def fmt_xaxis(ax):
    ax.xaxis.set_major_formatter(mdates.DateFormatter('%H:%M', tz=timezone.utc))
    ax.xaxis.set_major_locator(mdates.MinuteLocator(byminute=range(0,60,2)))
    plt.setp(ax.xaxis.get_majorticklabels(), rotation=30, ha='right', fontsize=7)

# ── Figure 1: RPM + Replicas ──────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Load and Autoscaling Response', fontweight='bold')

granite_rpm      = server_series('rpm', 'granite_13b')
llama_rpm        = server_series('rpm', 'llama_13b')
granite_replicas = server_series('replicas', 'granite_13b')
llama_replicas   = server_series('replicas', 'llama_13b')

ax1.plot(times, granite_rpm, color=GRANITE_COLOR, lw=1.5, label='granite_13b (Bronze)')
ax1.plot(times, llama_rpm,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Premium)')
ax1.set_ylabel('Arrival Rate (RPM)')
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.step(times, granite_replicas, where='post', color=GRANITE_COLOR, lw=1.5, label='granite_13b')
ax2.step(times, llama_replicas,   where='post', color=LLAMA_COLOR,   lw=1.5, label='llama_13b')
ax2.set_ylabel('Replica Count')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/exp_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved exp_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Latency vs SLO Targets', fontweight='bold')

granite_itl  = server_series('itl',  'granite_13b')
llama_itl    = server_series('itl',  'llama_13b')
granite_ttft = server_series('ttft', 'granite_13b')
llama_ttft   = server_series('ttft', 'llama_13b')
granite_slo_itl  = next(s['sloItl']  for s in records[0]['servers'] if s['model']=='granite_13b')
llama_slo_itl    = next(s['sloItl']  for s in records[0]['servers'] if s['model']=='llama_13b')
granite_slo_ttft = next(s['sloTtft'] for s in records[0]['servers'] if s['model']=='granite_13b')
llama_slo_ttft   = next(s['sloTtft'] for s in records[0]['servers'] if s['model']=='llama_13b')

ax1.plot(times, granite_itl, color=GRANITE_COLOR, lw=1.5, label='granite_13b (Bronze)')
ax1.plot(times, llama_itl,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Premium)')
ax1.axhline(granite_slo_itl, color=GRANITE_COLOR, lw=1, ls=':', label=f'Bronze SLO ({granite_slo_itl}ms)')
ax1.axhline(llama_slo_itl,   color=LLAMA_COLOR,   lw=1, ls=':', label=f'Premium SLO ({llama_slo_itl}ms)')
ax1.set_ylabel('ITL (ms)')
ax1.legend(fontsize=7)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, granite_ttft, color=GRANITE_COLOR, lw=1.5, label='granite_13b (Bronze)')
ax2.plot(times, llama_ttft,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Premium)')
ax2.axhline(granite_slo_ttft, color=GRANITE_COLOR, lw=1, ls=':', label=f'Bronze SLO ({granite_slo_ttft}ms)')
ax2.axhline(llama_slo_ttft,   color=LLAMA_COLOR,   lw=1, ls=':', label=f'Premium SLO ({llama_slo_ttft}ms)')
ax2.set_ylabel('TTFT (ms)')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=7)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/exp_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved exp_latency.png')

# ── Figure 3: EKF α convergence ───────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('EKF α Parameter Convergence (G2 accelerator)', fontweight='bold')

granite_alpha = internal_series('granite_13b', 'G2', 'alpha')
llama_alpha   = internal_series('llama_13b',   'G2', 'alpha')

GRANITE_STATIC = 16.78  # from model-data.json (same for both in large/ dataset)
LLAMA_STATIC   = 16.78

ax1.plot(times, granite_alpha, color=GRANITE_COLOR, lw=1.5, label='granite_13b α (tuned)')
ax1.axhline(GRANITE_STATIC, color=GRANITE_COLOR, lw=1, ls=':', label=f'Static value ({GRANITE_STATIC})')
ax1.set_ylabel('α')
ax1.set_title('granite_13b / G2', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_alpha, color=LLAMA_COLOR, lw=1.5, label='llama_13b α (tuned)')
ax2.axhline(LLAMA_STATIC, color=LLAMA_COLOR, lw=1, ls=':', label=f'Static value ({LLAMA_STATIC})')
ax2.set_ylabel('α')
ax2.set_title('llama_13b / G2', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/exp_ekf_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved exp_ekf_alpha.png')

# ── Figure 4: Cycle timing ────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Control Cycle Timing Breakdown', fontweight='bold')

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
plt.savefig(f'{OUT}/exp_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved exp_timing.png')

# ── Figure 5: Total cost ──────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 3))
fig.suptitle('Total Allocation Cost Over Time', fontweight='bold')

total_cost = [r['totalCost'] for r in records]
ax.plot(times, total_cost, color='#7030A0', lw=1.5)
ax.fill_between(times, total_cost, alpha=0.2, color='#7030A0')
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Time (UTC)')
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/exp_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved exp_cost.png')

print('All figures generated.')
