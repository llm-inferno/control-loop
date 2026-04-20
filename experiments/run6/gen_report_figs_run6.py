#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl — Run 6.
Run 6: queue-analysis evaluator, unlimited=true, relaxed SLOs (granite 20ms ITL, llama 60ms ITL),
       maxBatchSize=64, maxQueueSize=128, granite initial replicas=4, noise=2%.
"""

import json
import sys
from datetime import datetime, timedelta, timezone
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
        next((i.get(field) for i in r.get('internals', [])
              if i['model'] == model and i['acc'] == acc), None)
        for r in records
    ]

def phase_boundaries():
    """Phase offsets for Run 6: same 5-phase load profile as runs 4/5.
    Phases: 6-min hold, 5-min ramp, 5-min hold, 5-min ramp, hold forever.
    Adjust offsets_min based on actual first-cycle time relative to load emulator start."""
    start = times[0]
    # Default offsets from first cycle (update after experiment if start time differs)
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
fig.suptitle('Run 6 — Load and Autoscaling Response\n'
             '(maxBatchSize=64, maxQueueSize=128, granite init=4 replicas, noise=2%)',
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
peak_reps = max((r for r in granite_replicas if r is not None), default=0)
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
plt.savefig(f'{OUT}/run6_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run6_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 6 — Latency vs SLO Targets', fontweight='bold')

granite_itl  = server_series('itl',  'granite_8b')
llama_itl    = server_series('itl',  'llama_13b')
granite_ttft = server_series('ttft', 'granite_8b')
llama_ttft   = server_series('ttft', 'llama_13b')

granite_slo_itl  = 20    # ms (same as run5)
llama_slo_itl    = 60    # ms (same as run5)
granite_slo_ttft = 200   # ms (same as run5)
llama_slo_ttft   = 1000  # ms (same as run5)

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
plt.savefig(f'{OUT}/run6_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run6_latency.png')

# ── Figure 3: EKF α convergence ───────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 6 — EKF α Parameter Convergence (H100 accelerator)', fontweight='bold')

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
plt.savefig(f'{OUT}/run6_ekf_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run6_ekf_alpha.png')

# ── Figure 4: Cycle timing ────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Run 6 — Control Cycle Timing Breakdown', fontweight='bold')

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
plt.savefig(f'{OUT}/run6_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run6_timing.png')

# ── Figure 5: Total cost ──────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 3))
fig.suptitle('Run 6 — Total Allocation Cost Over Time', fontweight='bold')

total_cost = [r['totalCost'] for r in records]
ax.plot(times, total_cost, color='#7030A0', lw=1.5)
ax.fill_between(times, total_cost, alpha=0.2, color='#7030A0')
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Time (UTC)')
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run6_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run6_cost.png')

# ── Figure 6: EKF NIS (noise robustness check) ───────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 6 — EKF NIS (Normalized Innovation Squared)\n'
             'Lower = better filter health; >1 indicates rejected updates',
             fontweight='bold')

granite_nis = internal_series('granite_8b', 'H100', 'nis')
llama_nis   = internal_series('llama_13b',  'H100', 'nis')

ax1.semilogy(times, [v if v and v > 0 else float('nan') for v in granite_nis],
             color=GRANITE_COLOR, lw=1.5, label='granite_8b NIS')
ax1.axhline(1.0, color='red', lw=0.8, ls='--', alpha=0.6, label='Reject threshold (~1.0)')
ax1.set_ylabel('NIS (log scale)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.semilogy(times, [v if v and v > 0 else float('nan') for v in llama_nis],
             color=LLAMA_COLOR, lw=1.5, label='llama_13b NIS')
ax2.axhline(1.0, color='red', lw=0.8, ls='--', alpha=0.6, label='Reject threshold (~1.0)')
ax2.set_ylabel('NIS (log scale)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run6_nis.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run6_nis.png')

print('All Run 6 figures generated.')
