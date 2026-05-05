#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl — Run 11.
Run 11: queue-analysis evaluator, EKF estimator (Extended Kalman Filter),
        cold start (no initial perfParms), TUNER_INIT_OBS=3.
"""

import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import matplotlib.dates as mdates
import matplotlib.ticker

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
    # Load emulator started ~21:02 UTC on 2026-05-04.
    # Phase durations: Ph1=6min, Ph2=5min, Ph3=5min, Ph4=5min, Ph5=hold.
    # Inferred from JSONL RPM pattern (ramp up visible at cycle 10, 21:08:45).
    def utc(h, m, s=0):
        return datetime(2026, 5, 4, h, m, s, tzinfo=timezone.utc)
    return [
        utc(21,  2),   # Phase 1: baseline  (nominal 60/30 RPM)
        utc(21,  8),   # Phase 2: ramp 1→5× (nominal 60→300 RPM)
        utc(21, 13),   # Phase 3: hold 5×   (nominal 300 RPM)
        utc(21, 18),   # Phase 4: ramp 5→1× (nominal 300→60 RPM)
        utc(21, 23),   # Phase 5: hold 1×   (nominal 60/30 RPM)
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
    ax.set_xlim(times[0] - timedelta(seconds=30), xlim_end)
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
fig.suptitle('Load and Autoscaling Response\n'
             '(EKF estimator, cold start, TUNER_INIT_OBS=3)',
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
plt.savefig(f'{OUT}/run11_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Latency vs SLO Targets', fontweight='bold')

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

for t, v in zip(times, granite_ttft):
    if v is not None and v > 600:
        ax2.annotate(f'{v/1000:.1f}k', xy=(t, 600), fontsize=6, color=GRANITE_COLOR,
                     ha='center', va='bottom', rotation=45)
for t, v in zip(times, llama_ttft):
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
plt.savefig(f'{OUT}/run11_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_latency.png')

# ── Figure 3: EKF α convergence ───────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('EKF α Parameter Convergence (cold start, H100)', fontweight='bold')

granite_alpha = internal_series('granite_8b', 'H100', 'alpha')
llama_alpha   = internal_series('llama_13b',  'H100', 'alpha')

ax1.plot(times, granite_alpha, color=GRANITE_COLOR, lw=1.5, label='granite_8b α (EKF)')
ax1.axhline(8.0,  color=GRANITE_COLOR, lw=1, ls=':', label='Target (8.0ms)')
ax1.set_ylabel('α (ms)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_alpha, color=LLAMA_COLOR, lw=1.5, label='llama_13b α (EKF)')
ax2.axhline(12.0, color=LLAMA_COLOR, lw=1, ls=':', label='Target (12.0ms)')
ax2.set_ylabel('α (ms)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run11_ekf_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_ekf_alpha.png')

# ── Figure 4: EKF β parameter ─────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('EKF β Parameter Stability (H100)', fontweight='bold')

granite_beta = internal_series('granite_8b', 'H100', 'beta')
llama_beta   = internal_series('llama_13b',  'H100', 'beta')

ax1.plot(times, granite_beta, color=GRANITE_COLOR, lw=1.5, label='granite_8b β (EKF)')
ax1.axhline(0.016, color=GRANITE_COLOR, lw=1, ls=':', label='Target (0.016)')
ax1.set_ylabel('β (ms/tok)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_beta, color=LLAMA_COLOR, lw=1.5, label='llama_13b β (EKF)')
ax2.axhline(0.024, color=LLAMA_COLOR, lw=1, ls=':', label='Target (0.024)')
ax2.set_ylabel('β (ms/tok)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run11_ekf_beta.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_ekf_beta.png')

# ── Figure 5: EKF γ parameter ─────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('EKF γ Parameter Stability (H100)', fontweight='bold')

granite_gamma = internal_series('granite_8b', 'H100', 'gamma')
llama_gamma   = internal_series('llama_13b',  'H100', 'gamma')

ax1.plot(times, granite_gamma, color=GRANITE_COLOR, lw=1.5, label='granite_8b γ (EKF)')
ax1.axhline(0.0005, color=GRANITE_COLOR, lw=1, ls=':', label='Target (0.0005)')
ax1.set_ylabel('γ (ms/tok²)')
ax1.set_title('granite_8b / H100', fontsize=9)
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, llama_gamma, color=LLAMA_COLOR, lw=1.5, label='llama_13b γ (EKF)')
ax2.axhline(0.00075, color=LLAMA_COLOR, lw=1, ls=':', label='Target (0.00075)')
ax2.set_ylabel('γ (ms/tok²)')
ax2.set_title('llama_13b / H100', fontsize=9)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run11_ekf_gamma.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_ekf_gamma.png')

# ── Figure 6: Cycle timing ────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Cycle Timing (collect + EKF tune)', fontweight='bold')

collect_ms = [r['timing']['collectMs'] for r in records]
tune_ms    = [r['timing']['tuneMs']    for r in records]

ax.plot(times, collect_ms, color='#4472C4', lw=1.5, label='collect (ms)')
ax.fill_between(times, collect_ms, alpha=0.2, color='#4472C4')
ax.plot(times, tune_ms, color='#ED7D31', lw=1.5, label='tune/EKF (ms)')
ax.set_ylabel('Time (ms)')
ax.set_xlabel('Time (UTC)')
ax.legend(loc='upper right', fontsize=8)
ax.grid(True, alpha=0.3, axis='y')
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run11_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_timing.png')

# ── Figure 7: Total cost ──────────────────────────────────────────────────────
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
plt.savefig(f'{OUT}/run11_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_cost.png')

# ── Figure 8: Input and output tokens ────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Average Input and Output Tokens per Request', fontweight='bold')

granite_in_tok  = server_series('avgInTok',  'granite_8b')
llama_in_tok    = server_series('avgInTok',  'llama_13b')
granite_out_tok = server_series('avgOutTok', 'granite_8b')
llama_out_tok   = server_series('avgOutTok', 'llama_13b')

ax1.plot(times, granite_in_tok, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax1.plot(times, llama_in_tok,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax1.axhline(2048, color=GRANITE_COLOR, lw=1.2, ls='--', label='granite_8b nominal (2048)')
ax1.axhline(768,  color=LLAMA_COLOR,   lw=1.2, ls='--', label='llama_13b nominal (768)')
ax1.set_ylabel('Avg Input Tokens')
ax1.yaxis.set_major_locator(matplotlib.ticker.MultipleLocator(256))
ax1.legend(fontsize=7)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, granite_out_tok, color=GRANITE_COLOR, lw=1.5, label='granite_8b (Premium)')
ax2.plot(times, llama_out_tok,   color=LLAMA_COLOR,   lw=1.5, label='llama_13b (Bronze)')
ax2.axhline(1024, color=GRANITE_COLOR, lw=1.2, ls='--', label='granite_8b nominal (1024)')
ax2.axhline(768,  color=LLAMA_COLOR,   lw=1.2, ls='--', label='llama_13b nominal (768)')
ax2.set_ylabel('Avg Output Tokens')
ax2.set_xlabel('Time (UTC)')
ax2.yaxis.set_major_locator(matplotlib.ticker.MultipleLocator(256))
ax2.legend(fontsize=7)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run11_tokens.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run11_tokens.png')

print('All Run 11 figures generated.')
