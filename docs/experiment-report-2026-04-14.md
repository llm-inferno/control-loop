# Experiment Report — blis/trained-physics Evaluator with 5-Minute Simulation Horizon
**Date:** 2026-04-14  
**Cluster:** kind (kind-cluster), Docker Desktop, macOS arm64, single node  
**Control period:** 30s  
**Workloads:** `blis-granite-8b` (granite_8b/H100, Premium, nominal 60 RPM), `blis-llama-13b` (llama_13b/H100, Bronze, nominal 30 RPM)  
**Evaluator:** blis/trained-physics backend  
**Features under test:** 5-minute simulation horizon (up from 60s), INFERNO_LOAD_THETA=0.7

---

## 1. Objective

Validate the blis/trained-physics evaluator with an increased simulation horizon (60s → 300s) intended to reduce cold-start bias and stabilize EKF observations during the warm-up phase. Also tests a higher mean-reversion coefficient (THETA=0.7 vs prior 0.4) so the load emulator tracks load-phase transitions more closely.

This is the first complete (fresh-deploy, phase 1 through phase 5) phased experiment run with the blis evaluator. Prior session (2026-04-13) ended mid-run due to EKF instability at peak load.

---

## 2. Configuration Changes vs Defaults

| File | Parameter | Previous | This Run |
|---|---|---|---|
| `configmap-blis-small.yaml` | `simulationHorizon` | 60,000,000 µs (60s) | 300,000,000 µs (300s) |
| `yamls/deploy/load-emulator.yaml` | `INFERNO_LOAD_THETA` | 0.4 | 0.7 |
| `inferno-evaluator` image | betaCoeffs/alphaCoeffs | per-model calibrated | inference-sim v0.7.4 trained-physics defaults |

All other blis configuration parameters (SLOs, capacity, optimizer policy) are unchanged from the 2026-04-13 final state:

| File | Parameter | Value |
|---|---|---|
| `blis-data/serviceclass-data.json` | Premium all-models slo-itl | 15ms |
| `blis-data/serviceclass-data.json` | Premium all-models slo-ttft | 100ms |
| `blis-data/serviceclass-data.json` | Bronze llama_13b slo-itl | 50ms |
| `blis-data/capacity-data.json` | H100 count | 16 |
| `blis-data/optimizer-data.json` | saturationPolicy | PriorityExhaustive |
| `yamls/deploy/deploy-loop.yaml` | INFERNO_WARM_UP_TIMEOUT | 0 (disabled) |
| `yamls/deploy/deploy-loop.yaml` | INFERNO_STARTUP_DELAY | 60s |
| tuner | TUNER_INIT_OBS | 3 |
| tuner | TUNER_WARM_UP_CYCLES | 3 |

---

## 3. Load Profile

The load emulator ran a 5-phase sequence:

| Phase | Duration | Nominal RPM (granite / llama) | Behavior |
|---|---|---|---|
| 1 | 3 min | 60 / 30 | Hold flat at baseline (1×) |
| 2 | 5 min | 60 → 300 / 30 → 150 | Linear ramp to 5× baseline |
| 3 | 5 min | 300 / 150 | Hold at 5× |
| 4 | 5 min | 300 → 60 / 150 → 30 | Linear ramp back to 1× |
| 5 | ∞ | 60 / 30 | Hold at baseline |

**Note:** The load emulator runs independently of the controller warm-up. With a 4-minute warm-up time (deploy → first optimize), the controller first acted at the beginning of phase 2 (ramp already underway). Phase 3 peak was reached approximately 5 minutes after the first optimize.

---

## 4. Warm-up Sequence

| Time (UTC) | Event |
|---|---|
| 14:05:45 | Deploy |
| 14:06:15–14:06:45 | Cycles 1–2: pods within 60s startup delay — no replicaSpecs, optimizer 404 (skip) |
| 14:07:15 | Cycle 3: first replicaSpecs (obs 1/3 for both models) |
| 14:07:45 | Cycle 4: obs 2/3 |
| 14:08:15 | Cycle 5: obs 3/3 — **Fit() runs** (tune=124ms), EKF warm-up begins, controller holds back |
| 14:08:45 | Cycle 6: EKF updating (warmUp=true), controller holds back |
| 14:09:16 | Cycle 7: EKF updating (warmUp=true), controller holds back |
| 14:09:46 | Cycle 8: **First optimize+actuate** (collect: 823ms, tune: 3ms, optimize: 5ms, actuate: 17ms) |

Total hold-back from first observation: **90 seconds** (3 cycles × 30s). Total time from deploy to first optimize: **~4 minutes** (startup delay 60s + 3 collection cycles + 3 warm-up cycles).

**InitEstimator Fit results** (3 observations at phase 1 baseline + beginning of phase 2 ramp):

| Model | α (Fit) | β (Fit) | γ (Fit) | Objective value | Quality |
|---|---|---|---|---|---|
| granite_8b/H100 | 6.51 ms | 0.00867 | 0.000171 | 0.00043 | Excellent |
| llama_13b/H100 | 33.4 ms | 1.2×10⁻⁷ | 9.7×10⁻¹¹ | 0.148 | Poor — β/γ ≈ 0 |

The granite fit is excellent (low residual, physically reasonable parameters). The llama fit is poor: β and γ are effectively zero, meaning the Nelder-Mead minimizer found only the constant α term. This reflects identifiability failure — at baseline 30 RPM, all 3 observations are at similar (low) utilization, making the load-dependent terms unobservable.

---

## 5. EKF Parameter Convergence

### granite_8b/H100

The granite EKF was robust throughout the entire experiment.

| Phase | α | NIS | Notes |
|---|---|---|---|
| Post-fit | 6.51 ms | — | warmUp=true |
| First optimize (updateCount=4) | 6.47 ms | 0.0001 | Clean |
| Phase 2 ramp (updateCount≈10) | 6.2 ms | 0.013 | Drifting slightly under load |
| Phase 3 peak (updateCount≈15–25) | 7.5–8.2 ms | 0.001–0.027 | Stable, higher load pushes α up |
| Phase 4 ramp-down | 8.1 ms | 0.002 | |
| Phase 5 final (updateCount=44) | 8.0 ms | 0.003 | **Converged** |

NIS remained below 0.05 for all 44 accepted updates. No rejected observations for granite throughout the experiment.

### llama_13b/H100

The llama EKF showed a recurring instability pattern triggered by replica count changes.

| Phase | α | NIS | Notes |
|---|---|---|---|
| Post-fit | 33.4 ms | — | warmUp=true |
| First EKF update (updateCount=1) | 21.4 ms | 0.018 | EKF corrected poor fit immediately |
| End of warm-up (updateCount=3) | 25.6 ms | 0.12 | warmUp=true → false |
| First scale-out (2→3/4 replicas) | **3 ms** | Storm | NIS 200–80,000 for 2 cycles; wrong attractor |
| During phase 3 peak (8 replicas) | 3–5 ms | Mixed | Oscillating; 1 observation accepted/cycle |
| Phase 4 ramp-down scale-in | 5→8 ms | 0.006–0.022 | Self-correcting as per-pod load increases |
| Post-ramp-down | 8–11 ms | 0.002–0.30 | Continuing upward drift |
| Phase 5 final (updateCount=37) | 11 ms | 0.003 | Still self-correcting post-experiment |

---

## 6. Structural Finding: blis TTFT Instability at Low Per-Pod Load

The dominant issue of this experiment was a previously unreported instability in the blis/trained-physics evaluator for `llama_13b` at low per-pod arrival rates (~15–22 RPM/pod).

**Observed behavior:**

| Pod | ITL | TTFT | Throughput |
|---|---|---|---|
| blis-llama-13b-cwhmv | 38.5 ms | 1,765 ms | 0.39 req/s |
| blis-llama-13b-gftjp | 33.9 ms | **75 ms** | 0.35 req/s |
| blis-llama-13b-mv58k | 36.9 ms | 931 ms | 0.38 req/s |
| blis-llama-13b-p98lc | 38.6 ms | 1,966 ms | 0.39 req/s |
| blis-llama-13b-tpq8b | 40.3 ms | 4,351 ms | 0.40 req/s |

All pods serve identical load (~22 RPM), use identical model configs, and run identical evaluator code. ITL is consistent (34–40 ms). TTFT ranges from 75 ms to 4,351 ms — a 58× spread — within the same collector cycle.

**Root cause hypothesis:** At ~0.35 req/s arrival rate over a 5-minute simulation horizon (~105 requests), the blis prefill simulation is sensitive to stochastic batch composition. A run that happens to avoid head-of-line blocking (short prefill queues) reports TTFT≈75 ms; one that encounters it reports TTFT≈4000 ms. The 5-minute horizon was sufficient to stabilize ITL (many decode-step observations) but insufficient to average out the TTFT distribution for low-rate requests.

**EKF impact:** The NIS gate rejects high-TTFT observations (NIS=200–80,000). The one pod with anomalously low TTFT (≈75 ms) passes the gate and anchors the EKF. Over several cycles, α converges to ≈3 ms — a self-consistent but physically wrong attractor for llama_13b.

**Granite was unaffected** because at 70–90 RPM/pod (~1.3 req/s), sufficient prefill samples exist to average out TTFT variance. The 5-minute horizon was adequate for granite.

---

## 7. Scale-Change NIS Storms

Every change in replica count for llama triggered a **NIS storm** (1–3 cycles of large rejections) as the new per-pod load distribution produced observations outside the EKF's current prediction range.

| Event | Time | NIS range | Recovery cycles |
|---|---|---|---|
| 2→3/4 replicas (first optimize) | 14:09:46 | 200–80,000 | 2 |
| 4→8 replicas (phase 2 ramp) | 14:14:48 | 200–39,000 | 1 |
| 8→7 replicas (scale-in start) | 14:20:49 | 9–37 | ~2 |

The recovery time decreased with each event (EKF accumulates history), but the fundamental pattern — scale → new operating point → burst of rejected observations — recurred reliably. Granite experienced no NIS storms despite also scaling (2→6→1 replicas), suggesting this is specific to the llama operating regime.

---

## 8. Autoscaling Response

### Scale-out (phases 2–3)

| Time | granite replicas | llama replicas | Load context |
|---|---|---|---|
| 14:09:46 (first optimize) | 2 | 3 | Phase 2 ramp beginning |
| 14:10:16 | 2 | 4 | Phase 2, ~2.4× nominal |
| ~14:13 | 4 | 7–8 | Phase 2/3 transition, ~5× nominal |
| ~14:14 | 6 | 8 | Phase 3 peak, 5× nominal |

Granite scaled correctly in proportion to load (2→6 replicas at 5× load). Llama scaled excessively (2→8 replicas) due to the wrong α attractor (optimizer underestimated per-replica capacity with α≈3ms).

### Scale-in (phase 4–5)

| Time | granite replicas | llama replicas | Collect time |
|---|---|---|---|
| 14:22:17 | scaling in | scaling in | 2.4s |
| 14:23:47 | scaling in | scaling in | 1.6s |
| 14:24:16 | 1 | 3 | 825ms |
| 14:26:45 | 1 | 2 | 605ms |

Granite returned cleanly to 1 replica. Llama settled at 2 replicas at baseline (vs 1 in the initial deployment), consistent with the optimizer using α≈8–11 ms instead of the correct ~20+ ms.

---

## 9. Cycle Timing

| Phase | Pods | collect | tune | optimize | actuate | total |
|---|---|---|---|---|---|---|
| Baseline/warm-up (phase 1) | 4 | ~825 ms | 1–3 ms | 2–7 ms | 9–17 ms | ~850 ms |
| Phase 2 ramp (mid, 8–10 pods) | 8–10 | ~2400 ms | 2–5 ms | 3–7 ms | 10–22 ms | ~2440 ms |
| Phase 3 peak (12–13 pods) | 12–13 | ~4600–5000 ms | 2–5 ms | 3–8 ms | 10–25 ms | ~4640–5035 ms |
| Phase 5 final (3 pods) | 3 | ~615 ms | 1–3 ms | 2–7 ms | 10–21 ms | ~640 ms |

**Per-pod simulation time:** ~385 ms (vs ~50 ms in the 2026-04-11 queue-analysis experiment). The 5× increase in simulation horizon produced a ~7.7× increase in per-pod collect time, consistent with the blis simulation running at roughly 5–8× real-time speed. The collect phase dominates at peak load and is the primary scalability bottleneck.

**`maxRPS=0.00` on all blis pods:** The blis/trained-physics evaluator does not return a `maxRPS` value. The near-saturation re-simulation logic (Throughput/MaxRPS ≥ 0.95 → re-simulate at 90% MaxRPS) is therefore disabled. This is not a problem at the loads observed but should be noted for future high-utilization experiments.

---

## 10. Summary

| Aspect | Result |
|---|---|
| InitEstimator warm-up | Clean, 90s hold-back ✓ |
| granite_8b EKF convergence | α: 6.5→8.0 ms, NIS <0.05 throughout. **Excellent** ✓ |
| llama_13b EKF convergence | α: 33→3→8→11 ms. TTFT instability caused wrong attractor. **Partial** ⚠ |
| Scale-out | granite 2→6 correct ✓; llama 2→8 excessive (EKF-driven over-allocation) ⚠ |
| Scale-in | granite 6→1 clean ✓; llama 8→2 (vs baseline 1) ⚠ |
| Latency SLO compliance | Not directly measured (no JSONL cycle log analysis); ITL for both models within observed range |
| TTFT instability | blis evaluator produces 75–4350 ms TTFT spread for llama at ~22 RPM/pod — **new finding** |
| Scale-change NIS storms | Structural: every replica change triggers 1–3 cycle NIS storm for llama |
| Collect time at peak | ~5 s (13 pods, ~385 ms/pod) — 7.7× slower than queue-analysis with 1-min horizon |
| Errors | No optimizer 404 errors during normal operation; no controller crashes |

---

## 11. Open Issues and Recommendations

1. **llama_13b TTFT instability in blis evaluator.** At ~15–22 RPM/pod, TTFT varies 75–4350 ms across pods running identical workloads. The 5-minute simulation horizon was insufficient to average out this variance. Possible fixes:
   - Investigate the blis evaluator's prefill simulation at low arrival rates
   - Set a minimum per-pod RPM threshold before contributing a replicaSpec to the tuner
   - Use a longer simulation horizon (e.g., 10 min) for low-load conditions

2. **EKF wrong attractor for llama.** Alpha converged to ~3 ms (wrong) during scale-out. The NIS gate correctly rejected high-TTFT outliers, but the one low-TTFT pod (an outlier in the other direction) anchored the EKF. Possible fixes:
   - Widen the NIS gate during the first few cycles after a replica count change
   - Median-pool multiple pod observations rather than processing them sequentially
   - Track replica count changes and reset/re-fit the EKF after each scale event

3. **Llama baseline over-allocation.** With α≈8–11 ms instead of the physical ~20+ ms, the optimizer allocates 2 replicas at 30 RPM baseline. At correct parameters it would allocate 1. This is a downstream symptom of issue 1 and 2.

4. **maxRPS=0 from blis evaluator.** The near-saturation re-simulation guard is disabled. File a bug or add a fallback in the Collector.

5. **Load emulator phases misaligned with warm-up.** With a 4-minute warm-up and a 3-minute phase 1, the controller first acts during the load ramp (phase 2). Consider increasing phase 1 duration to 6+ minutes so the EKF can stabilize at baseline before the ramp begins.

---

## Appendix: Reproduction

```bash
# Deploy (from control-loop/ repo root)
bash scripts/kind-deploy-blis.sh

# Copy cycle log from running pod
kubectl cp inferno/$(kubectl get pod -n inferno -l app=inferno \
  -o jsonpath='{.items[0].metadata.name}'):inferno-cycles.jsonl /tmp/inferno-cycles.jsonl

# Generate figures (if cycle log available)
python3 scripts/gen_report_figs.py

# Watch controller and tuner during experiment
kubectl logs -f -n inferno deployment/inferno -c controller
kubectl logs -f -n inferno deployment/inferno -c tuner
```
