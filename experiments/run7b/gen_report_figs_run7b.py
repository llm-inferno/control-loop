#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl — Run 7b.
Run 7b: queue-analysis evaluator, errorLevel=0.2, percentChange=0.10,
        expectedObservations=[1000,100], granite init=4 replicas, noise=2%.
"""

import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import matplotlib.dates as mdates

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
    # Run 7b: load emulator started ~15:26 UTC (inferno pod started at 15:26:48)
    # Phase 1 (6 min hold), phase 2-4 (5 min each), phase 5 (forever)
    start = times[0]
    offsets_min = [-3.8, 2.2, 7.2, 12.2, 17.2]
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
    ax.xaxis.set_major_locator(mdates.MinuteLocator(byminute=range(0, 60, 2)))
    plt.setp(ax.xaxis.get_majorticklabels(), rotation=30, ha='right', fontsize=7)

# ── Figure 1: RPM + Replicas ──────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 7b — Load and Autoscaling Response\n'
             '(errorLevel=0.2, percentChange=0.10, granite init=4, noise=2%)',
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
plt.savefig(f'{OUT}/run7b_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run7b_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 7b — Latency vs SLO Targets', fontweight='bold')

granite_itl  = server_series('itl',  'granite_8b')
llama_itl    = server_series('itl',  'llama_13b')
granite_ttft = server_series('ttft', 'granite_8b')
llama_ttft   = server_series('ttft', 'llama_13b')

ax1.plot(times, granite_itl, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax1.plot(times, llama_itl,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax1.axhline(20,  color=GRANITE_COLOR, lw=1.2, ls='--', label='Premium ITL SLO (20ms)')
ax1.axhline(60,  color=LLAMA_COLOR,   lw=1.2, ls='--', label='Bronze ITL SLO (60ms)')
ax1.set_ylabel('ITL (ms)')
ax1.legend(fontsize=7)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, granite_ttft, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax2.plot(times, llama_ttft,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax2.axhline(200, color=GRANITE_COLOR, lw=1.2, ls='--', label='Premium TTFT SLO (200ms)')
ax2.set_ylabel('TTFT (ms)')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=7)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run7b_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run7b_latency.png')

# ── Figure 3: EKF α convergence ───────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 7b — EKF α Parameter Convergence (H100 accelerator)', fontweight='bold')

granite_alpha = internal_series('granite_8b', 'H100', 'alpha')
llama_alpha   = internal_series('llama_13b',  'H100', 'alpha')

ax1.plot(times, granite_alpha, color=GRANITE_COLOR, lw=1.5, label='granite_8b α (tuned)')
ax1.axhline(8.0,  color=GRANITE_COLOR, lw=1, ls=':', label='Target (8.0ms)')
ax1.set_ylabel('α (ms)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_alpha, color=LLAMA_COLOR, lw=1.5, label='llama_13b α (tuned)')
ax2.axhline(12.0, color=LLAMA_COLOR, lw=1, ls=':', label='Target (12.0ms)')
ax2.set_ylabel('α (ms)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run7b_ekf_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run7b_ekf_alpha.png')

# ── Figure 4: Cycle timing (from controller logs — cycle JSON timing always 0) ──
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Run 7b — Collect Timing (from controller logs)', fontweight='bold')

# Timing from controller stdout; cycle JSON timing field is always 0 (open issue)
collect_ms = [
    811, 73, 72, 72, 417, 57, 56, 62, 58, 413,
    1616, 2025, 2019, 2007, 2016, 3218, 3215, 3212, 4014, 4015,
    4020, 4018, 4023, 4018, 4021, 4016, 4021, 4011, 4024, 4012,
    4015, 4019, 2410, 2416, 822, 422, 417, 413, 413, 424,
    421, 55,
]

ax.plot(times[:len(collect_ms)], collect_ms, color='#4472C4', lw=1.5, label='collect (ms)')
ax.fill_between(times[:len(collect_ms)], collect_ms, alpha=0.3, color='#4472C4')
ax.set_ylabel('Collect Time (ms)')
ax.set_xlabel('Time (UTC)')
ax.legend(loc='upper right', fontsize=8)
ax.grid(True, alpha=0.3, axis='y')
add_phase_shading(ax)
fmt_xaxis(ax)
ax.text(0.99, 0.95,
        'Note: timing JSON field is 0 (open issue); values from controller stdout',
        transform=ax.transAxes, fontsize=6, ha='right', va='top', color='#888888')

plt.tight_layout()
plt.savefig(f'{OUT}/run7b_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run7b_timing.png')

# ── Figure 5: Total cost ──────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 3))
fig.suptitle('Run 7b — Total Allocation Cost Over Time', fontweight='bold')

total_cost = [r['totalCost'] for r in records]
ax.plot(times, total_cost, color='#7030A0', lw=1.5)
ax.fill_between(times, total_cost, alpha=0.2, color='#7030A0')
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Time (UTC)')
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run7b_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run7b_cost.png')

# ── Figure 6: EKF NIS ─────────────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 7b — EKF NIS (Normalized Innovation Squared)\n'
             'Lower = better filter health; >7.378 indicates rejected updates',
             fontweight='bold')

granite_nis = internal_series('granite_8b', 'H100', 'nis')
llama_nis   = internal_series('llama_13b',  'H100', 'nis')

ax1.semilogy(times, [v if v and v > 0 else float('nan') for v in granite_nis],
             color=GRANITE_COLOR, lw=1.5, label='granite_8b NIS')
ax1.axhline(7.378, color='red', lw=0.8, ls='--', alpha=0.7,
            label='Reject threshold (7.378, χ² 97.5%)')
ax1.set_ylabel('NIS (log scale)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.semilogy(times, [v if v and v > 0 else float('nan') for v in llama_nis],
             color=LLAMA_COLOR, lw=1.5, label='llama_13b NIS')
ax2.axhline(7.378, color='red', lw=0.8, ls='--', alpha=0.7,
            label='Reject threshold (7.378, χ² 97.5%)')
ax2.set_ylabel('NIS (log scale)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run7b_nis.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run7b_nis.png')

print('All Run 7b figures generated.')
