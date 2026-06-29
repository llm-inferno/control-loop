# run19 — blis Autoscaling (contrast to real vLLM) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Re-run run18 arm A (adaptive M\* search) end-to-end on a local kind cluster with the blis `trained-physics` evaluator standing in for the real vLLM/H100 server, then write a two-part report (autoscaling-with-blis; contrast-to-real-vLLM/run18).

**Architecture:** Additive only. New qwen entries in the blis data/config files + a single managed `blis-qwen` Deployment + run18's load profile + a NO_TUNER kind deploy script + a kubectl log-archiver. The optimizer runs on seeded perfParms (tuner OFF), so allocation decisions mirror run18 by construction; the blis evaluator supplies the observed ITL/TTFT/throughput that the report contrasts against run18.

**Tech Stack:** Kubernetes (kind, arm64, podman provider), Go microservice images (prebuilt), JSON/YAML config, bash deploy scripts, Python (matplotlib/pandas) for figures.

## Global Constraints

- **Single arm A only** — M\* search ON: `DEFAULT_MAX_BATCH_SIZE` left **unset** on the controller.
- **Tuner OFF** — `TUNER_HOST` unset on the controller; optimizer uses seeded perfParms every cycle.
- **Saturation policy `pass-through`** on server-sim (`SERVERSIM_SATURATION_POLICY=pass-through`).
- **Model/accelerator:** `qwen_2_5_14b` / `H100`. The blis-config `model` field and the `inferno.server.model` label MUST both be exactly `qwen_2_5_14b`.
- **Seeded perfParms (run16-converged):** `alpha=10.645377, beta=0.041760195, gamma=0.000057705090`.
- **M\* search ceiling / batch cap:** `maxBatchSize=128` (model-data), `maxbatchsize: "128"` (label), `maxRunningReqs=128` (blis-config) — all 128, matching run18's real `--max-num-seqs 128`.
- **blis KV calibration:** `totalKVBlocks=14140`, `blockSizeTokens=16`, `maxModelLen=4096`.
- **Bronze SLO:** ITL ≤ 20 ms, TTFT ≤ 1500 ms.
- **Capacity:** H100 count = **8**.
- **Control period:** 120 s. **Load profile:** run18 5-phase 5× ramp, `nominal.rpm=250`, tokens 1024/512, ~30 min.
- **Namespaces:** `inferno` (control pod) / `infer` (workload) — the kind convention.
- **Branch:** `feat/run19-blis-autoscaling` (already checked out).
- **Evaluator image prerequisite:** the user rebuilds `inferno-evaluator` from current `server-sim` main (has the batch-aware saturation patch, `b2b4eca`, merged 2026-06-24) **before** the live run. The deploy script must NOT build images; it loads them and asserts the evaluator is newer than 2026-06-24.

---

### Task 1: blis data — qwen model, SLO, capacity

**Files:**
- Modify: `inferno-data/blis/model-data.json`
- Modify: `inferno-data/blis/serviceclass-data.json`
- Modify: `inferno-data/blis/capacity-data.json`

**Interfaces:**
- Produces: a `qwen_2_5_14b`/`H100` model entry (`maxBatchSize=128`, seeded perfParms) consumed by the optimizer; a `Bronze → qwen_2_5_14b` SLO target (20/1500) consumed by the monitor's SLO lookup and the optimizer; `H100` capacity = 8.

- [ ] **Step 1: Add the qwen model entry with seeded perfParms.**

Replace the contents of `inferno-data/blis/model-data.json` with:

```json
{
  "models": [
    {
      "name": "granite_8b",
      "acc": "H100",
      "accCount": 1,
      "maxBatchSize": 128
    },
    {
      "name": "granite_8b",
      "acc": "A100",
      "accCount": 1,
      "maxBatchSize": 128
    },
    {
      "name": "llama_13b",
      "acc": "H100",
      "accCount": 1,
      "maxBatchSize": 128
    },
    {
      "name": "qwen_2_5_14b",
      "acc": "H100",
      "accCount": 1,
      "maxBatchSize": 128,
      "perfParms": {
        "alpha": 10.645377,
        "beta": 0.041760195,
        "gamma": 0.000057705090
      }
    }
  ]
}
```

- [ ] **Step 2: Add the Bronze→qwen SLO target.**

In `inferno-data/blis/serviceclass-data.json`, add a third `modelTarget` to the existing `Bronze` service class (after the `llama_13b` entry, inside its `modelTargets` array):

```json
        {
          "model": "qwen_2_5_14b",
          "slo-itl": 20,
          "slo-ttft": 1500
        }
```

The resulting `Bronze` block must read:

```json
    {
      "name": "Bronze",
      "priority": 2,
      "modelTargets": [
        {
          "model": "granite_8b",
          "slo-itl": 20,
          "slo-ttft": 1000
        },
        {
          "model": "llama_13b",
          "slo-itl": 14,
          "slo-ttft": 40
        },
        {
          "model": "qwen_2_5_14b",
          "slo-itl": 20,
          "slo-ttft": 1500
        }
      ]
    }
```

- [ ] **Step 3: Set H100 capacity to 8.**

In `inferno-data/blis/capacity-data.json`, change the `H100` count from `16` to `8`:

```json
{
  "count": [
    {
      "type": "A100",
      "count": 8
    },
    {
      "type": "H100",
      "count": 8
    }
  ]
}
```

- [ ] **Step 4: Validate all three JSON files parse.**

Run:
```bash
for f in inferno-data/blis/model-data.json inferno-data/blis/serviceclass-data.json inferno-data/blis/capacity-data.json; do python3 -m json.tool "$f" >/dev/null && echo "OK $f"; done
```
Expected: three `OK ...` lines, no traceback.

- [ ] **Step 5: Commit.**

```bash
git add inferno-data/blis/model-data.json inferno-data/blis/serviceclass-data.json inferno-data/blis/capacity-data.json
git commit -m "feat(run19): add qwen_2_5_14b to blis data (seeded perfParms, Bronze SLO, H100=8)"
```

---

### Task 2: blis-config ConfigMap for qwen + Qwen HF config

**Files:**
- Create: `manifests/blis/configmap-blis-qwen.yaml`

**Interfaces:**
- Consumes: nothing (self-contained).
- Produces: ConfigMap `server-sim-blis-qwen` (namespace `infer`) with `blis-config.json` (entry `model=qwen_2_5_14b`), `hardware_config.json` (H100), and `Qwen2.5-14B-Instruct.json` (HF config). Mounted at `/app/config` by Task 3's deployment.

- [ ] **Step 1: Create the ConfigMap.**

Create `manifests/blis/configmap-blis-qwen.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: server-sim-blis-qwen
  namespace: infer
data:
  # Single (model, accelerator) entry for run19: qwen_2_5_14b / H100.
  # "model" must match the inferno.server.model deployment label (qwen_2_5_14b).
  # totalKVBlocks=14140 calibrated from the real server (226,240 tokens / 16).
  # maxRunningReqs=128 mirrors run18's real vLLM --max-num-seqs 128.
  # betaCoeffs/alphaCoeffs are the generic trained-physics defaults (same as the
  # granite/llama entries) — known to under-model TTFT (see run19 report Part B).
  blis-config.json: |
    {
      "models": [
        {
          "accelerator": "H100",
          "model": "qwen_2_5_14b",
          "hfConfigPath": "/app/config/Qwen2.5-14B-Instruct.json",
          "gpu": "H100",
          "tp": 1,
          "totalKVBlocks": 14140,
          "blockSizeTokens": 16,
          "maxRunningReqs": 128,
          "maxScheduledTokens": 8192,
          "maxModelLen": 4096,
          "scheduler": "fcfs",
          "betaCoeffs": [0.15, 0.0, 1.4, 0.75, 32.0, 4.0, 126.0, 482.0, 0.0, 1.9],
          "alphaCoeffs": [15563.0, 777.0, 46.0],
          "simulationHorizon": 300000000,
          "numRequests": 0,
          "seed": 42
        }
      ]
    }

  hardware_config.json: |
    {
      "H100": {
        "TFlopsPeak":  989.5,
        "TFlopsFP8":   1979.0,
        "BwPeakTBs":   3.35,
        "mfuPrefill":  0.45,
        "mfuDecode":   0.30,
        "MemoryGiB":   80.0
      }
    }

  # Qwen2.5-14B-Instruct HF config (values from the model card config.json).
  Qwen2.5-14B-Instruct.json: |
    {
      "architectures": ["Qwen2ForCausalLM"],
      "hidden_act": "silu",
      "hidden_size": 5120,
      "initializer_range": 0.02,
      "intermediate_size": 13824,
      "max_position_embeddings": 32768,
      "model_type": "qwen2",
      "num_attention_heads": 40,
      "num_hidden_layers": 48,
      "num_key_value_heads": 8,
      "rms_norm_eps": 1e-06,
      "rope_theta": 1000000.0,
      "tie_word_embeddings": false,
      "torch_dtype": "bfloat16",
      "use_cache": true,
      "vocab_size": 152064
    }
```

- [ ] **Step 2: Validate the YAML and the three embedded JSON documents.**

Run:
```bash
python3 -c '
import yaml, json, sys
d = yaml.safe_load(open("manifests/blis/configmap-blis-qwen.yaml"))["data"]
for k in ("blis-config.json","hardware_config.json","Qwen2.5-14B-Instruct.json"):
    json.loads(d[k]); print("OK", k)
m = json.loads(d["blis-config.json"])["models"][0]
assert m["model"] == "qwen_2_5_14b" and m["totalKVBlocks"] == 14140 and m["maxRunningReqs"] == 128, m
print("blis-config asserts OK")
'
```
Expected: three `OK ...` lines and `blis-config asserts OK`.

- [ ] **Step 3: Commit.**

```bash
git add manifests/blis/configmap-blis-qwen.yaml
git commit -m "feat(run19): blis-config ConfigMap for qwen_2_5_14b/H100 (KV-calibrated)"
```

---

### Task 3: blis-qwen managed Deployment

**Files:**
- Create: `manifests/blis/dep-blis-qwen.yaml`

**Interfaces:**
- Consumes: ConfigMap `server-sim-blis-qwen` (Task 2).
- Produces: Deployment `blis-qwen` (namespace `infer`) labeled `inferno.server.managed=true`, model `qwen_2_5_14b`, class `Bronze`, accel `H100`, maxbatchsize 128, nominal.rpm 250. Its workload pod runs `server-sim` (continuous, pass-through) + `evaluator` (blis). Consumed by Task 5 (deploy) and Task 6 (log archive, deployment name `blis-qwen`).

- [ ] **Step 1: Create the Deployment manifest.**

Create `manifests/blis/dep-blis-qwen.yaml` (modeled on `dep-blis-granite.yaml`; single replica; pass-through policy; mounts the qwen blis-config):

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: blis-qwen
  namespace: infer
  labels:
    inferno.server.managed: "true"
    inferno.server.name: blis-qwen
    inferno.server.model: qwen_2_5_14b
    inferno.server.class: Bronze
    inferno.server.allocation.accelerator: H100
    inferno.server.allocation.maxbatchsize: "128"
    inferno.server.load.rpm: "250"
    inferno.server.load.intokens: "1024"
    inferno.server.load.outtokens: "512"
    inferno.server.load.nominal.rpm: "250"
    inferno.server.load.nominal.intokens: "1024"
    inferno.server.load.nominal.outtokens: "512"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: blis-qwen
  template:
    metadata:
      labels:
        app: blis-qwen
        inferno.server.model: qwen_2_5_14b
        inferno.server.allocation.accelerator: H100
        inferno.server.allocation.maxbatchsize: "128"
    spec:
      containers:
      - name: server-sim
        image: quay.io/atantawi/inferno-server-sim:latest
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 8080
        env:
        - name: EVALUATOR_URL
          value: "http://localhost:8081"
        - name: SERVERSIM_CONTINUOUS
          value: "true"
        - name: SERVERSIM_SATURATION_POLICY
          value: "pass-through"
        volumeMounts:
        - name: podinfo
          mountPath: /etc/podinfo
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "256Mi"
            cpu: "500m"
      - name: evaluator
        image: quay.io/atantawi/inferno-evaluator:latest
        imagePullPolicy: IfNotPresent
        args: ["blis"]
        ports:
        - containerPort: 8081
        env:
        - name: BLIS_CONFIG_FILE
          value: "/app/config/blis-config.json"
        - name: HW_CONFIG_FILE
          value: "/app/config/hardware_config.json"
        - name: LATENCY_BACKEND
          value: "trained-physics"
        volumeMounts:
        - name: blis-config
          mountPath: /app/config
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "256Mi"
            cpu: "500m"
      volumes:
      - name: blis-config
        configMap:
          name: server-sim-blis-qwen
      - name: podinfo
        downwardAPI:
          items:
          - path: labels
            fieldRef:
              fieldPath: metadata.labels
```

- [ ] **Step 2: Validate the manifest client-side.**

Run:
```bash
kubectl apply --dry-run=client -f manifests/blis/dep-blis-qwen.yaml
```
Expected: `deployment.apps/blis-qwen created (dry run)` (the `infer` namespace must exist or use `--validate=false`; if the namespace is absent client-side dry-run still validates schema).

- [ ] **Step 3: Commit.**

```bash
git add manifests/blis/dep-blis-qwen.yaml
git commit -m "feat(run19): blis-qwen managed Deployment (continuous, pass-through, 1 replica)"
```

---

### Task 4: run18 load profile for run19

**Files:**
- Create: `manifests/blis/configmap-load-phases-qwen.yaml`

**Interfaces:**
- Produces: ConfigMap `load-phases-config` (namespace `inferno`) with the run18 5-phase 5× ramp at `phases.yaml`. Consumed by the existing `manifests/blis/load-emulator.yaml` (mounts `load-phases-config` at `/etc/loadphases`) — reused unchanged.

- [ ] **Step 1: Create the load-phases ConfigMap (run18 5× ramp).**

Create `manifests/blis/configmap-load-phases-qwen.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: load-phases-config
  namespace: inferno
data:
  phases.yaml: |
    # run19 — run18's single-model 5x RAMP profile, reused unchanged so cycle
    # counts line up with run18 arm A for the sim-vs-real overlay.
    # nominal.rpm=250 sets 1x; 5x=1250 RPM. Ratios are chained-multiplicative;
    # hold phases use ratio: 1.0 and ramp phases carry the multiplier change.
    # Sequence (30 min): baseline 250 (10m) -> ramp ->5x=1250 (6m) -> hold 1250
    # (6m) -> ramp back ->250 (4m) -> hold 250 forever (until teardown).
    phases:
      - duration: 10m
        ratio: 1.0       # baseline hold at 1x (250 RPM)
      - duration: 6m
        ratio: 5.0       # ramp 1x -> 5x (250 -> 1250 RPM)
      - duration: 6m
        ratio: 1.0       # hold at 5x (1250 RPM)
      - duration: 4m
        ratio: 0.2       # ramp 5x -> 1x (1250 -> 250 RPM)
      - duration: 0s     # hold at 1x forever
```

- [ ] **Step 2: Validate the YAML and embedded phases.**

Run:
```bash
python3 -c '
import yaml
p = yaml.safe_load(yaml.safe_load(open("manifests/blis/configmap-load-phases-qwen.yaml"))["data"]["phases.yaml"])["phases"]
assert [x.get("ratio") for x in p] == [1.0, 5.0, 1.0, 0.2, None], p
print("phases OK", p)
'
```
Expected: `phases OK [...]`.

- [ ] **Step 3: Commit.**

```bash
git add manifests/blis/configmap-load-phases-qwen.yaml
git commit -m "feat(run19): run18 5x ramp load profile for the blis-qwen run"
```

---

### Task 5: NO_TUNER single-arm kind deploy script

**Files:**
- Create: `scripts/blis/kind-deploy-qwen.sh`

**Interfaces:**
- Consumes: Tasks 1–4 artifacts + `manifests/common/{ns-inferno.yaml,ns-infer.yaml,configmap-tuner.yaml,deploy-loop.yaml}` + `manifests/blis/load-emulator.yaml`.
- Produces: a deployed run19 stack. Sets controller env: `INFERNO_CONTROL_PERIOD=120`, `INFERNO_WARM_UP_TIMEOUT=10`, `INFERNO_CYCLE_LOG=/tmp/inferno-cycles.jsonl`, `TUNER_HOST` unset (NO_TUNER), `DEFAULT_MAX_BATCH_SIZE` unset (search ON).

- [ ] **Step 1: Create the deploy script.**

Create `scripts/blis/kind-deploy-qwen.sh`:

```bash
#!/usr/bin/env bash
# run19 — deploy the inferno control loop + a single blis-qwen workload to kind.
# Single arm A (M* search ON), tuner OFF (seeded perfParms), pass-through saturation.
# Contrasts the blis trained-physics simulator against the real-vLLM run18 arm A.
#
# Prerequisite: the user has rebuilt inferno-evaluator from current server-sim main
# (batch-aware saturation patch b2b4eca, merged 2026-06-24) before running this.
# This script does NOT build images; it loads them and asserts the evaluator is fresh.
#
# Run from the control-loop/ repo root.

set -euo pipefail

CLUSTER=${KIND_CLUSTER:-kind-cluster}
export KIND_EXPERIMENTAL_PROVIDER=podman   # images live in the podman store
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA_DIR="$REPO_ROOT/inferno-data/blis"
COMMON="$REPO_ROOT/manifests/common"
EXP="$REPO_ROOT/manifests/blis"

# --- Freshness gate: evaluator must post-date the blis saturation patch ----------
echo "==> Asserting inferno-evaluator image is newer than the blis saturation patch (2026-06-24)"
EVAL_CREATED="$(podman images --format '{{.Repository}} {{.CreatedAt}}' \
  | awk '$1=="quay.io/atantawi/inferno-evaluator"{ $1=""; sub(/^ /,""); print; exit }')"
if [[ -z "$EVAL_CREATED" ]]; then
  echo "ERROR: quay.io/atantawi/inferno-evaluator:latest not found in podman. Build it first." >&2
  exit 1
fi
# CreatedAt looks like "2026-06-25 13:25:29 +0000 UTC"; compare the YYYY-MM-DD date.
EVAL_DATE="${EVAL_CREATED%% *}"
if [[ "$EVAL_DATE" < "2026-06-24" ]]; then
  echo "ERROR: evaluator image dated $EVAL_DATE predates the blis saturation patch (2026-06-24)." >&2
  echo "       Rebuild inferno-evaluator from current server-sim main, then re-run." >&2
  exit 1
fi
echo "    evaluator image dated $EVAL_DATE — OK"

echo "==> Loading images into kind cluster: $CLUSTER"
kind load docker-image quay.io/atantawi/inferno-loop:latest             --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-optimizer-light:latest  --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-tuner:latest            --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-server-sim:latest       --name "$CLUSTER"
kind load docker-image quay.io/atantawi/inferno-evaluator:latest        --name "$CLUSTER"

echo "==> Creating namespaces"
kubectl apply -f "$COMMON/ns-inferno.yaml"
kubectl apply -f "$COMMON/ns-infer.yaml"

echo "==> Creating inferno ConfigMaps (blis data)"
kubectl create configmap inferno-static-data -n inferno \
  --from-file=accelerator-data.json="$DATA_DIR/accelerator-data.json" \
  --from-file=model-data.json="$DATA_DIR/model-data.json" \
  --from-file=serviceclass-data.json="$DATA_DIR/serviceclass-data.json" \
  --from-file=optimizer-data.json="$DATA_DIR/optimizer-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl create configmap inferno-dynamic-data -n inferno \
  --from-file=capacity-data.json="$DATA_DIR/capacity-data.json" \
  --save-config --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$COMMON/configmap-tuner.yaml"

echo "==> Deploying inferno pod (controller, collector, optimizer, actuator, tuner)"
kubectl apply -f "$COMMON/deploy-loop.yaml"

# run19 controller env: 120s control period (run18 parity), seeded perfParms so
# warm-up is fast, deterministic cycle-log path for the archiver.
kubectl set env deployment/inferno -n inferno -c controller \
  INFERNO_CONTROL_PERIOD=120 \
  INFERNO_WARM_UP_TIMEOUT=10 \
  INFERNO_CYCLE_LOG=/tmp/inferno-cycles.jsonl

# NO_TUNER: unset TUNER_HOST so the controller skips /tune+/merge and the optimizer
# runs on the seeded perfParms every cycle (matches run18 arm A; dodges model-tuner#19).
kubectl set env deployment/inferno -n inferno -c controller TUNER_HOST-

# Arm A: search ON. DEFAULT_MAX_BATCH_SIZE is not set in deploy-loop.yaml, so the
# optimizer searches M* bounded by the model-data maxBatchSize ceiling (128). Remove
# any stale value defensively (harmless if absent).
kubectl set env deployment/inferno -n inferno -c controller DEFAULT_MAX_BATCH_SIZE- || true

kubectl rollout status deployment/inferno -n inferno --timeout=120s

echo "==> Creating blis-qwen workload ConfigMap"
kubectl apply -f "$EXP/configmap-blis-qwen.yaml"

echo "==> Deploying blis-qwen workload (qwen_2_5_14b/H100 Bronze, 1 replica)"
kubectl apply -f "$EXP/dep-blis-qwen.yaml"

echo "==> Deploying load emulator (run18 5x ramp profile)"
kubectl apply -f "$EXP/configmap-load-phases-qwen.yaml"
kubectl delete pod load-emulator -n inferno --ignore-not-found
kubectl apply -f "$EXP/load-emulator.yaml"

echo ""
echo "==> Done. NO_TUNER, search ON, control period 120s."
echo "    Watch controller:  kubectl logs -f -n inferno deployment/inferno -c controller"
echo "    Watch scaling:     kubectl get deployment blis-qwen -n infer -w"
echo "    After ~30 min:     RUN=run19 scripts/blis/save-cycle-log.sh armA-search"
```

- [ ] **Step 2: Syntax-check the script.**

Run:
```bash
bash -n scripts/blis/kind-deploy-qwen.sh && chmod +x scripts/blis/kind-deploy-qwen.sh && echo "syntax OK"
```
Expected: `syntax OK`.

- [ ] **Step 3: Verify the freshness-gate date comparison logic in isolation.**

Run:
```bash
bash -c 'EVAL_DATE=2026-06-23; [[ "$EVAL_DATE" < "2026-06-24" ]] && echo "stale rejected (correct)"; EVAL_DATE=2026-06-25; [[ "$EVAL_DATE" < "2026-06-24" ]] || echo "fresh accepted (correct)"'
```
Expected: `stale rejected (correct)` then `fresh accepted (correct)`.

- [ ] **Step 4: Commit.**

```bash
git add scripts/blis/kind-deploy-qwen.sh
git commit -m "feat(run19): kind deploy script (NO_TUNER, search ON, evaluator freshness gate)"
```

---

### Task 6: kubectl log archiver

**Files:**
- Create: `scripts/blis/save-cycle-log.sh`

**Interfaces:**
- Consumes: a live run19 cluster (namespaces `inferno`/`infer`, deployment `blis-qwen`, cycle log at `/tmp/inferno-cycles.jsonl`).
- Produces: `experiments/<RUN>/<arm>-cycles.jsonl` + `experiments/<RUN>/logs/<arm>-*.log` (all five control containers, the workload's two sidecars, and the load-emulator pod).

- [ ] **Step 1: Create the archiver.**

Create `scripts/blis/save-cycle-log.sh`:

```bash
#!/usr/bin/env bash
# Archive one run's cycle log + all control/workload/emulator container logs from
# the kind cluster to experiments/<RUN>/. Run AFTER the ~30-min profile completes.
#
# Usage:  RUN=run19 scripts/blis/save-cycle-log.sh armA-search
#
# View offline:  cd dashboard && INFERNO_CYCLE_LOG=../experiments/run19/<arm>-cycles.jsonl python dashboard.py

set -euo pipefail

ARM="${1:?usage: save-cycle-log.sh <arm-label>}"
SYS_NS="${SYS_NS:-inferno}"
WORK_NS="${WORK_NS:-infer}"
POD_LOG="${POD_LOG:-/tmp/inferno-cycles.jsonl}"
RUN="${RUN:-run19}"
WORKLOAD="${WORKLOAD:-blis-qwen}"
OUT="$(cd "$(dirname "$0")/../.." && pwd)/experiments/${RUN}"
LOGS="$OUT/logs"

mkdir -p "$LOGS"

# Cycle log (the dashboard JSONL) at the run-dir top level.
kubectl exec -n "$SYS_NS" deployment/inferno -c controller -- cat "$POD_LOG" > "$OUT/${ARM}-cycles.jsonl"
echo "saved $(wc -l < "$OUT/${ARM}-cycles.jsonl" | tr -d ' ') cycle records -> $OUT/${ARM}-cycles.jsonl"

# All five control-pod container logs (tuner present but idle under NO_TUNER).
for c in controller collector optimizer actuator tuner; do
  kubectl logs -n "$SYS_NS" deployment/inferno -c "$c" > "$LOGS/${ARM}-${c}.log" 2>&1 || true
  echo "saved ${c} log -> $LOGS/${ARM}-${c}.log"
done

# Workload pod sidecars: server-sim (traffic gen) + evaluator (blis).
for c in server-sim evaluator; do
  kubectl logs -n "$WORK_NS" deployment/"$WORKLOAD" -c "$c" > "$LOGS/${ARM}-${WORKLOAD}-${c}.log" 2>&1 || true
  echo "saved ${WORKLOAD}/${c} log -> $LOGS/${ARM}-${WORKLOAD}-${c}.log"
done

# Load emulator pod log.
kubectl logs -n "$SYS_NS" pod/load-emulator > "$LOGS/${ARM}-load-emulator.log" 2>&1 || true
echo "saved load-emulator log -> $LOGS/${ARM}-load-emulator.log"
```

- [ ] **Step 2: Syntax-check.**

Run:
```bash
bash -n scripts/blis/save-cycle-log.sh && chmod +x scripts/blis/save-cycle-log.sh && echo "syntax OK"
```
Expected: `syntax OK`.

- [ ] **Step 3: Commit.**

```bash
git add scripts/blis/save-cycle-log.sh
git commit -m "feat(run19): kubectl cycle-log + container-log archiver"
```

---

### Task 7: Execute the run and archive logs

**Files:**
- Produce: `experiments/run19/armA-search-cycles.jsonl`, `experiments/run19/logs/*.log`

**Interfaces:**
- Consumes: Tasks 1–6. **Prerequisite:** the user has rebuilt `inferno-evaluator` from current `server-sim` main.

- [ ] **Step 1: Confirm the rebuilt evaluator is present and fresh.**

Run:
```bash
podman images --format '{{.Repository}}:{{.Tag}} {{.CreatedAt}}' | grep inferno-evaluator
```
Expected: a line dated **after** 2026-06-24. If not, stop — the user must rebuild it first.

- [ ] **Step 2: Deploy.**

Run:
```bash
scripts/blis/kind-deploy-qwen.sh
```
Expected: ends with `==> Done.`; the freshness gate prints `evaluator image dated <date> — OK`; `deployment/inferno` rolls out.

- [ ] **Step 3: Confirm the loop reaches a useful cycle and blis returns non-zero throughput.**

Run (wait ~3–4 min for startup + first 120 s cycle):
```bash
kubectl logs -n inferno deployment/inferno -c controller | grep -E 'cycle|optimize' | tail -20
kubectl logs -n infer deployment/blis-qwen -c evaluator | tail -20
```
Expected: controller logs a completed optimize cycle (no repeated 404/`connection refused`); the evaluator does **not** report `saturation: "bandwidth"` at baseline (~4 RPS/pod) — confirms the merged patch is in the image. If the evaluator vetoes at ~0.22 RPS, the wrong (stale) image was loaded; stop and rebuild.

- [ ] **Step 4: Watch the ramp drive scale-out (background, ~30 min).**

Monitor replicas across the profile:
```bash
kubectl get deployment blis-qwen -n infer -w
```
Expected: replicas rise from 1 toward ~5 during the 5× hold (cycles ~6–12), then drain back to 1 on the down-ramp — qualitatively matching run18 arm A.

- [ ] **Step 5: After the profile completes (~30 min from deploy), archive.**

Run:
```bash
RUN=run19 scripts/blis/save-cycle-log.sh armA-search
```
Expected: `saved N cycle records -> .../experiments/run19/armA-search-cycles.jsonl` with N ≥ ~10, plus the per-container logs.

- [ ] **Step 6: Sanity-check the cycle log has the expected shape.**

Run:
```bash
python3 -c '
import json
rows=[json.loads(l) for l in open("experiments/run19/armA-search-cycles.jsonl")]
print("cycles:", len(rows))
print("max replicas:", max(s["replicas"] for r in rows for s in r["servers"]))
' 2>/dev/null || wc -l experiments/run19/armA-search-cycles.jsonl
```
Expected: a non-trivial cycle count and a max replica count > 1 (scale-out occurred). (If the record schema differs, fall back to the `wc -l` line — Task 8 inspects the schema directly.)

- [ ] **Step 7: Commit the captured data.**

```bash
git add experiments/run19/armA-search-cycles.jsonl experiments/run19/logs
git commit -m "feat(run19): capture blis-qwen cycle log + container logs"
```

---

### Task 8: Figures + two-part report

**Files:**
- Create: `experiments/run19/gen_report_figs_run19.py`
- Create: `experiments/run19/experiment-report-2026-06-26-run19.md`
- Produce: `experiments/run19/figs/*.png`

**Interfaces:**
- Consumes: `experiments/run19/armA-search-cycles.jsonl` (Task 7) and `experiments/run18/armA-search-cycles.jsonl` (for the overlay).

- [ ] **Step 1: Inspect both cycle-log schemas so the plot script reads the right keys.**

Run:
```bash
python3 -c '
import json
for p in ("experiments/run19/armA-search-cycles.jsonl","experiments/run18/armA-search-cycles.jsonl"):
    r=json.loads(open(p).readline()); print(p); print(" top:",list(r)); 
    s=r["servers"][0]; print(" server:",list(s))
'
```
Expected: prints the top-level and per-server keys for both files. Use these exact keys (e.g. `replicas`, `itlAvg`/`ttftAvg`/`tput` or whatever they are named) in Step 2 instead of guessing. Cross-reference `pkg/monitor/record.go` if a field is ambiguous.

- [ ] **Step 2: Write the figure generator.**

Create `experiments/run19/gen_report_figs_run19.py`. Model it on `experiments/run18/plot_run18.py` (read it first for the exact field accessors and phase-band logic). It must produce, into `experiments/run19/figs/`:
- `run19-autoscaling.png` — Part A: run19 replicas + per-replica occupancy + ITL/TTFT vs cycle, with SLO lines (20 ms / 1500 ms) and phase bands.
- `run19-vs-run18.png` — Part B: 2×2 overlay of run19 (blis) vs run18 arm A on replicas, ITL, TTFT, throughput vs cycle.

Use the exact keys discovered in Step 1. Skeleton (fill accessors from Step 1 / `plot_run18.py`):

```python
#!/usr/bin/env python3
"""run19 figures: Part A (blis autoscaling) and Part B (blis vs real-vLLM run18)."""
import json, os
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

HERE = os.path.dirname(__file__)
FIGS = os.path.join(HERE, "figs"); os.makedirs(FIGS, exist_ok=True)

def load(path):
    return [json.loads(l) for l in open(path) if l.strip()]

run19 = load(os.path.join(HERE, "armA-search-cycles.jsonl"))
run18 = load(os.path.join(HERE, "..", "run18", "armA-search-cycles.jsonl"))

# TODO(from Step 1): define accessors matching the real schema, e.g.
#   replicas = lambda r: r["servers"][0]["replicas"]
#   itl      = lambda r: r["servers"][0]["itlAvg"]   # exact key from Step 1
#   ttft     = lambda r: r["servers"][0]["ttftAvg"]
#   tput     = lambda r: r["servers"][0]["throughput"]
#   occ      = lambda r: r["servers"][0]["occPerReplica"]
# ... then build the two figures with phase bands and SLO lines.
```

- [ ] **Step 3: Generate the figures.**

Run:
```bash
python3 experiments/run19/gen_report_figs_run19.py && ls experiments/run19/figs
```
Expected: `run19-autoscaling.png` and `run19-vs-run18.png` listed.

- [ ] **Step 4: Write the two-part report.**

Create `experiments/run19/experiment-report-2026-06-26-run19.md`, modeled on `experiments/run15/experiment-report-2026-06-17-run15.md` (header block, Configuration table, Methodology/cycle-alignment, Findings). It MUST have two top-level parts:

- **Part A — Autoscaling with the blis simulator.** Header (date, cluster=kind arm64 podman, workload=`blis-qwen` qwen_2_5_14b/H100/Bronze, deploy script). Configuration table (the Global Constraints values). Methodology + cycle alignment to the 5-phase profile. The replica/occupancy/ITL/TTFT trajectory (`figs/run19-autoscaling.png`), stating whether the loop met the Bronze SLO and how it scaled 1→~5→1.
- **Part B — Contrast to real vLLM/GPU (run18).** State the framing up front: tuner OFF ⇒ allocation is driven by the shared queueing model, so replica trajectories are expected to coincide; the contrast is in observed metrics. The overlay (`figs/run19-vs-run18.png`). Quantify replica agreement; quantify the ITL/throughput agreement and the **expected TTFT under-modeling** (generic blis α/β vs run16-fit), tying back to the server-sim curve study (`server-sim/experiments/qwen2.5-14b-h100/REPORT.md`). Note neither model reproduces the real post-saturation TTFT blow-up.

Include an **Artifacts** section (cycle JSONL, `logs/`, figs, scripts) and a **Reproduce** block:

```bash
# (user) rebuild inferno-evaluator from current server-sim main, then:
scripts/blis/kind-deploy-qwen.sh
RUN=run19 scripts/blis/save-cycle-log.sh armA-search
python3 experiments/run19/gen_report_figs_run19.py
```

- [ ] **Step 5: Commit.**

```bash
git add experiments/run19/gen_report_figs_run19.py experiments/run19/experiment-report-2026-06-26-run19.md experiments/run19/figs
git commit -m "docs(run19): two-part report (blis autoscaling; contrast to real vLLM) + figures"
```

---

## Self-Review

**Spec coverage:**
- blis-config qwen entry + KV calibration → Task 2 ✓
- inferno-data qwen model/SLO/capacity → Task 1 ✓
- single blis-qwen deployment, pass-through → Task 3 ✓
- run18 load profile → Task 4 ✓
- NO_TUNER + search-ON deploy, evaluator freshness assert → Task 5 ✓
- log archiver (control + workload + emulator) → Task 6 ✓
- live run + capture → Task 7 ✓
- two-part report + figures → Task 8 ✓
- Image situation (build by user, load + assert) → Task 5/Task 7 ✓

**Notes on deviations from the spec:** the spec mentioned a possible new load-emulator manifest; the existing `manifests/blis/load-emulator.yaml` already mounts `load-phases-config` in the `inferno` namespace and drives all managed deployments, so it is reused unchanged and only the phases ConfigMap is new (simpler; YAGNI). An optional PDF render of the report is dropped unless requested (the markdown + figures are the deliverable).

**Placeholder scan:** the only deliberate "fill from real schema" step is Task 8 Step 2's accessors — this is gated by Task 8 Step 1, which prints the exact keys, because the `CycleRecord` field names must be read from the actual log rather than guessed. All config/manifest/script tasks contain complete content.

**Type/name consistency:** deployment name `blis-qwen`, ConfigMap `server-sim-blis-qwen`, model key `qwen_2_5_14b`, namespaces `inferno`/`infer`, cycle-log path `/tmp/inferno-cycles.jsonl`, and arm label `armA-search` are used identically across Tasks 3, 5, 6, 7, 8.
