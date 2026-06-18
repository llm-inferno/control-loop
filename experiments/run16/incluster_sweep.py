#!/usr/bin/env python3
"""
In-cluster delay-throughput curve sweep against a real vLLM pod (run16).

Runs as a Job on the cached vllm/vllm-openai image (python3 + stdlib only -- no
GuideLLM, no extra deps). Drives N concurrent full-length completions pod-to-pod
(no port-forward tunnel) and reads ITL/TTFT/throughput from vLLM's own server-side
metric histograms -- the same network path the real evaluator uses, so the curve
is faithful.

Env:
  TARGET   base url, e.g. http://vllm-qwen-14b-gpu.inferno-workload.svc:8000
  MODEL    served model name (e.g. qwen)
  SHAPES   comma list of in x out, e.g. "1024x512,1024x2048"
  LEVELS   comma list of concurrency levels (default 4,8,16,32,48,64,96,128)
  RAMP/WINDOW seconds (default 15/30)
Emits CSV to stdout: shape,concurrency,achieved_rpm,itl_ms,ttft_ms,running,kv_pct
"""
import json, os, sys, threading, time, urllib.request

TARGET = os.environ["TARGET"].rstrip("/")
MODEL = os.environ.get("MODEL", "qwen")
SHAPES = os.environ.get("SHAPES", "1024x512,1024x2048").split(",")
LEVELS = [int(x) for x in os.environ.get("LEVELS", "4,8,16,32,48,64,96,128").split(",")]
RAMP = int(os.environ.get("RAMP", "15"))
WINDOW = int(os.environ.get("WINDOW", "30"))

MET_KEYS = ("vllm:inter_token_latency_seconds_sum", "vllm:inter_token_latency_seconds_count",
            "vllm:time_to_first_token_seconds_sum", "vllm:time_to_first_token_seconds_count",
            "vllm:request_success_total", "vllm:num_requests_running", "vllm:kv_cache_usage_perc")

def metrics():
    raw = urllib.request.urlopen(TARGET + "/metrics", timeout=15).read().decode()
    v = {}
    for line in raw.splitlines():
        if line.startswith("#") or not line.strip():
            continue
        for k in MET_KEYS:
            if line.startswith(k + "{") or line.startswith(k + " "):
                try:
                    v[k] = v.get(k, 0.0) + float(line.rsplit(None, 1)[1])
                except (ValueError, IndexError):
                    pass
    return v

def worker(prompt, outtok, stop, ctr):
    body = json.dumps({"model": MODEL, "prompt": prompt, "max_tokens": outtok,
                       "min_tokens": outtok, "ignore_eos": True, "temperature": 0.0,
                       "stream": False}).encode()
    while not stop.is_set():
        try:
            req = urllib.request.Request(TARGET + "/v1/completions", data=body,
                                         headers={"Content-Type": "application/json"})
            urllib.request.urlopen(req, timeout=600).read()
        except Exception:
            time.sleep(0.5)
        ctr[0] += 1

def sweep_shape(intok, outtok):
    prompt = ("The quick brown fox jumps over the lazy dog. " * (intok * 4 // 44 + 1))[:intok * 4]
    for n in LEVELS:
        stop = threading.Event()
        ctr = [0]
        threads = [threading.Thread(target=worker, args=(prompt, outtok, stop, ctr), daemon=True) for _ in range(n)]
        for t in threads:
            t.start()
        time.sleep(RAMP)
        m0 = metrics()
        runs = []
        t_end = time.time() + WINDOW
        while time.time() < t_end:
            runs.append(metrics().get("vllm:num_requests_running", 0.0))
            time.sleep(3)
        m1 = metrics()
        stop.set()
        dcount = m1.get(MET_KEYS[1], 0) - m0.get(MET_KEYS[1], 0)
        dsum = m1.get(MET_KEYS[0], 0) - m0.get(MET_KEYS[0], 0)
        dtc = m1.get(MET_KEYS[3], 0) - m0.get(MET_KEYS[3], 0)
        dts = m1.get(MET_KEYS[2], 0) - m0.get(MET_KEYS[2], 0)
        dsucc = m1.get(MET_KEYS[4], 0) - m0.get(MET_KEYS[4], 0)
        itl = (dsum / dcount * 1000) if dcount > 0 else float("nan")
        ttft = (dts / dtc * 1000) if dtc > 0 else float("nan")
        rpm = dsucc / WINDOW * 60
        run = max(runs) if runs else 0
        kv = m1.get(MET_KEYS[6], 0) * 100
        print(f"{intok}x{outtok},{n},{rpm:.1f},{itl:.2f},{ttft:.1f},{run:.0f},{kv:.1f}", flush=True)
        time.sleep(6)  # drain in-flight before next level

print("shape,concurrency,achieved_rpm,itl_ms,ttft_ms,running,kv_pct", flush=True)
for s in SHAPES:
    intok, outtok = (int(x) for x in s.lower().split("x"))
    print(f"# --- shape {intok}x{outtok} ---", file=sys.stderr, flush=True)
    sweep_shape(intok, outtok)
print("# SWEEP DONE", flush=True)
