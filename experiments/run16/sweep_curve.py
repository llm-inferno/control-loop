#!/usr/bin/env python3
"""
run16 delay-throughput curve sweep against ONE real vLLM pod.

GuideLLM (v0.2.0) is used only as the concurrency load driver (backgrounded and
killed after each level, since its post-run finalize hangs). The curve itself is
read from vLLM's server-side metric histograms, which is the authoritative signal:
  ITL  = d(vllm:time_per_output_token_seconds_sum) / d(_count) * 1000   [ms]
  TTFT = d(vllm:time_to_first_token_seconds_sum)    / d(_count) * 1000   [ms]
  RPM  = d(vllm:request_success_total) / window_s * 60
Also samples vllm:num_requests_running (achieved batch depth) and kv_cache_usage.

Usage:
  python3 sweep_curve.py <served_model> <hf_processor> <in_tok> <out_tok> <label>
Requires: `oc port-forward ... 8000:8000` already running to the vLLM pod.
"""
import json, subprocess, sys, time, urllib.request

PORT = 8000
GUIDELLM = "/Users/tantawi/Projects/llm/guidellm/.venv-preflight/bin/guidellm"
LEVELS = [4, 8, 16, 32, 48, 64, 96, 128]
RAMP_S = 25      # let the level reach N concurrent + warm
WINDOW_S = 35    # measurement window
DRAIN_S = 6

def metrics():
    raw = urllib.request.urlopen(f"http://localhost:{PORT}/metrics", timeout=10).read().decode()
    v = {}
    for line in raw.splitlines():
        if line.startswith("#") or not line.strip():
            continue
        for key in ("vllm:time_per_output_token_seconds_sum",
                    "vllm:time_per_output_token_seconds_count",
                    "vllm:time_to_first_token_seconds_sum",
                    "vllm:time_to_first_token_seconds_count",
                    "vllm:request_success_total",
                    "vllm:num_requests_running",
                    "vllm:kv_cache_usage_perc"):
            if line.startswith(key + "{") or line.startswith(key + " "):
                try:
                    v[key] = v.get(key, 0.0) + float(line.rsplit(None, 1)[1])
                except (ValueError, IndexError):
                    pass
    return v

def main():
    model, proc, intok, outtok, label = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
    print(f"# sweep model={model} shape={intok}/{outtok} label={label}", file=sys.stderr)
    print("concurrency,achieved_rpm,itl_ms,ttft_ms,running,kv_pct")
    for n in LEVELS:
        gl = subprocess.Popen(
            [GUIDELLM, "benchmark", "--target", f"http://localhost:{PORT}",
             "--model", model, "--processor", proc,
             "--rate-type", "concurrent", "--rate", str(n), "--max-seconds", str(RAMP_S + WINDOW_S + 15),
             "--data", f"prompt_tokens={intok},output_tokens={outtok},samples=2000",
             "--output-path", f"/tmp/gl_{label}_{n}.json"],
            stdout=open(f"/tmp/gl_{label}_{n}.log", "w"), stderr=subprocess.STDOUT)
        try:
            time.sleep(RAMP_S)
            m0 = metrics()
            running_samples = []
            t_end = time.time() + WINDOW_S
            while time.time() < t_end:
                running_samples.append(metrics().get("vllm:num_requests_running", 0.0))
                time.sleep(5)
            m1 = metrics()
        finally:
            gl.terminate()
            subprocess.run(["pkill", "-f", "guidellm benchmark"], capture_output=True)
        dcount = m1.get("vllm:time_per_output_token_seconds_count", 0) - m0.get("vllm:time_per_output_token_seconds_count", 0)
        dsum = m1.get("vllm:time_per_output_token_seconds_sum", 0) - m0.get("vllm:time_per_output_token_seconds_sum", 0)
        dtc = m1.get("vllm:time_to_first_token_seconds_count", 0) - m0.get("vllm:time_to_first_token_seconds_count", 0)
        dts = m1.get("vllm:time_to_first_token_seconds_sum", 0) - m0.get("vllm:time_to_first_token_seconds_sum", 0)
        dsucc = m1.get("vllm:request_success_total", 0) - m0.get("vllm:request_success_total", 0)
        itl = (dsum / dcount * 1000) if dcount > 0 else float("nan")
        ttft = (dts / dtc * 1000) if dtc > 0 else float("nan")
        rpm = dsucc / WINDOW_S * 60
        run = max(running_samples) if running_samples else 0
        kv = m1.get("vllm:kv_cache_usage_perc", 0) * 100
        print(f"{n},{rpm:.1f},{itl:.2f},{ttft:.1f},{run:.0f},{kv:.1f}", flush=True)
        time.sleep(DRAIN_S)

if __name__ == "__main__":
    main()
