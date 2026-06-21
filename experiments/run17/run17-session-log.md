# run17 — Session Log (working narrative)

**Date:** 2026-06-20 → 2026-06-21
**Scope:** Planning, execution, debugging, and write-up of the run17 three-arm concurrency-control A/B/B on real vLLM.
**Note:** This is a reconstructed narrative of the working session (decision trail + reasoning), not a verbatim transcript. The polished results live in [`experiment-report-2026-06-21-run17.md`](experiment-report-2026-06-21-run17.md); this file records *how we got there*, including the dead ends.

---

## 1. Design discussion

Goal: repeat run16 (concurrency control A/B on real vLLM) after the continuous-streaming + parallel-activation changes, contrasting **A (concurrency search)** vs **B (none)**.

User's opening proposal: lower the load-emulator interval (~10 s) to track current time vs the 120 s cycle; reuse run15's traffic pattern but longer (10 m baseline / 6 m ramp to 5× / 6 m hold / 4 m down / hold = 30 min); raise capacity 6→8 for the 5×; widen the eval window 30→100 s for statistical significance; verify all configs/manifests reflect recent code changes.

**Key pushback (grounded in run16):** run16 was a *structural* null — real vLLM is throughput-bound, so a searched M\*≈60 and a pinned 128 both sit *above* the operating concurrency and neither cap binds. Repeating search-vs-128 with bigger/slower traffic would reproduce the null; more capacity and a slower ramp push *further* into null territory. The signal needs a cap that **binds below** the operating point.

**Decisions (via Q&A):**
- **Three arms**, not two: A=search, **B-low=M\*32**, B-high=M\*128. User initially worried M\*=8 was an indefensible strawman ("who picks 8, other than to make us look good?"). Resolved by choosing **M\*=32**, justified as the value a latency-conscious admin sizes to the *baseline* operating point (run16 measured ~conc 32–40 at 250 RPM) — defensible, and it binds (< the ~64 knee).
- **Eval window 60 s** (not 100): 100 s with a 120 s period pushes the coherence/edge-detection lag to ~2 cycles during ramps; 60 s keeps it at 1 cycle while doubling samples.
- **Capacity 8**, single model (qwen) — same as run16.

Configs updated (`manifests/vllm-gpu`, `inferno-data/vllm-gpu`, `scripts/vllm-gpu/oc-deploy.sh`), validated, and recorded in memory. The continuous-streaming wiring (`SERVERSIM_CONTINUOUS`, `pass-through`) was already present; parallel activation is actuator-internal.

---

## 2. Detour A — single-replica EKF deadlock (→ tuner off)

First Arm A deploy came up clean (M\*≈50, scale-out 1→2 on the first surge), but every cycle after warm-up logged `optimize failed to find solution: 404`. Stuck at 1 replica through the whole 5× peak.

**Investigation:**
- Tuner logs showed `ill-conditioned fit, holding previous params kappa=+Inf max=1000`. The guard *was* working — but the held params had γ ≈ 0.00133, ~20× the feasible seed (0.0000577).
- User asked the sharp question: *"ill-conditioned even though funcValue is small?"* — exactly the crux. `funcValue` (residual) and `kappa` (Jacobian condition number) are orthogonal: at a **single operating point** the fit passes through the data (`funcValue≈0.08`) but β/γ are unidentifiable (`kappa→+Inf`). The analytical `GuessInitState` fallback then deterministically emits an **infeasible γ**, and the optimizer can't satisfy the SLO at any allocation → 404 every cycle → never scales → deadlock.
- Reproduced across two independent fresh warm-ups — deterministic, structural.

**Decision path:** user chose "keep tuner, replicate run16 procedure" — but that was empirically *falsified* (a fresh warm-up at single-replica baseline drifted to the same infeasible γ and 404'd). Conclusion: at a single-replica low-load baseline the EKF cannot produce a feasible fit; run16 worked only because *its* guess happened to be feasible at its operating point.

**Resolution:** disable the tuner (`NO_TUNER=1` → unset `TUNER_HOST`), run on the run16-converged seeded `perfParms`. Framed as an **isolation** choice — separate dynamic concurrency/scaling from EKF training (the A/B tests the optimizer, not the EKF). The real defect was filed as **[model-tuner#19](https://github.com/llm-inferno/model-tuner/issues/19)**.

---

## 3. Detour B — dead GPU node + pod storm

Mid-ramp, the user flagged "a lot of pods running with unknown state." The optimizer had correctly scaled out (1→3→5), but the **paired vLLM Deployment couldn't place its 5th pod**: node `pokprod-b93r39s1` repeatedly threw `UnexpectedAdmissionError: ... nvlink ... GPU is lost` (allocatable 6 / capacity 8 — a dead GPU), leaving ~200 terminal litter pods.

**Resolution:** scoped `nodeAffinity` exclusion of `pokprod-b93r39s1` on the vLLM Deployment (ample healthy H100 elsewhere). The first Arm A run (peak degraded to 4/5) was torn down; partial data retained as `armA-search-PARTIAL`. Agreed to re-run A clean after B.

---

## 4. Detour C — saturationPolicy (None → PriorityExhaustive)

User noticed: with `unlimited:false`, the optimizer would **fail the cycle** rather than cap at the limit when M\*=32 demand exceeds 8 H100. Confirmed in `optimizer-light` (`SolveGreedy` → unallocated server → `saturationPolicy:"None"` does nothing → `no feasible allocation` → controller skips, no record at peak).

Discussion of options: `unlimited:true` would ignore capacity and could "eat GPUs" (user's concern — valid). The right fix was **`PriorityExhaustive`** with `unlimited:false`: `allocateMaximally` allocates `min(available, wanted)`, so it's **hard-bounded by capacity (8)** — no runaway — but best-effort-fills to the cap and *records* the capped peak with its deficit. User approved.

(Also hit the **load-emulator restart footgun** here — restarting the emulator mid-ramp persisted a phase-adjusted value (483) into `nominal.rpm`. Fixed by the correct order: stop emulator → reset to 250 → restart.)

---

## 5. The three arms (clean)

With tuner off, `PriorityExhaustive`, and `b93r39s1` excluded, all three arms ran clean (30-min profile each, footgun-safe resets between):

- **B-low (M\*=32):** baseline **2 replicas** (vs A's 1), peak **7–8** (hit the 8 cap). Over-provisioning visible from t=0. Pairing reconciler kept the vLLM Deployment replica-locked (2→5→7→8) — a brief per-pod `pair=NONE` lag during the fast ramp was just the 5 s reconcile tick catching up, not a fault (user asked; verified via actuator logs).
- **Arm A re-run (search ≈50):** baseline **1 replica**, clean peak **5 replicas** — the apples-to-apples peak missing from the node-degraded first attempt.
- **B-high (M\*=128):** baseline ~1.4, peak **4.8 = Arm A** — the high cap never binds (occ ~42 ≪ 128), so it allocates like the search. Reproduces run16's null.

Per-arm cycle logs archived via `scripts/vllm-gpu/save-cycle-log.sh`.

---

## 6. Analysis & the offered-vs-throughput question

User asked whether there's a story in offered load (arrival) vs throughput. After extracting all three arms (`analyze.py`):

- The peak "deficit" (21–26%) is **similar across all three arms** and **confounded** — dominated by ramp-lag transients (few peak cycles, replicas catching up) and the known **evaluator window-undercount bias**. Baseline (un-saturated) deficit ≈ 0–10%, roughly the measurement floor.
- The **clean differentiator is the latency profile**: per-replica occupancy orders B-high hottest (42) → A (39) → B-low coolest (25), and ITL tracks it. Per-replica throughput differences are explained by occupancy via Little's law, **not** by the cap throttling hardware throughput.
- Verdict: don't headline throughput; lead with replicas/occupancy/ITL, present offered-vs-throughput as a methodological caveat.

---

## 7. Wrap-up

- **Cluster** torn down (GPUs released; PVC + namespaces preserved). Verified empty.
- **Issue** [model-tuner#19](https://github.com/llm-inferno/model-tuner/issues/19) filed with full evidence (the `funcValue` vs `kappa` logs) and proposed fixes (hold the seed on ill-conditioned fits; investigate the guess's γ; γ feasibility clamp).
- **Report + figure + analysis** written to `experiments/run17/`.
- **Branch** `exp/run17-concurrency-ab` → **[PR #52](https://github.com/llm-inferno/control-loop/pull/52)** (vs `main`).
- **Doc fixes** (in PR #52): CLAUDE.md `--max-num-seqs 32→128` (was stale); operational-notes EKF section gained the run17 γ-infeasible sibling failure + `NO_TUNER` workaround.

### Lessons / reusable patterns
1. **`funcValue` small ≠ identifiable fit** — check the condition number; a single operating point gives a low-residual but rank-deficient fit.
2. **Single-replica EKF cannot identify β/γ** — for experiments isolating scaling from training, run tuner-off on seeded perfParms (`NO_TUNER=1`).
3. **`unlimited:false` + `saturationPolicy:None`** fails the cycle at the cap (no record); use `PriorityExhaustive` to cap-and-record while staying capacity-bounded.
4. **Load-emulator restart footgun** — stop emulator → reset `nominal.rpm`/`load.rpm` → restart, never reset while it's running.
5. On a shared cluster, **scope node exclusions** for dead hardware rather than touching the node (other teams).
6. **Foreground oc polling** for cluster truth; background watchers for trajectory, but verify verdicts in the foreground.
