#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl — Run 9.
Run 9: queue-analysis evaluator, sliding-window Nelder-Mead estimator (fixed),
       windowSize=10, minObs=3, residualThreshold=0.5, initObs=3.
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
OUT.mkdir(exist_ok=True)

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
    # Load emulator phase transitions (from load-emulator logs):
    # Phase 1 entered: 11:33:34 UTC (before first cycle at 11:36:04)
    # Phase 2 entered: 11:39:37 UTC
    # Phase 3 entered: 11:44:40 UTC
    # Phase 4 entered: 11:49:40 UTC
    # Phase 5 entered: 11:54:43 UTC
    def utc(h, m, s):
        return datetime(2026, 4, 21, h, m, s, tzinfo=timezone.utc)
    return [
        utc(11, 33, 34),
        utc(11, 39, 37),
        utc(11, 44, 40),
        utc(11, 49, 40),
        utc(11, 54, 43),
    ]

phase_ts  = phase_boundaries()
phase_lbl = ['Ph1\nBaseline', 'Ph2\nRamp↑', 'Ph3\nHold\n5×', 'Ph4\nRamp↓', 'Ph5\nHold']

GRANITE_COLOR = '#1f77b4'
LLAMA_COLOR   = '#ff7f0e'

def add_phase_shading(ax):
    colors = ['#f0f0f0', '#d9ead3', '#fff2cc', '#d9ead3', '#f0f0f0']
    xlim_end = times[-1] + timedelta(seconds=60)
    boundaries = phase_ts + [xlim_end]
    for i in range(len(phase_ts)):
        ax.axvspan(boundaries[i], boundaries[i+1], alpha=0.35, color=colors[i], zorder=0)
    ax.set_xlim(times[0], xlim_end)
    trans = ax.get_xaxis_transform()
    for t, lbl in zip(phase_ts, phase_lbl):
        ax.axvline(t, color='#999999', lw=0.8, ls='--', zorder=1)
        ax.text(t, 0.97, lbl, fontsize=6, ha='left', va='top', color='#555555',
                transform=trans)

def fmt_xaxis(ax):
    ax.xaxis.set_major_formatter(mdates.DateFormatter('%H:%M', tz=timezone.utc))
    ax.xaxis.set_major_locator(mdates.MinuteLocator(byminute=range(0, 60, 2)))
    plt.setp(ax.xaxis.get_majorticklabels(), rotation=30, ha='right', fontsize=7)

# ── Figure 1: RPM + Replicas ──────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 9 — Load and Autoscaling Response\n'
             '(sliding-window Nelder-Mead, windowSize=10, minObs=3, fixed warm-start)',
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
ax2.axhline(16, color='red', lw=0.8, ls=':', alpha=0.5, label='H100 capacity (16)')
ax2.set_ylabel('Replica Count')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run9_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run9_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 9 — Latency vs SLO Targets', fontweight='bold')

granite_itl  = server_series('itl',  'granite_8b')
llama_itl    = server_series('itl',  'llama_13b')
granite_ttft = server_series('ttft', 'granite_8b')
llama_ttft   = server_series('ttft', 'llama_13b')

ax1.plot(times, granite_itl, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax1.plot(times, llama_itl,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax1.axhline(30,  color=GRANITE_COLOR, lw=1.2, ls='--', label='Premium ITL SLO (30ms)')
ax1.axhline(60,  color=LLAMA_COLOR,   lw=1.2, ls='--', label='Bronze ITL SLO (60ms)')
ax1.set_ylabel('ITL (ms)')
ax1.legend(fontsize=7)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, granite_ttft, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax2.plot(times, llama_ttft,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax2.axhline(200,  color=GRANITE_COLOR, lw=1.2, ls='--', label='Premium TTFT SLO (200ms)')
ax2.axhline(1000, color=LLAMA_COLOR,   lw=1.2, ls='--', label='Bronze TTFT SLO (1000ms)')
ax2.set_ylabel('TTFT (ms)')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=7)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run9_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run9_latency.png')

# ── Figure 3: SWE α convergence ───────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 9 — Sliding-Window α Parameter Stability (H100 accelerator)',
             fontweight='bold')

granite_alpha = internal_series('granite_8b', 'H100', 'alpha')
llama_alpha   = internal_series('llama_13b',  'H100', 'alpha')

ax1.plot(times, granite_alpha, color=GRANITE_COLOR, lw=1.5, label='granite_8b α (SWE)')
ax1.axhline(8.0,  color=GRANITE_COLOR, lw=1, ls=':', label='Target (8.0ms)')
ax1.set_ylabel('α (ms)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_alpha, color=LLAMA_COLOR, lw=1.5, label='llama_13b α (SWE)')
ax2.axhline(12.0, color=LLAMA_COLOR, lw=1, ls=':', label='Target (12.0ms)')
ax2.set_ylabel('α (ms)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run9_swe_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run9_swe_alpha.png')

# ── Figure 4: SWE β parameter ─────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 9 — Sliding-Window β Parameter Stability (H100 accelerator)',
             fontweight='bold')

granite_beta = internal_series('granite_8b', 'H100', 'beta')
llama_beta   = internal_series('llama_13b',  'H100', 'beta')

ax1.plot(times, granite_beta, color=GRANITE_COLOR, lw=1.5, label='granite_8b β (SWE)')
ax1.axhline(0.016, color=GRANITE_COLOR, lw=1, ls=':', label='Target (0.016)')
ax1.set_ylabel('β (ms/tok)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_beta, color=LLAMA_COLOR, lw=1.5, label='llama_13b β (SWE)')
ax2.axhline(0.024, color=LLAMA_COLOR, lw=1, ls=':', label='Target (0.024)')
ax2.set_ylabel('β (ms/tok)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run9_swe_beta.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run9_swe_beta.png')

# ── Figure 5: Cycle timing ────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Run 9 — Cycle Timing (from controller logs)', fontweight='bold')

collect_ms = [
    816, 71, 63, 59, 68, 56, 65, 71, 139, 63,
    1207, 2017, 3214, 3209, 3220, 2418, 2419, 3617, 5617, 4808,
    3608, 4812, 4818, 4819, 4811, 4418, 3617, 4417, 4016, 4414,
    4017, 3218, 2820, 2418, 2423, 1607, 421, 818, 61, 62,
    60, 63, 73, 57, 65,
]
tune_ms = [
    52, 31, 37, 30, 45, 43, 50, 57, 62, 54,
    62, 73, 88, 71, 109, 106, 112, 107, 116, 90,
    107, 88, 90, 81, 73, 68, 57, 65, 71, 49,
    67, 64, 64, 67, 66, 50, 63, 62, 47, 59,
    65, 58, 55, 48, 59,
]

t = times[:len(collect_ms)]
ax.plot(t, collect_ms, color='#4472C4', lw=1.5, label='collect (ms)')
ax.fill_between(t, collect_ms, alpha=0.2, color='#4472C4')
ax.plot(t, tune_ms, color='#ED7D31', lw=1.5, label='tune/SWE (ms)')
ax.set_ylabel('Time (ms)')
ax.set_xlabel('Time (UTC)')
ax.legend(loc='upper right', fontsize=8)
ax.grid(True, alpha=0.3, axis='y')
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run9_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run9_timing.png')

# ── Figure 6: Total cost ──────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 3))
fig.suptitle('Run 9 — Total Allocation Cost Over Time', fontweight='bold')

total_cost = [r['totalCost'] for r in records]
ax.plot(times, total_cost, color='#7030A0', lw=1.5)
ax.fill_between(times, total_cost, alpha=0.2, color='#7030A0')
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Time (UTC)')
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run9_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run9_cost.png')

# ── Figure 7: Run 8 vs Run 9 α comparison ────────────────────────────────────
# Run 8 granite α values (from cycle log)
run8_granite_alpha = [
    8.082, 0.0000, 0.0000, 19.797, 20.185,
    20.877, 21.601, 0.0000, 25.104, 0.0000,
]
run8_cycles = list(range(1, len(run8_granite_alpha) + 1))
run9_cycles = list(range(1, len(records) + 1))

fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=False)
fig.suptitle('Run 8 vs Run 9 — granite_8b α: Before and After Fixes', fontweight='bold')

ax1.plot(run8_cycles, run8_granite_alpha, color='red', lw=1.5, marker='o', ms=4,
         label='Run 8 (broken SWE)')
ax1.axhline(8.0, color='grey', lw=1, ls=':', label='Target (8.0ms)')
ax1.set_ylabel('α (ms)')
ax1.set_xlabel('Cycle')
ax1.set_title('Run 8: Wild oscillation — optimizer scales granite 2→1 rep at cycle 2', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
ax1.set_ylim(-1, 30)

ax2.plot(run9_cycles, granite_alpha, color=GRANITE_COLOR, lw=1.5, marker='o', ms=3,
         label='Run 9 (fixed SWE)')
ax2.axhline(8.0, color='grey', lw=1, ls=':', label='Target (8.0ms)')
ax2.set_ylabel('α (ms)')
ax2.set_xlabel('Cycle')
ax2.set_title('Run 9: Stable convergence throughout all 45 cycles', fontsize=9)
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
ax2.set_ylim(6, 10)

plt.tight_layout()
plt.savefig(f'{OUT}/run9_vs_run8_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run9_vs_run8_alpha.png')

print('All Run 9 figures generated.')
