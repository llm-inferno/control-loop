# vllm-gpu Experiment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `vllm-gpu` experiment scenario — manifests, data files, deploy script, and CLAUDE.md docs — for running the inferno control loop against real H100 vLLM servers (Qwen2.5-14B + Llama-3.1-8B) on the shared OpenShift cluster.

**Architecture:** Pure configuration delta on top of the existing scenarios. Two new namespaces (`inferno-system`, `inferno-workload`) avoid colliding with the other team's `inferno`/`infer`. Manifests, data files, and deploy script are siblings of `vllm-cpu`. The shared `manifests/common/deploy-loop.yaml` is reused by sed-rewriting its hard-coded namespace at apply time.

**Tech Stack:** Kubernetes/OpenShift YAML, JSON, Bash, kubectl/oc, vLLM, existing inferno-loop containers. No Go code changes.

**Spec:** [`docs/superpowers/specs/2026-06-07-vllm-gpu-experiment-design.md`](../specs/2026-06-07-vllm-gpu-experiment-design.md)
**Issue:** [#32](https://github.com/llm-inferno/control-loop/issues/32)
**Branch:** `feat/vllm-gpu-experiment` (already created)

---

## Conventions for this plan

- All file paths are relative to the repo root `/Users/tantawi/Projects/llm-inferno/control-loop/`.
- Validation commands use `kubectl --dry-run=client -o yaml` for YAML and `jq empty` for JSON. They confirm parseability without applying anything.
- Cluster operations (steps that touch the OpenShift cluster) are clearly marked **[CLUSTER]** and only run during the validation task at the end.
- Commits use the existing repo's style (subject prefix `feat`, `chore`, `docs`, `fix`; lowercase imperative; trailing `Co-Authored-By` line per CLAUDE.md guidance).
- Each task ends with a single commit; no partial commits.
- This work is configuration, not code, so there are no unit tests. Validation is parser-level (dry-run, jq) plus a final cluster smoke-test.

---

## File Structure

```
manifests/common/
  ns-inferno-system.yaml        Namespace: inferno-system           [Task 1]
  ns-inferno-workload.yaml      Namespace: inferno-workload         [Task 1]

inferno-data/vllm-gpu/
  accelerator-data.json         H100 only, cost=1.0                 [Task 2]
  model-data.json               qwen_2_5_14b + llama_3_1_8b          [Task 2]
  serviceclass-data.json        Premium llama, Bronze qwen           [Task 2]
  optimizer-data.json           saturationPolicy: None               [Task 2]
  capacity-data.json            H100 count = 6                       [Task 2]

manifests/vllm-gpu/
  pvc-models-cache.yaml         100Gi RWX, ibm-spectrum-scale        [Task 3]
  secret-hf-token.yaml          Stub Secret manifest                 [Task 3]
  rbac-vllm-eval.yaml           SA + Role + RoleBinding              [Task 3]
  deployment-vllm-qwen.yaml     Qwen2.5-14B vLLM Deployment          [Task 4]
  deployment-vllm-llama.yaml    Llama-3.1-8B vLLM Deployment         [Task 4]
  dep-vllm-qwen-server.yaml     Managed wrapper (Bronze)             [Task 5]
  dep-vllm-llama-server.yaml    Managed wrapper (Premium)            [Task 5]
  configmap-vllm-eval.yaml      Eval config for both models          [Task 6]
  configmap-load-phases.yaml    1x->3x->1x five-phase ramp           [Task 6]
  load-emulator.yaml            load-emulator Pod                    [Task 6]

scripts/vllm-gpu/
  oc-deploy.sh                  OpenShift deploy script              [Task 7]

CLAUDE.md                       Add vllm-gpu workloads section       [Task 8]
```

---

## Task 1: Common namespaces

**Files:**
- Create: `manifests/common/ns-inferno-system.yaml`
- Create: `manifests/common/ns-inferno-workload.yaml`

- [ ] **Step 1: Create ns-inferno-system.yaml**

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: inferno-system
```

- [ ] **Step 2: Create ns-inferno-workload.yaml**

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: inferno-workload
```

- [ ] **Step 3: Validate both YAMLs parse**

Run:
```bash
kubectl apply --dry-run=client -f manifests/common/ns-inferno-system.yaml \
  && kubectl apply --dry-run=client -f manifests/common/ns-inferno-workload.yaml
```
Expected output:
```
namespace/inferno-system created (dry run)
namespace/inferno-workload created (dry run)
```

- [ ] **Step 4: Commit**

```bash
git add manifests/common/ns-inferno-system.yaml manifests/common/ns-inferno-workload.yaml
git commit -m "$(cat <<'EOF'
feat(common): add inferno-system + inferno-workload namespaces

The vllm-gpu experiment runs on a shared OpenShift cluster where
'inferno' and 'infer' are already in use by another team. These
namespaces give the new scenario its own slot without colliding.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: inferno-data/vllm-gpu/

**Files:**
- Create: `inferno-data/vllm-gpu/accelerator-data.json`
- Create: `inferno-data/vllm-gpu/model-data.json`
- Create: `inferno-data/vllm-gpu/serviceclass-data.json`
- Create: `inferno-data/vllm-gpu/optimizer-data.json`
- Create: `inferno-data/vllm-gpu/capacity-data.json`

- [ ] **Step 1: Create accelerator-data.json**

```json
{
  "accelerators": [
    {
      "name": "H100",
      "type": "H100",
      "multiplicity": 1,
      "cost": 1.0
    }
  ]
}
```

- [ ] **Step 2: Create model-data.json**

```json
{
  "models": [
    {
      "name": "qwen_2_5_14b",
      "acc": "H100",
      "accCount": 1,
      "maxBatchSize": 32,
      "atTokens": 1024,
      "perfParms": {
        "alpha": 10.645377,
        "beta": 0.041760195,
        "gamma": 0.000057705090
      }
    },
    {
      "name": "llama_3_1_8b",
      "acc": "H100",
      "accCount": 1,
      "maxBatchSize": 32,
      "atTokens": 512,
      "perfParms": {
        "alpha": 6.49,
        "beta": 0.0219,
        "gamma": 0.0000496
      }
    }
  ]
}
```

- [ ] **Step 3: Create serviceclass-data.json**

```json
{
  "serviceClasses": [
    {
      "name": "Premium",
      "priority": 1,
      "modelTargets": [
        {
          "model": "llama_3_1_8b",
          "slo-itl": 9.5,
          "slo-ttft": 50
        }
      ]
    },
    {
      "name": "Bronze",
      "priority": 2,
      "modelTargets": [
        {
          "model": "qwen_2_5_14b",
          "slo-itl": 25,
          "slo-ttft": 100
        }
      ]
    }
  ]
}
```

- [ ] **Step 4: Create optimizer-data.json**

```json
{
  "optimizer": {
    "unlimited": true,
    "heterogeneous": false,
    "milpsolver": false,
    "useCplex": false,
    "delayedBestEffort": false,
    "saturationPolicy": "None"
  }
}
```

- [ ] **Step 5: Create capacity-data.json**

```json
{
  "count": [
    {
      "type": "H100",
      "count": 6
    }
  ]
}
```

- [ ] **Step 6: Validate all five JSON files parse**

Run:
```bash
for f in inferno-data/vllm-gpu/*.json; do
  echo "==> $f"
  jq empty "$f" && echo "  ok"
done
```
Expected output: five `==>` lines each followed by `  ok`. No `parse error` messages.

- [ ] **Step 7: Commit**

```bash
git add inferno-data/vllm-gpu/
git commit -m "$(cat <<'EOF'
feat(vllm-gpu): add inferno-data for H100 / qwen_14b / llama_8b

perfParms are seeded with the converged values from the existing
shared-cluster setup so cycle 1 produces a useful allocation.
maxBatchSize=32 is uniform across both models, matching the
--max-num-seqs flag on the vLLM Deployments.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: PVC, HF token Secret stub, evaluator RBAC

**Files:**
- Create: `manifests/vllm-gpu/pvc-models-cache.yaml`
- Create: `manifests/vllm-gpu/secret-hf-token.yaml`
- Create: `manifests/vllm-gpu/rbac-vllm-eval.yaml`

- [ ] **Step 1: Create pvc-models-cache.yaml**

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: vllm-models-cache
  namespace: inferno-workload
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 100Gi
  storageClassName: ibm-spectrum-scale-fileset
  volumeMode: Filesystem
```

- [ ] **Step 2: Create secret-hf-token.yaml**

```yaml
# Stub Secret manifest. The deploy script (scripts/vllm-gpu/oc-deploy.sh)
# copies the actual token value from infer/hf-token-secret on the cluster.
# This file documents the required Secret name/namespace/key contract;
# applying it directly creates an empty Secret which the vLLM containers
# would reject. Do not apply it standalone — let oc-deploy.sh handle it.
apiVersion: v1
kind: Secret
metadata:
  name: hf-token-secret
  namespace: inferno-workload
type: Opaque
data:
  token: ""
```

- [ ] **Step 3: Create rbac-vllm-eval.yaml**

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: vllm-server-evaluator
  namespace: inferno-workload
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: vllm-server-evaluator
  namespace: inferno-workload
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: vllm-server-evaluator
  namespace: inferno-workload
subjects:
  - kind: ServiceAccount
    name: vllm-server-evaluator
    namespace: inferno-workload
roleRef:
  kind: Role
  name: vllm-server-evaluator
  apiGroup: rbac.authorization.k8s.io
```

- [ ] **Step 4: Validate all three YAMLs parse**

Run:
```bash
for f in manifests/vllm-gpu/pvc-models-cache.yaml manifests/vllm-gpu/secret-hf-token.yaml manifests/vllm-gpu/rbac-vllm-eval.yaml; do
  echo "==> $f"
  kubectl apply --dry-run=client -f "$f"
done
```
Expected output: three `==>` lines, each followed by one or more `... created (dry run)` lines (PVC: 1, Secret: 1, RBAC: 3 — SA + Role + RoleBinding). No errors.

- [ ] **Step 5: Commit**

(This is the first commit on what will be the larger "manifests" diff — committing each subgroup separately keeps reviewable diffs.)

```bash
git add manifests/vllm-gpu/pvc-models-cache.yaml manifests/vllm-gpu/secret-hf-token.yaml manifests/vllm-gpu/rbac-vllm-eval.yaml
git commit -m "$(cat <<'EOF'
feat(vllm-gpu): add PVC, HF token stub, and evaluator RBAC

PVC is RWX 100Gi on ibm-spectrum-scale-fileset (cluster default that
supports RWX). The HF Secret is a stub; the deploy script copies the
real value from infer/hf-token-secret. RBAC mirrors the vllm-cpu
pattern, scoped to the new inferno-workload namespace.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: vLLM Deployments (Qwen, Llama)

**Files:**
- Create: `manifests/vllm-gpu/deployment-vllm-qwen.yaml`
- Create: `manifests/vllm-gpu/deployment-vllm-llama.yaml`

These two Deployments are adapted from the existing `infer/vllm-qwen-14b-gpu` and `infer/vllm-llama-gpu` (known to work on this cluster), with namespace changed to `inferno-workload`, `--max-num-seqs` aligned to 32 for Llama (was 16), and image kept at the pinned `vllm/vllm-openai:v0.21.0`.

- [ ] **Step 1: Create deployment-vllm-qwen.yaml**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm-qwen-14b-gpu
  namespace: inferno-workload
  labels:
    app: vllm-qwen-14b-gpu
    inferno.vllm.model: qwen
    inferno.vllm.accelerator: H100
spec:
  progressDeadlineSeconds: 900
  replicas: 1
  revisionHistoryLimit: 3
  selector:
    matchLabels:
      app: vllm-qwen-14b-gpu
  strategy:
    type: Recreate
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8000"
        prometheus.io/path: /metrics
      labels:
        app: vllm-qwen-14b-gpu
        inferno.vllm.model: qwen
        inferno.vllm.accelerator: H100
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: kubernetes.io/hostname
                    operator: NotIn
                    values:
                      - pokprod-b93r43s0
                      - pokprod-b93r43s1
                      - pokprod-b93r43s2
                      - pokprod-b93r43s3
                      - pokprod-b93r44s0
                      - pokprod-b93r44s1
                      - pokprod-b93r44s2
                      - pokprod-b93r44s3
      tolerations:
        - effect: NoSchedule
          key: nvidia.com/gpu
          operator: Exists
      terminationGracePeriodSeconds: 120
      containers:
        - name: server
          image: vllm/vllm-openai:v0.21.0
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "-c"]
          args:
            - >
              vllm serve Qwen/Qwen2.5-14B-Instruct
              --served-model-name qwen
              --trust-remote-code
              --download-dir /models-cache
              --no-enable-prefix-caching
              --dtype bfloat16
              --max-model-len 4096
              --max-num-seqs 32
              --gpu-memory-utilization 0.90
              --port 8000
              --generation-config vllm
          env:
            - name: HUGGING_FACE_HUB_TOKEN
              valueFrom:
                secretKeyRef:
                  key: token
                  name: hf-token-secret
            - name: HOME
              value: /models-cache
            - name: VLLM_PORT
              value: "8000"
            - name: TORCHINDUCTOR_CACHE_DIR
              value: /models-cache/.cache/torchinductor
            - name: XDG_CACHE_HOME
              value: /models-cache/.cache
            - name: TORCH_HOME
              value: /models-cache/.torch
          ports:
            - containerPort: 8000
              name: http
              protocol: TCP
          resources:
            requests:
              cpu: "6"
              memory: 48Gi
              nvidia.com/gpu: "1"
            limits:
              cpu: "8"
              memory: 64Gi
              nvidia.com/gpu: "1"
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          startupProbe:
            httpGet:
              path: /health
              port: http
            failureThreshold: 30
            periodSeconds: 30
            timeoutSeconds: 1
          readinessProbe:
            httpGet:
              path: /health
              port: http
            failureThreshold: 3
            periodSeconds: 30
            timeoutSeconds: 5
          livenessProbe:
            httpGet:
              path: /health
              port: http
            failureThreshold: 3
            periodSeconds: 100
            timeoutSeconds: 8
          volumeMounts:
            - name: models-cache
              mountPath: /models-cache
            - name: shm
              mountPath: /dev/shm
      volumes:
        - name: models-cache
          persistentVolumeClaim:
            claimName: vllm-models-cache
        - name: shm
          emptyDir:
            medium: Memory
            sizeLimit: 4Gi
```

- [ ] **Step 2: Create deployment-vllm-llama.yaml**

Identical to qwen except: `name`, `app` labels, model name, `--max-model-len 8192`, memory requests/limits (32Gi/48Gi).

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm-llama-gpu
  namespace: inferno-workload
  labels:
    app: vllm-llama-gpu
    inferno.vllm.model: llama
    inferno.vllm.accelerator: H100
spec:
  progressDeadlineSeconds: 900
  replicas: 1
  revisionHistoryLimit: 3
  selector:
    matchLabels:
      app: vllm-llama-gpu
  strategy:
    type: Recreate
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8000"
        prometheus.io/path: /metrics
      labels:
        app: vllm-llama-gpu
        inferno.vllm.model: llama
        inferno.vllm.accelerator: H100
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: kubernetes.io/hostname
                    operator: NotIn
                    values:
                      - pokprod-b93r43s0
                      - pokprod-b93r43s1
                      - pokprod-b93r43s2
                      - pokprod-b93r43s3
                      - pokprod-b93r44s0
                      - pokprod-b93r44s1
                      - pokprod-b93r44s2
                      - pokprod-b93r44s3
      tolerations:
        - effect: NoSchedule
          key: nvidia.com/gpu
          operator: Exists
      terminationGracePeriodSeconds: 120
      containers:
        - name: server
          image: vllm/vllm-openai:v0.21.0
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "-c"]
          args:
            - >
              vllm serve unsloth/Meta-Llama-3.1-8B-Instruct
              --served-model-name llama
              --trust-remote-code
              --download-dir /models-cache
              --no-enable-prefix-caching
              --dtype bfloat16
              --max-model-len 8192
              --max-num-seqs 32
              --gpu-memory-utilization 0.90
              --port 8000
              --generation-config vllm
          env:
            - name: HUGGING_FACE_HUB_TOKEN
              valueFrom:
                secretKeyRef:
                  key: token
                  name: hf-token-secret
            - name: HOME
              value: /models-cache
            - name: VLLM_PORT
              value: "8000"
            - name: TORCHINDUCTOR_CACHE_DIR
              value: /models-cache/.cache/torchinductor
            - name: XDG_CACHE_HOME
              value: /models-cache/.cache
            - name: TORCH_HOME
              value: /models-cache/.torch
          ports:
            - containerPort: 8000
              name: http
              protocol: TCP
          resources:
            requests:
              cpu: "6"
              memory: 32Gi
              nvidia.com/gpu: "1"
            limits:
              cpu: "8"
              memory: 48Gi
              nvidia.com/gpu: "1"
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          startupProbe:
            httpGet:
              path: /health
              port: http
            failureThreshold: 30
            periodSeconds: 30
            timeoutSeconds: 1
          readinessProbe:
            httpGet:
              path: /health
              port: http
            failureThreshold: 3
            periodSeconds: 30
            timeoutSeconds: 5
          livenessProbe:
            httpGet:
              path: /health
              port: http
            failureThreshold: 3
            periodSeconds: 100
            timeoutSeconds: 8
          volumeMounts:
            - name: models-cache
              mountPath: /models-cache
            - name: shm
              mountPath: /dev/shm
      volumes:
        - name: models-cache
          persistentVolumeClaim:
            claimName: vllm-models-cache
        - name: shm
          emptyDir:
            medium: Memory
            sizeLimit: 4Gi
```

- [ ] **Step 3: Validate both YAMLs parse**

Run:
```bash
kubectl apply --dry-run=client -f manifests/vllm-gpu/deployment-vllm-qwen.yaml \
  && kubectl apply --dry-run=client -f manifests/vllm-gpu/deployment-vllm-llama.yaml
```
Expected output:
```
deployment.apps/vllm-qwen-14b-gpu created (dry run)
deployment.apps/vllm-llama-gpu created (dry run)
```

- [ ] **Step 4: Commit**

```bash
git add manifests/vllm-gpu/deployment-vllm-qwen.yaml manifests/vllm-gpu/deployment-vllm-llama.yaml
git commit -m "$(cat <<'EOF'
feat(vllm-gpu): add vLLM Deployments for Qwen2.5-14B and Llama-3.1-8B

Both Deployments use vllm/vllm-openai:v0.21.0 on H100 with
--max-num-seqs 32, mounting the shared vllm-models-cache PVC and
reading HUGGING_FACE_HUB_TOKEN from hf-token-secret. The Llama
Deployment uses unsloth/Meta-Llama-3.1-8B-Instruct (the non-gated
fork) so the HF gate doesn't block deploys. Node affinity excludes
the eight reserved cluster nodes to maintain cluster etiquette.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Managed Deployments (server-sim + vllm-server evaluator)

**Files:**
- Create: `manifests/vllm-gpu/dep-vllm-qwen-server.yaml`
- Create: `manifests/vllm-gpu/dep-vllm-llama-server.yaml`

These wrap each vLLM Deployment with the standard inferno managed-deployment two-sidecar pattern, carrying the labels the controller and pairing reconciler read.

- [ ] **Step 1: Create dep-vllm-qwen-server.yaml**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm-qwen-14b-server
  namespace: inferno-workload
  labels:
    inferno.server.managed: "true"
    inferno.server.name: vllm-qwen-14b
    inferno.server.model: qwen_2_5_14b
    inferno.server.class: Bronze
    inferno.server.evaluator: vllm-server
    inferno.server.allocation.accelerator: H100
    inferno.server.allocation.maxbatchsize: "32"
    inferno.server.allocation.maxqueuesize: "64"
    inferno.server.vllm-deployment: vllm-qwen-14b-gpu
    inferno.server.vllm-namespace: inferno-workload
    inferno.server.load.rpm: "60"
    inferno.server.load.intokens: "2048"
    inferno.server.load.outtokens: "1024"
    inferno.server.load.nominal.rpm: "60"
    inferno.server.load.nominal.intokens: "2048"
    inferno.server.load.nominal.outtokens: "1024"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: vllm-qwen-14b-server
  template:
    metadata:
      labels:
        app: vllm-qwen-14b-server
        inferno.server.vllm-deployment: vllm-qwen-14b-gpu
    spec:
      serviceAccountName: vllm-server-evaluator
      containers:
        - name: server-sim
          image: quay.io/atantawi/inferno-server-sim:latest
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 8080
          env:
            - name: EVALUATOR_URL
              value: http://localhost:8081
            - name: NOISE_ENABLED
              value: "false"
          resources:
            requests:
              memory: 128Mi
              cpu: 100m
            limits:
              memory: 256Mi
              cpu: 500m
        - name: evaluator
          image: quay.io/atantawi/inferno-evaluator:latest
          imagePullPolicy: IfNotPresent
          args: ["vllm-server"]
          ports:
            - containerPort: 8081
          env:
            - name: EVALUATOR_PORT
              value: "8081"
            - name: VLLM_EVAL_CONFIG_FILE
              value: /app/config/vllm-eval-config.json
            - name: POD_NAME
              valueFrom:
                fieldRef: { fieldPath: metadata.name }
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef: { fieldPath: metadata.namespace }
            - name: VLLM_NAMESPACE
              value: inferno-workload
          volumeMounts:
            - name: vllm-server-config
              mountPath: /app/config
            - name: podinfo
              mountPath: /etc/podinfo
          resources:
            requests:
              memory: 256Mi
              cpu: 200m
            limits:
              memory: 512Mi
              cpu: 1000m
      volumes:
        - name: vllm-server-config
          configMap:
            name: vllm-server-eval-config
        - name: podinfo
          downwardAPI:
            items:
              - path: pair-id
                fieldRef:
                  fieldPath: "metadata.labels['inferno.server.pair-id']"
              - path: vllm-deployment
                fieldRef:
                  fieldPath: "metadata.labels['inferno.server.vllm-deployment']"
```

- [ ] **Step 2: Create dep-vllm-llama-server.yaml**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm-llama-server
  namespace: inferno-workload
  labels:
    inferno.server.managed: "true"
    inferno.server.name: vllm-llama
    inferno.server.model: llama_3_1_8b
    inferno.server.class: Premium
    inferno.server.evaluator: vllm-server
    inferno.server.allocation.accelerator: H100
    inferno.server.allocation.maxbatchsize: "32"
    inferno.server.allocation.maxqueuesize: "64"
    inferno.server.vllm-deployment: vllm-llama-gpu
    inferno.server.vllm-namespace: inferno-workload
    inferno.server.load.rpm: "90"
    inferno.server.load.intokens: "4096"
    inferno.server.load.outtokens: "2048"
    inferno.server.load.nominal.rpm: "90"
    inferno.server.load.nominal.intokens: "4096"
    inferno.server.load.nominal.outtokens: "2048"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: vllm-llama-server
  template:
    metadata:
      labels:
        app: vllm-llama-server
        inferno.server.vllm-deployment: vllm-llama-gpu
    spec:
      serviceAccountName: vllm-server-evaluator
      containers:
        - name: server-sim
          image: quay.io/atantawi/inferno-server-sim:latest
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 8080
          env:
            - name: EVALUATOR_URL
              value: http://localhost:8081
            - name: NOISE_ENABLED
              value: "false"
          resources:
            requests:
              memory: 128Mi
              cpu: 100m
            limits:
              memory: 256Mi
              cpu: 500m
        - name: evaluator
          image: quay.io/atantawi/inferno-evaluator:latest
          imagePullPolicy: IfNotPresent
          args: ["vllm-server"]
          ports:
            - containerPort: 8081
          env:
            - name: EVALUATOR_PORT
              value: "8081"
            - name: VLLM_EVAL_CONFIG_FILE
              value: /app/config/vllm-eval-config.json
            - name: POD_NAME
              valueFrom:
                fieldRef: { fieldPath: metadata.name }
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef: { fieldPath: metadata.namespace }
            - name: VLLM_NAMESPACE
              value: inferno-workload
          volumeMounts:
            - name: vllm-server-config
              mountPath: /app/config
            - name: podinfo
              mountPath: /etc/podinfo
          resources:
            requests:
              memory: 256Mi
              cpu: 200m
            limits:
              memory: 512Mi
              cpu: 1000m
      volumes:
        - name: vllm-server-config
          configMap:
            name: vllm-server-eval-config
        - name: podinfo
          downwardAPI:
            items:
              - path: pair-id
                fieldRef:
                  fieldPath: "metadata.labels['inferno.server.pair-id']"
              - path: vllm-deployment
                fieldRef:
                  fieldPath: "metadata.labels['inferno.server.vllm-deployment']"
```

- [ ] **Step 3: Validate both YAMLs parse**

Run:
```bash
kubectl apply --dry-run=client -f manifests/vllm-gpu/dep-vllm-qwen-server.yaml \
  && kubectl apply --dry-run=client -f manifests/vllm-gpu/dep-vllm-llama-server.yaml
```
Expected output:
```
deployment.apps/vllm-qwen-14b-server created (dry run)
deployment.apps/vllm-llama-server created (dry run)
```

- [ ] **Step 4: Commit**

```bash
git add manifests/vllm-gpu/dep-vllm-qwen-server.yaml manifests/vllm-gpu/dep-vllm-llama-server.yaml
git commit -m "$(cat <<'EOF'
feat(vllm-gpu): add managed Deployments for qwen and llama

Standard server-sim + vllm-server evaluator pattern, paired with the
vLLM Deployments via inferno.server.vllm-deployment / vllm-namespace
labels. Bronze for qwen (RPM 60, in 2048, out 1024), Premium for
llama (RPM 90, in 4096, out 2048). Both declare maxBatchSize=32 to
match the vLLM --max-num-seqs flag.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Eval ConfigMap, load-phases ConfigMap, load-emulator Pod

**Files:**
- Create: `manifests/vllm-gpu/configmap-vllm-eval.yaml`
- Create: `manifests/vllm-gpu/configmap-load-phases.yaml`
- Create: `manifests/vllm-gpu/load-emulator.yaml`

- [ ] **Step 1: Create configmap-vllm-eval.yaml**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: vllm-server-eval-config
  namespace: inferno-workload
data:
  vllm-eval-config.json: |
    {
      "configs": [
        {
          "accelerator": "H100",
          "model": "qwen_2_5_14b",
          "vllmServedModelName": "qwen",
          "vllmPort": 8000,
          "warmupSec": 0,
          "minWindowSec": 0,
          "maxWindowSec": 30,
          "targetSamples": 0,
          "minSamples": 3,
          "ignoreEOS": true,
          "queueTimeMetric": "vllm:request_queue_time_seconds",
          "inputTokenDistribution":  "uniform-bounded",
          "outputTokenDistribution": "uniform-bounded"
        },
        {
          "accelerator": "H100",
          "model": "llama_3_1_8b",
          "vllmServedModelName": "llama",
          "vllmPort": 8000,
          "warmupSec": 0,
          "minWindowSec": 0,
          "maxWindowSec": 30,
          "targetSamples": 0,
          "minSamples": 3,
          "ignoreEOS": true,
          "queueTimeMetric": "vllm:request_queue_time_seconds",
          "inputTokenDistribution":  "uniform-bounded",
          "outputTokenDistribution": "uniform-bounded"
        }
      ]
    }
```

- [ ] **Step 2: Create configmap-load-phases.yaml**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: load-phases-config
  namespace: inferno-system
data:
  phases.yaml: |
    # vllm-gpu experiment: 5-phase 1x -> 3x -> 1x ramp, 6 minutes per phase.
    # Total active phase sequence ~24 min, then hold at 1x indefinitely.
    # Stays under the 30-min gpu-reaper.io idle threshold.
    #
    # Ratios are chained-multiplicative; hold phases use ratio: 1.0 and
    # ramp phases carry the multiplier change. The load emulator
    # interpolates linearly within a phase based on its ratio.
    phases:
      - duration: 6m
        ratio: 1.0       # hold at 1x
      - duration: 6m
        ratio: 3.0       # linear ramp 1x -> 3x
      - duration: 6m
        ratio: 1.0       # hold at 3x
      - duration: 6m
        ratio: 0.333     # linear ramp 3x -> 1x
      - duration: 0s     # hold at 1x forever
```

- [ ] **Step 3: Create load-emulator.yaml**

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: load-emulator
  namespace: inferno-system
spec:
  serviceAccountName: inferno
  containers:
    - name: loademulator
      image: quay.io/atantawi/inferno-loop:latest
      imagePullPolicy: IfNotPresent
      command: ["loademulator"]
      env:
        - name: INFERNO_LOAD_INTERVAL
          value: "30"
        - name: INFERNO_LOAD_ALPHA
          value: "0.1"
        - name: INFERNO_LOAD_THETA
          value: "0.9"
        - name: INFERNO_LOAD_SKEW
          value: "0.0"
        - name: INFERNO_STARTUP_DELAY
          value: "0"
        - name: INFERNO_LOAD_PHASES
          value: "/etc/loadphases/phases.yaml"
      volumeMounts:
        - name: load-phases-config
          mountPath: /etc/loadphases
          readOnly: true
      resources:
        requests:
          memory: 512Mi
          cpu: 100m
        limits:
          memory: 1Gi
          cpu: 500m
  volumes:
    - name: load-phases-config
      configMap:
        name: load-phases-config
        optional: true
```

- [ ] **Step 4: Validate all three YAMLs parse**

Run:
```bash
kubectl apply --dry-run=client \
  -f manifests/vllm-gpu/configmap-vllm-eval.yaml \
  -f manifests/vllm-gpu/configmap-load-phases.yaml \
  -f manifests/vllm-gpu/load-emulator.yaml
```
Expected output:
```
configmap/vllm-server-eval-config created (dry run)
configmap/load-phases-config created (dry run)
pod/load-emulator created (dry run)
```

- [ ] **Step 5: Validate the embedded eval JSON parses**

The vllm-server evaluator validates the JSON at startup; catching syntax errors here saves a deploy round-trip.

Run:
```bash
yq -r '.data."vllm-eval-config.json"' manifests/vllm-gpu/configmap-vllm-eval.yaml | jq empty && echo ok
```
Expected output: `ok` (no `parse error`).

If `yq` is not installed, equivalent fallback:
```bash
python3 -c "
import yaml, json, sys
with open('manifests/vllm-gpu/configmap-vllm-eval.yaml') as f:
    cm = yaml.safe_load(f)
json.loads(cm['data']['vllm-eval-config.json'])
print('ok')"
```

- [ ] **Step 6: Commit**

```bash
git add manifests/vllm-gpu/configmap-vllm-eval.yaml \
        manifests/vllm-gpu/configmap-load-phases.yaml \
        manifests/vllm-gpu/load-emulator.yaml
git commit -m "$(cat <<'EOF'
feat(vllm-gpu): add eval ConfigMap, load-phases, and load-emulator

Eval config covers both H100/qwen_2_5_14b and H100/llama_3_1_8b with
uniform-bounded token sampling (mean preserved, ±50% range) and a
30-second measurement window. Load-phases configures the run10-style
1x->3x->1x ramp at 6 minutes per phase. The load-emulator Pod runs
in inferno-system with INFERNO_LOAD_INTERVAL=30 (finer than the
120s control period so ramps are visible to the controller).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: OpenShift deploy script

**Files:**
- Create: `scripts/vllm-gpu/oc-deploy.sh`

The script applies all the manifests in the right order, copies the HF token from the existing `infer/` namespace, and sed-rewrites the namespace in the shared `manifests/common/deploy-loop.yaml` and `configmap-tuner.yaml` so they target `inferno-system` instead of the hard-coded `inferno`.

- [ ] **Step 1: Inspect the existing tuner ConfigMap and deploy-loop.yaml to confirm namespace fields the sed needs to rewrite**

Run:
```bash
grep -nE 'namespace:|name: inferno$' manifests/common/configmap-tuner.yaml manifests/common/deploy-loop.yaml
```

Expected: every `namespace: inferno` and the ClusterRoleBinding `subjects[0].namespace: inferno` in `deploy-loop.yaml`. The sed in the script rewrites all `namespace: inferno` (literal) → `namespace: inferno-system`, which is safe since `inferno-workload` is never substring-matched by the literal `inferno` followed by end-of-line / non-name char (we anchor with `\<` or use exact line match — see step 2).

- [ ] **Step 2: Create scripts/vllm-gpu/oc-deploy.sh**

```bash
#!/usr/bin/env bash
# Deploy the inferno control loop + vllm-server evaluator workload to a shared
# OpenShift cluster, running real H100 vLLM servers (Qwen2.5-14B + Llama-3.1-8B).
#
# Differences from scripts/vllm-cpu/kind-deploy.sh:
#   - No `kind load`; OpenShift pulls images from the registry on demand.
#   - Two new namespaces (inferno-system, inferno-workload) so we don't collide
#     with the other team's existing inferno/infer namespaces on the cluster.
#   - The shared manifests/common/deploy-loop.yaml and configmap-tuner.yaml
#     hard-code namespace: inferno; we sed-rewrite them at apply time.
#   - The HF_TOKEN secret is copied from infer/hf-token-secret rather than
#     committed to git.
#
# Run from the control-loop/ repo root.
# Prerequisites:
#   - oc whoami succeeds against the target cluster.
#   - The user has read access to infer/hf-token-secret (script will fail with
#     a clear message otherwise).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA_DIR="$REPO_ROOT/inferno-data/vllm-gpu"
COMMON="$REPO_ROOT/manifests/common"
EXP="$REPO_ROOT/manifests/vllm-gpu"

SYS_NS="inferno-system"
WORK_NS="inferno-workload"

# Rewrite hard-coded `namespace: inferno` (and the matching ClusterRoleBinding
# subject) in shared common YAMLs to target the new system namespace. We use
# sed with a word boundary substitute so `inferno-workload` (which appears
# nowhere in the common files anyway) cannot accidentally match.
rewrite_ns() {
  sed "s/^\(  *\)namespace: inferno$/\1namespace: ${SYS_NS}/g
       s/^\(  *\)name: inferno$/\1name: inferno/g"
}

echo "==> Pre-flight: oc whoami"
oc whoami
echo "    server: $(oc whoami --show-server)"

echo "==> Creating namespaces"
oc apply -f "$COMMON/ns-inferno-system.yaml"
oc apply -f "$COMMON/ns-inferno-workload.yaml"

echo "==> Copying HF token secret from infer/hf-token-secret"
if ! oc get secret hf-token-secret -n infer >/dev/null 2>&1; then
  echo "ERROR: cannot read infer/hf-token-secret. Either:" >&2
  echo "  - ask the cluster admin to grant 'get secrets' on the infer namespace, OR" >&2
  echo "  - manually create ${WORK_NS}/hf-token-secret with key 'token' before re-running." >&2
  exit 1
fi
oc get secret hf-token-secret -n infer -o yaml \
  | sed "s/namespace: infer$/namespace: ${WORK_NS}/" \
  | grep -vE '^  (uid|resourceVersion|creationTimestamp|selfLink):' \
  | oc apply -f -

echo "==> Creating PVC + RBAC in ${WORK_NS}"
oc apply -f "$EXP/pvc-models-cache.yaml"
oc apply -f "$EXP/rbac-vllm-eval.yaml"

echo "==> Creating eval ConfigMap in ${WORK_NS}"
oc apply -f "$EXP/configmap-vllm-eval.yaml"

echo "==> Creating inferno static + dynamic data ConfigMaps in ${SYS_NS}"
oc create configmap inferno-static-data -n "$SYS_NS" \
  --from-file=accelerator-data.json="$DATA_DIR/accelerator-data.json" \
  --from-file=model-data.json="$DATA_DIR/model-data.json" \
  --from-file=serviceclass-data.json="$DATA_DIR/serviceclass-data.json" \
  --from-file=optimizer-data.json="$DATA_DIR/optimizer-data.json" \
  --save-config --dry-run=client -o yaml | oc apply -f -

oc create configmap inferno-dynamic-data -n "$SYS_NS" \
  --from-file=capacity-data.json="$DATA_DIR/capacity-data.json" \
  --save-config --dry-run=client -o yaml | oc apply -f -

echo "==> Creating tuner ConfigMap in ${SYS_NS} (namespace rewritten)"
rewrite_ns < "$COMMON/configmap-tuner.yaml" | oc apply -f -

echo "==> Deploying inferno pod (controller, collector, optimizer, actuator, tuner) into ${SYS_NS}"
rewrite_ns < "$COMMON/deploy-loop.yaml" | oc apply -f -

# Override env to match the vllm-gpu scenario:
#   - 120s control period covers worst-case collect time (2 deployments x 30s window)
#   - INFERNO_WARM_UP_TIMEOUT=10 default (perfParms are seeded; warm-up is fast)
#   - DEFAULT_MAX_BATCH_SIZE=32 matches per-server label and per-model maxBatchSize
oc set env deployment/inferno -n "$SYS_NS" -c controller \
  INFERNO_CONTROL_PERIOD=120 \
  INFERNO_WARM_UP_TIMEOUT=10 \
  DEFAULT_MAX_BATCH_SIZE=32

# Collector simulate timeout > 2x maxWindowSec=30
oc set env deployment/inferno -n "$SYS_NS" -c collector \
  INFERNO_SIMULATE_TIMEOUT_SEC=60

oc rollout status deployment/inferno -n "$SYS_NS" --timeout=180s

echo "==> Deploying vLLM servers (Qwen2.5-14B + Llama-3.1-8B on H100)"
oc apply -f "$EXP/deployment-vllm-qwen.yaml"
oc apply -f "$EXP/deployment-vllm-llama.yaml"

echo "    First-run weight download to PVC may take ~15-30 min for both models."
echo "    Waiting for both vLLM Deployments to become Available..."
oc wait --for=condition=available deployment/vllm-qwen-14b-gpu -n "$WORK_NS" --timeout=1800s
oc wait --for=condition=available deployment/vllm-llama-gpu    -n "$WORK_NS" --timeout=1800s

echo "==> Deploying managed wrappers (server-sim + vllm-server evaluator)"
oc apply -f "$EXP/dep-vllm-qwen-server.yaml"
oc apply -f "$EXP/dep-vllm-llama-server.yaml"
oc rollout status deployment/vllm-qwen-14b-server -n "$WORK_NS" --timeout=300s
oc rollout status deployment/vllm-llama-server    -n "$WORK_NS" --timeout=300s

echo "==> Deploying load emulator (5-phase 1x->3x->1x ramp, 6 min per phase)"
oc apply -f "$EXP/configmap-load-phases.yaml"
oc delete pod load-emulator -n "$SYS_NS" --ignore-not-found
oc apply -f "$EXP/load-emulator.yaml"

echo ""
echo "==> Done."
echo ""
echo "    Watch controller logs:"
echo "      oc logs -f -n $SYS_NS deployment/inferno -c controller"
echo ""
echo "    Watch tuner EKF output:"
echo "      oc logs -f -n $SYS_NS deployment/inferno -c tuner"
echo ""
echo "    Watch the actuator pairing reconciler:"
echo "      oc logs -f -n $SYS_NS deployment/inferno -c actuator"
echo ""
echo "    Verify the evaluator resolved its paired vLLM pod:"
echo "      oc logs -n $WORK_NS deployment/vllm-qwen-14b-server -c evaluator | grep 'pairing resolved'"
echo "      oc logs -n $WORK_NS deployment/vllm-llama-server    -c evaluator | grep 'pairing resolved'"
echo ""
echo "    NOTE: control period = 120s (2 min); INFERNO_WARM_UP_TIMEOUT=10."
echo "    perfParms are seeded so the first useful cycle should appear quickly."
```

- [ ] **Step 3: Make the script executable**

Run:
```bash
chmod +x scripts/vllm-gpu/oc-deploy.sh
```

- [ ] **Step 4: Validate script with shellcheck (if available)**

Run:
```bash
if command -v shellcheck >/dev/null 2>&1; then
  shellcheck scripts/vllm-gpu/oc-deploy.sh && echo ok
else
  echo "shellcheck not installed; skipping"
fi
```
Expected: `ok` if shellcheck is installed, otherwise the skip message. No warnings if `ok` printed.

- [ ] **Step 5: Validate sed pattern + bash parse**

Run:
```bash
bash -n scripts/vllm-gpu/oc-deploy.sh && echo "syntax ok"
# Confirm rewrite_ns produces expected output on the actual common files
bash -c '
SYS_NS=inferno-system
sed "s/^\(  *\)namespace: inferno\$/\1namespace: ${SYS_NS}/g" \
  manifests/common/deploy-loop.yaml \
  | grep -E "namespace: inferno" | head -5
'
```
Expected: `syntax ok`, followed by zero or more `namespace: inferno-system` lines (no remaining bare `namespace: inferno` after the rewrite).

- [ ] **Step 6: Commit**

```bash
git add scripts/vllm-gpu/oc-deploy.sh
git commit -m "$(cat <<'EOF'
feat(vllm-gpu): add OpenShift deploy script

Apply order: namespaces -> HF token copy -> PVC/RBAC -> eval CM ->
inferno static/dynamic data CMs -> tuner CM (ns rewritten) -> inferno
pod (ns rewritten) -> env overrides -> vLLM servers (with 30-min wait
for first-run weight download) -> managed wrappers -> load emulator.

The HF token is copied from infer/hf-token-secret on the cluster so
no token leaks into git. The shared manifests/common YAMLs are
sed-rewritten on the fly to target inferno-system instead of the
hard-coded inferno.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md` (under the "Workloads" section, after the blis subsection)

- [ ] **Step 1: Read the current end of the Workloads section to find the insertion point**

Run:
```bash
grep -n -E '^### (Workloads|Useful)' CLAUDE.md
```
Expected output (line numbers will vary):
```
NNN:### Workloads
MMM:### Useful commands after deploy
```

The new subsection goes between the existing blis subsection inside `### Workloads` and the next `### Useful commands after deploy` heading. Read the lines from `### Workloads` to one line before `### Useful commands after deploy` to confirm exact existing layout.

Run:
```bash
awk '/^### Workloads/,/^### Useful commands after deploy/' CLAUDE.md
```

- [ ] **Step 2: Insert the new vllm-gpu subsection before the "### Useful commands after deploy" heading**

Use the Edit tool. The `old_string` is the line immediately preceding `### Useful commands after deploy` plus the heading itself; the `new_string` adds the new subsection in between.

Concretely, find the existing closing line of the blis section (the `INFERNO_WARM_UP_TIMEOUT=0` paragraph). The Edit replaces:

`old_string` (last paragraph of the blis subsection plus the next heading; verify the exact wording with `awk` above before editing):
```
Both use `configmap-blis-small.yaml` (betaCoeffs/alphaCoeffs for trained-physics) and `inferno-data/blis/` for optimizer/SLO config. `INFERNO_WARM_UP_TIMEOUT=0` is set so the optimizer waits for full EKF convergence before running.

### Useful commands after deploy
```

`new_string`:
```
Both use `configmap-blis-small.yaml` (betaCoeffs/alphaCoeffs for trained-physics) and `inferno-data/blis/` for optimizer/SLO config. `INFERNO_WARM_UP_TIMEOUT=0` is set so the optimizer waits for full EKF convergence before running.

**vllm-gpu workloads** (`scripts/vllm-gpu/oc-deploy.sh`):

| Deployment | Model | Accelerator | Evaluator | Class |
|---|---|---|---|---|
| `dep-vllm-qwen-server.yaml` | `qwen_2_5_14b` (Qwen2.5-14B-Instruct) | H100 | vllm-server | Bronze |
| `dep-vllm-llama-server.yaml` | `llama_3_1_8b` (unsloth/Meta-Llama-3.1-8B-Instruct, non-gated) | H100 | vllm-server | Premium |

Targets a shared OpenShift cluster (not kind). Uses two new namespaces — `inferno-system` (replaces `inferno`) and `inferno-workload` (replaces `infer`) — to avoid colliding with another team's existing setup. Both vLLM Deployments use `vllm/vllm-openai:v0.21.0` with `--max-num-seqs 32`, mount a shared `vllm-models-cache` PVC (RWX 100Gi on `ibm-spectrum-scale-fileset`), and read `HUGGING_FACE_HUB_TOKEN` from `hf-token-secret` (copied from the existing `infer/hf-token-secret` by the deploy script, not stored in git). perfParms in `inferno-data/vllm-gpu/model-data.json` are seeded with the converged values from the existing setup so cycle 1 produces a useful allocation. Control period is 120 s (worst-case `/collect` is ~60 s with 2 deployments × 30 s eval windows). The eval config uses `uniform-bounded` token sampling on both inputs and outputs to add per-request size variation without breaking `--max-model-len`. The cluster runs a `gpu-reaper.io` controller that scales down idle GPU pods after 30 min; the experiment's 24-min active phase sequence stays under the threshold, but vLLM Deployments left idle overnight will be reaped (cold start on next deploy, the PVC keeps weights).

### Useful commands after deploy
```

- [ ] **Step 3: Verify the new subsection renders correctly in the rendered Markdown table preview**

Run:
```bash
grep -A 6 'vllm-gpu workloads' CLAUDE.md
```
Expected: table header, separator, two table rows for the qwen and llama servers, then the prose paragraph beginning `Targets a shared OpenShift cluster`.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(vllm-gpu): document scenario in CLAUDE.md

Adds a vllm-gpu workloads subsection with the deployment table and
operational notes (OpenShift namespaces, HF token copy from infer/,
gpu-reaper idle threshold).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Cluster validation **[CLUSTER]**

This is the only task that touches the live cluster. Run from the working branch (`feat/vllm-gpu-experiment`) before opening the PR. It is iterative: if a check fails, fix on the branch with additional commits and re-run from the failing step.

Files: none modified directly. The deploy script does the apply.

- [ ] **Step 1: Verify cluster context**

Run:
```bash
oc whoami && oc whoami --show-server
```
Expected: matches `tantawi@us.ibm.com` and the OpenShift cluster URL the rest of the design assumes.

- [ ] **Step 2: Run the deploy script**

Run:
```bash
scripts/vllm-gpu/oc-deploy.sh
```
Expected exit: 0. The script can take up to 30 minutes during first deploy because of the model weight download; subsequent re-runs reuse the PVC cache.

- [ ] **Step 3: Verify the PVC bound**

Run:
```bash
oc get pvc -n inferno-workload vllm-models-cache
```
Expected: `STATUS = Bound`, `CAPACITY = 100Gi`, `STORAGECLASS = ibm-spectrum-scale-fileset`.

- [ ] **Step 4: Verify both vLLM Deployments Available**

Run:
```bash
oc get deploy -n inferno-workload -l 'inferno.vllm.accelerator=H100'
```
Expected: two rows (`vllm-qwen-14b-gpu`, `vllm-llama-gpu`), both with `READY` matching `UP-TO-DATE` matching `AVAILABLE` (e.g., `1/1`).

- [ ] **Step 5: Verify both managed Deployments Available**

Run:
```bash
oc get deploy -n inferno-workload -l 'inferno.server.managed=true'
```
Expected: two rows (`vllm-qwen-14b-server`, `vllm-llama-server`), both `READY 1/1`.

- [ ] **Step 6: Verify evaluator resolved pairing for both managed Deployments**

Run:
```bash
oc logs -n inferno-workload deployment/vllm-qwen-14b-server -c evaluator --tail=200 | grep -i 'pairing resolved'
oc logs -n inferno-workload deployment/vllm-llama-server    -c evaluator --tail=200 | grep -i 'pairing resolved'
```
Expected: at least one matching line per Deployment, including the resolved vLLM pod IP.

- [ ] **Step 7: Verify the controller has run at least one cycle**

Run:
```bash
oc logs -n inferno-system deployment/inferno -c controller --tail=200 | grep -E 'cycle|collect|optimize|actuate' | tail -30
```
Expected: lines showing a complete cycle (collect → optimize → actuate, or the equivalent log messages from this codebase). If the controller is still in EKF warm-up, log lines will say "warm-up in progress — skipping optimize+actuate"; that is acceptable for this validation, but rerun this check after another cycle period (~120 s) to confirm at least one non-warm-up cycle.

- [ ] **Step 8: Verify the cycle log JSONL is being written**

Run:
```bash
oc exec -n inferno-system deployment/inferno -c controller -- wc -l /inferno-cycles.jsonl 2>/dev/null \
  || oc exec -n inferno-system deployment/inferno -c controller -- sh -c 'find / -name inferno-cycles.jsonl 2>/dev/null | head -1 | xargs -r wc -l'
```
Expected: at least 1 line. The path may differ if the controller's working directory is set to a custom location; the second command discovers it.

- [ ] **Step 9: If any step failed**

Identify the cause from the relevant logs:
```bash
oc describe pod -n inferno-workload -l app=vllm-qwen-14b-gpu | grep -A 20 Events
oc describe pod -n inferno-workload -l app=vllm-llama-gpu | grep -A 20 Events
oc logs -n inferno-system deployment/inferno -c controller --tail=200
```
Fix on the branch with one or more new commits, then return to step 2 of this task. Do **not** silently work around failures or skip checks.

- [ ] **Step 10: Tear-down (optional)**

If you want a clean slate before the PR:
```bash
oc delete -f manifests/vllm-gpu/load-emulator.yaml --ignore-not-found
oc delete -f manifests/vllm-gpu/dep-vllm-qwen-server.yaml --ignore-not-found
oc delete -f manifests/vllm-gpu/dep-vllm-llama-server.yaml --ignore-not-found
oc delete -f manifests/vllm-gpu/deployment-vllm-qwen.yaml --ignore-not-found
oc delete -f manifests/vllm-gpu/deployment-vllm-llama.yaml --ignore-not-found
# Leave the PVC, namespaces, and inferno deployment in place so subsequent
# re-deploys are fast.
```
This step is optional — the script is idempotent and re-running it after a partial deploy is safe.

(No commit for this task — it is verification, not code change.)

---

## Task 10: Push branch and open PR

- [ ] **Step 1: Confirm branch is up to date and only contains intended commits**

Run:
```bash
git log --oneline main..HEAD
```
Expected: 6 commits (one for the spec already committed at the start, then five new ones: namespaces, inferno-data, manifests subgroups split into multiple commits per Tasks 3–6, deploy script, CLAUDE.md). If counts differ, review with `git log` to confirm each commit's content is intentional.

- [ ] **Step 2: Push the branch**

Run:
```bash
git push -u origin feat/vllm-gpu-experiment
```
Expected: branch pushed; remote tracking set.

- [ ] **Step 3: Open the PR**

Run:
```bash
gh pr create --base main --head feat/vllm-gpu-experiment \
  --title "feat(vllm-gpu): add experiment scenario for real H100 vLLM servers" \
  --body "$(cat <<'EOF'
## Summary

Adds the `vllm-gpu` experiment scenario — manifests, data files, deploy script, and CLAUDE.md docs — for running the inferno control loop against real H100 vLLM servers (Qwen2.5-14B + Llama-3.1-8B) on the shared OpenShift cluster.

Closes #32.

## Design

Spec: [`docs/superpowers/specs/2026-06-07-vllm-gpu-experiment-design.md`](https://github.com/llm-inferno/control-loop/blob/feat/vllm-gpu-experiment/docs/superpowers/specs/2026-06-07-vllm-gpu-experiment-design.md)

The PR is a configuration delta on top of `vllm-cpu`. Two new namespaces (`inferno-system`, `inferno-workload`) avoid colliding with another team's existing `inferno`/`infer` namespaces on the shared cluster. perfParms in `inferno-data/vllm-gpu/model-data.json` are seeded with the converged values from the existing setup so cycle 1 produces a useful allocation. The deploy script copies the HF token from `infer/hf-token-secret` rather than committing it to git.

## Test plan

- [ ] `scripts/vllm-gpu/oc-deploy.sh` completes without error on the OpenShift cluster
- [ ] `oc get pvc -n inferno-workload vllm-models-cache` shows Bound 100Gi RWX
- [ ] Both vLLM Deployments (`vllm-*-gpu`) and managed Deployments (`vllm-*-server`) reach READY
- [ ] `oc logs ... -c evaluator | grep "pairing resolved"` matches for both managed Deployments
- [ ] Controller log shows at least one complete cycle and `inferno-cycles.jsonl` has at least one record

## Out of scope (follow-ups)

- Experiment report (separate PR after first successful run + parameter tuning).
- Cold-start EKF validation on real GPUs.
- A way to override load-phase ratios from the CLI.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```
Expected: PR URL printed. Save it for the user.

(No commit for this task — `gh pr create` is the deliverable.)

---

## Self-review checklist

A pass over the plan against the spec:

**Spec coverage (each spec section → task):**
- "Namespace layout" → Task 1 ✓
- "vLLM Deployments" → Task 4 ✓
- "Managed Deployments" → Task 5 ✓
- "Eval ConfigMap" → Task 6 ✓
- "Load emulator" + "Load phases" → Task 6 ✓
- "Inferno data files" → Task 2 ✓
- "RBAC" → Task 3 ✓
- "PVC" → Task 3 ✓
- "HF token secret" → Task 3 (stub) + Task 7 (script copy) ✓
- "Deploy script" → Task 7 ✓
- "Control-loop tuning" → Task 7 (env overrides in oc-deploy.sh) ✓
- "Validation (pre-PR-merge)" → Task 9 ✓
- "Things deliberately not in this PR" → No code change needed; called out in PR body (Task 10) ✓
- CLAUDE.md update mentioned in spec scope → Task 8 ✓

**Placeholder scan:** No "TBD"/"TODO"/"add appropriate error handling"/"similar to Task N"/"write tests for the above" anywhere. Each step has actual content. The phrase "If any check fails" in Task 9 is not a placeholder — it's an explicit branch in the validation flow with concrete diagnostic commands.

**Type/name consistency check:**
- `inferno-system` and `inferno-workload` used consistently as the new namespaces. The other team's `inferno` and `infer` are only mentioned where deliberately differentiated (HF secret source, pre-existing collision context).
- Server names: `vllm-qwen-14b-server` and `vllm-llama-server` (managed) paired with `vllm-qwen-14b-gpu` and `vllm-llama-gpu` (vLLM) — consistent across Tasks 4, 5, 7, 9, 10.
- Model names in JSON: `qwen_2_5_14b` and `llama_3_1_8b` — consistent across `model-data.json` (Task 2), service classes (Task 2), eval ConfigMap (Task 6), and managed Deployment labels (Task 5).
- ConfigMap names: `vllm-server-eval-config` (Task 6 ConfigMap, Task 5 Deployment volume reference); `load-phases-config` (Task 6 ConfigMap, Task 6 load-emulator volume reference); `inferno-static-data` and `inferno-dynamic-data` (Task 7 deploy script).
- HF Secret: name `hf-token-secret`, key `token` — consistent across Task 3 stub, Task 4 envFrom, Task 7 copy command.
- `--max-num-seqs 32` in Task 4, `maxBatchSize: 32` in Task 2, `inferno.server.allocation.maxbatchsize: "32"` in Task 5, `DEFAULT_MAX_BATCH_SIZE=32` env in Task 7 — uniform.
- Storage class `ibm-spectrum-scale-fileset` matches the cluster default observed during cluster reconnaissance.

No issues found. Plan is ready to execute.
