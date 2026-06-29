# run19 — Autoscaling with the blis simulator, contrasted to real vLLM/GPU (run18)

**Date:** 2026-06-26
**Author:** brainstorming session (control-loop)
**Status:** design — pending implementation plan

## Objective

Re-run **run18 arm A** (the adaptive optimal-concurrency search arm) end-to-end on a
local **kind** cluster, with the **blis `trained-physics` evaluator** standing in for
the real vLLM/H100 server. Two goals:

1. **Demonstrate autoscaling using blis as a server simulator** — the closed control
   loop scales a blis-simulated Qwen2.5-14B server up and down across a 5× load ramp,
   meeting (or visibly breaching) the Bronze SLO.
2. **Contrast blis-as-simulator against the real-GPU run18** — overlay run19's observed
   ITL / TTFT / throughput / replica trajectory against run18 arm A.

This is **not** a concurrency-control study (that is run15/run17/run18). Concurrency
control is *on* (M\* search) only because run18 arm A used it; the variable under study
here is **evaluator backend (blis sim) vs real vLLM**, holding everything else fixed.

## Decisions (locked in brainstorming)

| Choice | Value | Rationale |
|---|---|---|
| Tuner (EKF) | **OFF** (`NO_TUNER`, seeded perfParms) | Matches run18 arm A; isolates evaluator-vs-real; dodges model-tuner#19 (single-replica EKF infeasible γ). |
| Saturation policy | **`pass-through`** | Matches run18's vllm arm — cache/propagate the saturated point so the optimizer reacts to overload. |
| Scope | **Single arm A** (search M\*) | User request; not a concurrency study. |
| Load profile | **Full 30-min** run18 5-phase ramp (no compression) | Cycle counts line up with run18 for the overlay. |
| H100 capacity | **8** | Parity with run18 (blis data currently has 16). |
| Control period | **120 s** | Parity with run18 (~15 cycles over 30 min). |
| M\* search ceiling | **128** | `maxBatchSize=128`, matching run18's real `--max-num-seqs 128`. |
| Replicas at start | **1** | Single deployment, as run18. |

### Scientific framing (important — state this up front in the report)

With the **tuner OFF**, the optimizer's allocation is driven entirely by the *seeded
queueing model* (α/β/γ in `model-data.json`) + offered load + SLO + capacity — **not by
which evaluator runs**. Therefore run19's **replica trajectory is expected to mirror
run18 arm A by construction**. That is the intended result for Goal 1: it shows the
simulated-server loop produces the same control decisions as the real one.

The genuine sim-vs-real contrast (Goal 2) lives in the **observed** metrics the evaluator
reports: ITL, TTFT, throughput. Per the server-sim curve study
(`server-sim/experiments/qwen2.5-14b-h100/REPORT.md`), expect blis to **track real
throughput and ITL closely** but **under-model TTFT** (generic trained-physics α/β
coeffs vs run16-fit), and to **not reproduce the real post-saturation TTFT blow-up**
(both are stable-state models). The report frames the TTFT gap as a known calibration
finding, not a surprise.

## Image situation (point 1 from review — verified against podman)

All five images **exist** in the local **podman** store as **multi-arch manifest lists
(amd64 + arm64)** — the docker CLI here is a podman shim, so `docker images` showed
nothing; `podman images` / `podman manifest inspect` is the source of truth. The arm64
variant is present, so they run on the arm64 kind node. **But** the kind node currently
has **no** inferno images loaded — every image still needs `kind load` regardless.

The decisive detail is the **evaluator's build date vs the patch merge**:

| Image | Built | Reuse? |
|---|---|---|
| `inferno-evaluator` | **2026-06-23 15:08 UTC** | **NO — rebuild.** Predates the blis batch-aware saturation patch (`server-sim` main `b2b4eca`, PR #33, merged **2026-06-24 13:25**). The stale image would still falsely veto qwen at ~0.22 RPS — fatal. |
| `inferno-server-sim` | 2026-06-23 15:00 UTC | Yes — continuous + pass-through already present. |
| `inferno-loop` | 2026-06-23 21:46 UTC | Yes. |
| `inferno-tuner` | 2026-06-20 16:31 UTC | Yes (idle under NO_TUNER anyway). |
| `inferno-optimizer-light` | 2026-06-20 16:28 UTC | Yes. |

**Action:** the **user rebuilds `inferno-evaluator`** from current `server-sim` main
(out of band, before deploy). The deploy script does **not** build images — it only
`kind load`s all five from the podman store (`KIND_EXPERIMENTAL_PROVIDER=podman`). The
script should **assert the evaluator image is newer than the patch merge** (built after
2026-06-24) and fail fast otherwise, so a stale evaluator can't silently veto qwen.

## Components to build (all additive — no existing files change)

### 1. `manifests/blis/dep-blis-qwen.yaml` (new)
Single managed Deployment, `replicas: 1`. Labels:
- `inferno.server.managed: "true"`, `inferno.server.name: blis-qwen`
- `inferno.server.model: qwen_2_5_14b`, `inferno.server.class: Bronze`
- `inferno.server.allocation.accelerator: H100`, `inferno.server.allocation.maxbatchsize: "128"`
- load: `inferno.server.load.rpm/intokens/outtokens = 250/1024/512`; matching `nominal.*`.

Containers (mirror `dep-blis-granite.yaml`):
- **server-sim**: `SERVERSIM_CONTINUOUS=true`, `SERVERSIM_SATURATION_POLICY=pass-through`.
- **evaluator**: `args: ["blis"]`, `LATENCY_BACKEND=trained-physics`,
  `BLIS_CONFIG_FILE`/`HW_CONFIG_FILE` from the blis-config ConfigMap.

### 2. blis-config ConfigMap (new `manifests/blis/configmap-blis-qwen.yaml`, or extend the existing one)
Add a `qwen_2_5_14b` / H100 entry to `blis-config.json`:
- `model: "qwen_2_5_14b"` (**must equal** the `inferno.server.model` label),
  `accelerator: "H100"`, `gpu: "H100"`, `tp: 1`.
- `totalKVBlocks: 14140` (calibrated from the real server: 226,240 tokens / 16),
  `blockSizeTokens: 16`, `maxModelLen: 4096`.
- `maxRunningReqs: 128` (matches run18's `--max-num-seqs 128`), `maxScheduledTokens: 8192`.
- Generic trained-physics `betaCoeffs`/`alphaCoeffs` (same defaults the granite/llama
  entries use).
- `hfConfigPath` → a new Qwen2.5-14B-Instruct HF config json bundled in the ConfigMap.
  Pull exact values from the model card; expected: `hidden_size 5120`,
  `intermediate_size 13824`, `num_hidden_layers 48`, `num_attention_heads 40`,
  `num_key_value_heads 8`, `vocab_size 152064`, `max_position_embeddings 32768`,
  `rope_theta 1000000.0`, `model_type qwen2`, `torch_dtype bfloat16`,
  `rms_norm_eps 1e-06`.

### 3. `inferno-data/blis/` (extend)
- `model-data.json`: add `qwen_2_5_14b`/H100, `maxBatchSize: 128`, **seeded perfParms**
  `{alpha: 10.645377, beta: 0.041760195, gamma: 0.000057705090}` (the run16-converged
  values from `inferno-data/vllm-gpu/model-data.json`). Seeding satisfies the warm-up
  gate immediately so the optimizer runs from cycle 1 with the tuner off.
- `serviceclass-data.json`: add `Bronze → qwen_2_5_14b` with `slo-itl: 20`,
  `slo-ttft: 1500` (run18 Bronze targets).
- `capacity-data.json`: set `H100` count to **8**.

### 4. Load profile (new)
- `manifests/blis/configmap-load-phases-qwen.yaml`: port run18's 5-phase
  chained-multiplicative ramp — baseline 10 m (1×) → ramp 6 m (→5×) → hold 6 m (5×) →
  ramp-down 4 m → hold (1×); `nominal.rpm = 250`, tokens 1024/512.
- A load-emulator manifest targeting the `blis-qwen` deployment (clone
  `manifests/blis/load-emulator.yaml`, point it at the new workload + phases ConfigMap).

### 5. Scripts
- `scripts/blis/kind-deploy-qwen.sh` (new, modeled on `kind-deploy.sh` but single-arm):
  - `kind load` all five images from the podman store
    (`KIND_EXPERIMENTAL_PROVIDER=podman`); **do not build** — the evaluator is rebuilt by
    the user beforehand. Assert the loaded evaluator image is newer than the patch merge
    (2026-06-24) and fail fast otherwise,
  - **NO_TUNER path**: do not set `TUNER_HOST`; set `INFERNO_WARM_UP_TIMEOUT` to a small
    nonzero value (so the optimizer does not wait for EKF — seeded perfParms pass the
    gate). The tuner container may still be present (idle); `/tune` is simply not called.
  - leave `DEFAULT_MAX_BATCH_SIZE` **unset** (M\* search on),
  - `INFERNO_CONTROL_PERIOD=120`,
  - deploy only the qwen blis workload + its load emulator.
- `scripts/blis/save-cycle-log.sh` (new, kubectl variant of
  `scripts/vllm-gpu/save-cycle-log.sh`): namespaces `inferno`/`infer`, workload
  `blis-qwen`. Archives into `experiments/run19/`:
  - the cycle JSONL (the "dashboard log") → `experiments/run19/armA-search-cycles.jsonl`,
  - all five control-pod container logs (controller, collector, optimizer, actuator,
    tuner) → `logs/`,
  - the workload pod's `server-sim` + `evaluator` sidecar logs → `logs/`,
  - the `load-emulator` pod log and the deploy log → `logs/`.

### 6. `experiments/run19/` deliverables (point 3 from review)
- `experiment-report-2026-06-26-run19.md`, structured like run15, with **two parts**:
  - **Part A — Autoscaling with blis**: config table, target params, methodology/cycle
    alignment, replica + occupancy + SLO trajectory across the ramp; the autoscaling story
    on its own terms.
  - **Part B — Contrast to real vLLM/GPU (run18)**: overlay run19 (blis) vs run18 arm A
    on replicas, ITL, TTFT, throughput; quantify agreement and the expected TTFT
    under-modeling; tie back to the server-sim curve study.
- `gen_report_figs_run19.py` (modeled on run15/run18 plot scripts) → `figs/`.
- `armA-search-cycles.jsonl` + `logs/` (per point 2).
- Optional PDF render of the report (run15 has one).

## Verification / success criteria

- Loop runs ≥1 full 30-min profile with no controller errors; blis evaluator returns
  non-zero throughput across the operating range (confirms the merged saturation patch is
  in the built image — sanity check that it does **not** veto at ~0.22 RPS).
- Replica trajectory scales 1 → ~5 at peak and drains back; qualitatively matches run18
  arm A.
- Cycle JSONL + all container/dashboard logs archived under `experiments/run19/`.
- Two-part report written, with the real-vs-sim overlay figure.

## Risks / open items

- **blis HF config accuracy** — wrong Qwen2.5-14B layer/dim values would skew blis ITL.
  Mitigation: pull exact `config.json` from the model card at implementation time.
- **maxRunningReqs=128 vs calibration's 256** — the calibrated `totalKVBlocks=14140` was
  read from a `--max-num-seqs 256` server; we intentionally cap `maxRunningReqs` at 128 to
  mirror run18's real server. The KV pool is a memory property independent of the batch
  cap, so this is consistent; the blis batch becomes `min(128, B_kv≈176)=128`.
- **Allocation mirrors run18 by construction** (tuner off) — already framed as the
  intended Goal-1 result, not a defect. If we later want blis physics to *drive*
  allocation, that is a separate tuner-ON follow-up (blocked on model-tuner#19).
- **pass-through with blis** — `SERVERSIM_SATURATION_POLICY` governs server-sim's loop, not
  the evaluator, so `pass-through` works with the blis backend; verify the saturated
  `/latest` envelope propagates (cycle-1 coherence-gate zeros are expected on arm switch /
  cold start, as in run18).

## Reproduce (target)

```bash
# (user) rebuild the evaluator image from current server-sim main, then:
scripts/blis/kind-deploy-qwen.sh   # kind-loads all five; asserts evaluator is post-2026-06-24
# after the ~30-min profile completes:
RUN=run19 scripts/blis/save-cycle-log.sh armA-search
# figures + report:
python experiments/run19/gen_report_figs_run19.py
```
