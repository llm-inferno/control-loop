#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl — Run 10.
Run 10: queue-analysis evaluator, sliding-window Nelder-Mead estimator (SWE),
        cold start (no initial perfParms), TUNER_INIT_FIT_THRESHOLD=10 (new).
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
    # Phase entry times estimated from load-emulator log (18 × 20s per phase):
    # Phase 1 (1× hold, 6 min):  entered ~16:49 UTC
    # Phase 2 (ramp 1→5×, 5 min): entered ~16:55 UTC
    # Phase 3 (5× hold, 5 min):   entered ~17:00 UTC
    # Phase 4 (ramp 5→1×, 5 min): entered ~17:05 UTC
    # Phase 5 (1× hold, ∞):       entered ~17:10 UTC
    def utc(h, m, s):
        return datetime(2026, 4, 21, h, m, s, tzinfo=timezone.utc)
    return [
        utc(16, 49,  0),
        utc(16, 55,  0),
        utc(17,  0,  0),
        utc(17,  5,  0),
        utc(17, 10,  0),
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
fig.suptitle('Run 10 — Load and Autoscaling Response\n'
             '(sliding-window Nelder-Mead, cold start, initFitThreshold=10)',
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
plt.savefig(f'{OUT}/run10_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run10_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 10 — Latency vs SLO Targets', fontweight='bold')

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

# TTFT: clip extreme values for readability, annotate outliers
granite_ttft_clipped = [min(v, 600) if v is not None else None for v in granite_ttft]
llama_ttft_clipped   = [min(v, 2000) if v is not None else None for v in llama_ttft]

ax2.plot(times, granite_ttft_clipped, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax2.plot(times, llama_ttft_clipped,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax2.axhline(200,  color=GRANITE_COLOR, lw=1.2, ls='--', label='Premium TTFT SLO (200ms)')
ax2.axhline(1000, color=LLAMA_COLOR,   lw=1.2, ls='--', label='Bronze TTFT SLO (1000ms)')

# Annotate clipped outliers
for i, (t, v) in enumerate(zip(times, granite_ttft)):
    if v is not None and v > 600:
        ax2.annotate(f'{v/1000:.1f}k', xy=(t, 600), fontsize=6, color=GRANITE_COLOR,
                     ha='center', va='bottom', rotation=45)
for i, (t, v) in enumerate(zip(times, llama_ttft)):
    if v is not None and v > 2000:
        ax2.annotate(f'{v/1000:.1f}k', xy=(t, 2000), fontsize=6, color=LLAMA_COLOR,
                     ha='center', va='bottom', rotation=45)

ax2.set_ylabel('TTFT (ms, clipped)')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=7)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run10_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run10_latency.png')

# ── Figure 3: SWE α convergence ───────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 10 — Sliding-Window α Parameter Convergence (cold start, H100)',
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
plt.savefig(f'{OUT}/run10_swe_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run10_swe_alpha.png')

# ── Figure 4: SWE β parameter ─────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 10 — Sliding-Window β Parameter Stability (H100)', fontweight='bold')

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
plt.savefig(f'{OUT}/run10_swe_beta.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run10_swe_beta.png')

# ── Figure 5: Cycle timing ────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Run 10 — Cycle Timing (from controller logs)', fontweight='bold')

collect_ms = [
    811,  60,  59, 130,  68,  56,  61,  67,  65, 138,
    411, 813, 1216, 1623, 2021, 3611, 4419, 4416, 6016, 6409,
    5217, 5214, 4812, 3618, 4810, 4413, 4413, 4811, 4809, 3615,
    3614, 4013, 2425, 2015, 2020, 1213,  411,  57,  134,  66,
      64,   64,  69,   54,  65,
]
tune_ms = [
     44,  38,  40,  49,  57,  49,  56,  54,  55,  50,
     48,  46,  48,  62,  67,  56,  87,  88,  87,  82,
     81,  90,  75,  90,  75,  78,  66,  56,  53,  46,
     70,  65,  54,  66,  70,  67,  68,  51,  59,  58,
     55,  51,  51,  48,  63,
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
plt.savefig(f'{OUT}/run10_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run10_timing.png')

# ── Figure 6: Total cost ──────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 3))
fig.suptitle('Run 10 — Total Allocation Cost Over Time', fontweight='bold')

total_cost = [r['totalCost'] for r in records]
ax.plot(times, total_cost, color='#7030A0', lw=1.5)
ax.fill_between(times, total_cost, alpha=0.2, color='#7030A0')
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Time (UTC)')
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run10_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run10_cost.png')

# ── Figure 7: γ parameter ─────────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Run 10 — Sliding-Window γ Parameter Stability (H100)', fontweight='bold')

granite_gamma = internal_series('granite_8b', 'H100', 'gamma')
llama_gamma   = internal_series('llama_13b',  'H100', 'gamma')

ax1.plot(times, granite_gamma, color=GRANITE_COLOR, lw=1.5, label='granite_8b γ (SWE)')
ax1.axhline(0.0005, color=GRANITE_COLOR, lw=1, ls=':', label='Target (0.0005)')
ax1.set_ylabel('γ (ms/tok²)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_gamma, color=LLAMA_COLOR, lw=1.5, label='llama_13b γ (SWE)')
ax2.axhline(0.00075, color=LLAMA_COLOR, lw=1, ls=':', label='Target (0.00075)')
ax2.set_ylabel('γ (ms/tok²)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run10_swe_gamma.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run10_swe_gamma.png')

print('All Run 10 figures generated.')
