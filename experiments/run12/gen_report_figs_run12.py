#!/usr/bin/env python3
"""Generate experiment report figures from inferno-cycles.jsonl — Run 12.
Run 12: vllm-server evaluator (real CPU vLLM Qwen2.5-0.5B-Instruct), single
        managed deployment paired with vllm-qwen-cpu, cold start
        (no initial perfParms), sliding-window estimator.
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

MODEL = 'qwen_0_5b'
ACC   = 'cpu'

def server_series(field):
    return [
        next((s[field] for s in r['servers'] if s['model'] == MODEL), None)
        for r in records
    ]

def internal_series(field):
    return [
        next((i.get(field) for i in r.get('internals', [])
              if i['model'] == MODEL and i['acc'] == ACC), None)
        for r in records
    ]

def phase_boundaries():
    # Phases from yamls/deploy/configmap-load-phases-vllm.yaml.
    # Controller's first cycle attempt at 15:00:20Z; load emulator started
    # around the same time. Phase schedule (load-emulator interpolates ratios
    # linearly between phase endpoints):
    #   Phase 1: 20m hold at ratio 1.0  (30 RPM)
    #   Phase 2: 1s step  to ratio 2.0  (60 RPM)
    #   Phase 3: 20m ramp 2.0 → 1.0     (60 → 30 RPM)
    #   Phase 4: 1s step  to ratio 0.5  (15 RPM)
    #   Phase 5: hold forever at 0.5    (15 RPM)
    def utc(h, m, s):
        return datetime(2026, 5, 31, h, m, s, tzinfo=timezone.utc)
    return [
        utc(15,  0, 20),   # Phase 1 start (baseline 30 RPM)
        utc(15, 20, 20),   # Phase 2 step up to 60 RPM
        utc(15, 20, 21),   # Phase 3 start (decaying ramp 60→30)
        utc(15, 40, 21),   # Phase 4 step down to 15 RPM
        utc(15, 40, 22),   # Phase 5 hold at 15 RPM
    ]

phase_ts  = phase_boundaries()
phase_lbl = ['Ph1\nHold 30',
             'Ph2\n→60',
             'Ph3\nRamp 60→30',
             'Ph4\n→15',
             'Ph5\nHold 15']

QWEN_COLOR  = '#1f77b4'
SLO_COLOR   = '#d62728'

def add_phase_shading(ax):
    colors = ['#f0f0f0', '#fff2cc', '#d9ead3', '#fff2cc', '#f0f0f0']
    xlim_end = times[-1] + timedelta(seconds=60)
    boundaries = phase_ts + [xlim_end]
    for i in range(len(phase_ts)):
        ax.axvspan(boundaries[i], boundaries[i+1], alpha=0.35,
                   color=colors[i], zorder=0)
    ax.set_xlim(times[0] - timedelta(seconds=60), xlim_end)
    trans = ax.get_xaxis_transform()
    for t, lbl in zip(phase_ts, phase_lbl):
        ax.axvline(t, color='#999999', lw=0.8, ls='--', zorder=1)
        ax.text(t, 0.97, lbl, fontsize=6, ha='left', va='top', color='#555555',
                transform=trans)

def fmt_xaxis(ax):
    ax.xaxis.set_major_formatter(mdates.DateFormatter('%H:%M', tz=timezone.utc))
    ax.xaxis.set_major_locator(mdates.MinuteLocator(byminute=range(0, 60, 5)))
    plt.setp(ax.xaxis.get_majorticklabels(), rotation=30, ha='right', fontsize=7)

# ── Figure 1: RPM + Replicas ──────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Load and Autoscaling Response\n'
             '(real CPU vLLM Qwen2.5-0.5B, sliding-window estimator, cold start)',
             fontweight='bold')

rpm        = server_series('rpm')
throughput = server_series('throughput')
replicas   = server_series('replicas')

ax1.plot(times, rpm,        color=QWEN_COLOR, lw=1.5, label='Arrival rate (RPM)')
ax1.plot(times, throughput, color='#888888',  lw=1.0, ls='--',
         label='Reported throughput (RPM)')
ax1.set_ylabel('Rate (RPM)')
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.step(times, replicas, where='post', color=QWEN_COLOR, lw=1.5,
         label='vllm-qwen replicas')
ax2.axhline(2, color=SLO_COLOR, lw=0.8, ls=':', alpha=0.5,
            label='cpu capacity (2)')
ax2.set_ylabel('Replica count')
ax2.set_ylim(0, 3)
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_load_replicas.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_load_replicas.png')

# ── Figure 2: ITL + TTFT with SLOs ───────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Latency vs SLO Targets (Bronze class)', fontweight='bold')

itl  = server_series('itl')
ttft = server_series('ttft')

# Mark cycles where the pod was excluded (saturated) as gaps
itl_plot  = [v if v and v > 0 else None for v in itl]
ttft_plot = [v if v and v > 0 else None for v in ttft]

ax1.plot(times, itl_plot, color=QWEN_COLOR, lw=1.5, marker='o', ms=4,
         label='qwen_0_5b ITL (measured)')
ax1.axhline(100, color=SLO_COLOR, lw=1.2, ls='--', label='Bronze ITL SLO (100 ms)')
ax1.set_ylabel('ITL (ms)')
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

# Mark saturation-skipped cycles on the TTFT plot
sat_times = [t for t, v in zip(times, ttft) if v == 0]
ax2.plot(times, ttft_plot, color=QWEN_COLOR, lw=1.5, marker='o', ms=4,
         label='qwen_0_5b TTFT (measured)')
ax2.axhline(500, color=SLO_COLOR, lw=1.2, ls='--', label='Bronze TTFT SLO (500 ms)')
for t in sat_times:
    ax2.axvline(t, color='#cc6600', lw=0.6, alpha=0.5, zorder=1)
if sat_times:
    ax2.plot([], [], color='#cc6600', lw=0.6, alpha=0.7,
             label='saturation → pod excluded')
ax2.set_ylabel('TTFT (ms)')
ax2.set_xlabel('Time (UTC)')
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_latency.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_latency.png')

# ── Figure 3: Tokens ─────────────────────────────────────────────────────────
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 6), sharex=True)
fig.suptitle('Average Input and Output Tokens per Request', fontweight='bold')

in_tok  = server_series('avgInTok')
out_tok = server_series('avgOutTok')

ax1.plot(times, in_tok, color=QWEN_COLOR, lw=1.5, marker='o', ms=4,
         label='qwen_0_5b (Bronze)')
ax1.axhline(64, color=QWEN_COLOR, lw=1.2, ls='--', label='nominal (64)')
ax1.set_ylabel('Avg input tokens')
ax1.yaxis.set_major_locator(matplotlib.ticker.MultipleLocator(16))
ax1.legend(fontsize=8)
ax1.grid(True, alpha=0.3)
add_phase_shading(ax1)

ax2.plot(times, out_tok, color=QWEN_COLOR, lw=1.5, marker='o', ms=4,
         label='qwen_0_5b (Bronze)')
ax2.axhline(32, color=QWEN_COLOR, lw=1.2, ls='--', label='nominal (32)')
ax2.set_ylabel('Avg output tokens')
ax2.set_xlabel('Time (UTC)')
ax2.yaxis.set_major_locator(matplotlib.ticker.MultipleLocator(8))
ax2.legend(fontsize=8)
ax2.grid(True, alpha=0.3)
add_phase_shading(ax2)
fmt_xaxis(ax2)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_tokens.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_tokens.png')

# ── Figure 4: SWE α convergence ──────────────────────────────────────────────
# InitEstimator's first fit was alpha=40.83, beta=1.82, gamma=0.094.
# After the SWE took over, parameters settled to α≈62, β≈1.85, γ≈1.6e-6.
INIT_ALPHA, INIT_BETA, INIT_GAMMA = 40.83, 1.82, 0.094

fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Sliding-Window α Parameter Convergence (cold start, qwen_0_5b / cpu)',
             fontweight='bold')

alpha_s = internal_series('alpha')

ax.plot(times, alpha_s, color=QWEN_COLOR, lw=1.5, marker='o', ms=4,
        label='qwen_0_5b α (SWE)')
ax.axhline(INIT_ALPHA, color='#888888', lw=1, ls=':',
           label=f'InitEstimator fit ({INIT_ALPHA} ms)')
ax.set_ylabel('α (ms)')
ax.set_xlabel('Time (UTC)')
ax.legend(fontsize=8)
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_swe_alpha.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_swe_alpha.png')

# ── Figure 5: SWE β ──────────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Sliding-Window β Parameter (qwen_0_5b / cpu)', fontweight='bold')

beta_s = internal_series('beta')

ax.plot(times, beta_s, color=QWEN_COLOR, lw=1.5, marker='o', ms=4,
        label='qwen_0_5b β (SWE)')
ax.axhline(INIT_BETA, color='#888888', lw=1, ls=':',
           label=f'InitEstimator fit ({INIT_BETA} ms/tok)')
ax.set_ylabel('β (ms/tok)')
ax.set_xlabel('Time (UTC)')
ax.legend(fontsize=8)
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_swe_beta.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_swe_beta.png')

# ── Figure 6: SWE γ ──────────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Sliding-Window γ Parameter (qwen_0_5b / cpu)', fontweight='bold')

gamma_s = internal_series('gamma')

ax.plot(times, gamma_s, color=QWEN_COLOR, lw=1.5, marker='o', ms=4,
        label='qwen_0_5b γ (SWE)')
ax.set_ylabel('γ (ms/tok²)')
ax.set_xlabel('Time (UTC)')
# γ stayed in the 1.4e-6 — 1.9e-6 range after the InitEstimator's outlier
# (0.094) was discarded by the first SWE fit. Auto y-scale shows the SWE band.
ax.legend(fontsize=8)
ax.grid(True, alpha=0.3)
ax.ticklabel_format(axis='y', style='sci', scilimits=(-6, -6))
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_swe_gamma.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_swe_gamma.png')

# ── Figure 7: Cycle timing ───────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 4))
fig.suptitle('Cycle Timing (collect dominated by /simulate calls)',
             fontweight='bold')

collect_ms = [r['timing']['collectMs'] for r in records]
tune_ms    = [r['timing']['tuneMs']    for r in records]

ax.plot(times, collect_ms, color='#4472C4', lw=1.5, label='collect (ms)')
ax.fill_between(times, collect_ms, alpha=0.2, color='#4472C4')
ax.plot(times, tune_ms, color='#ED7D31', lw=1.5, label='tune/SWE (ms)')
ax.set_ylabel('Time (ms)')
ax.set_xlabel('Time (UTC)')
ax.legend(loc='center right', fontsize=8)
ax.grid(True, alpha=0.3, axis='y')
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_timing.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_timing.png')

# ── Figure 8: Total cost ─────────────────────────────────────────────────────
fig, ax = plt.subplots(figsize=(10, 3))
fig.suptitle('Total Allocation Cost Over Time', fontweight='bold')

total_cost = [r['totalCost'] for r in records]
ax.plot(times, total_cost, color='#7030A0', lw=1.5)
ax.fill_between(times, total_cost, alpha=0.2, color='#7030A0')
ax.set_ylabel('Cost (arb. units)')
ax.set_xlabel('Time (UTC)')
ax.set_ylim(0, max(total_cost) + 1)
ax.grid(True, alpha=0.3)
add_phase_shading(ax)
fmt_xaxis(ax)

plt.tight_layout()
plt.savefig(f'{OUT}/run12_cost.png', dpi=150, bbox_inches='tight')
plt.close()
print('Saved run12_cost.png')

print('All Run 12 figures generated.')
