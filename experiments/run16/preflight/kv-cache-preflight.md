# Run16 KV-cache pre-flight (concurrency ceiling = 128)

**Date:** 2026-06-17
**Cluster:** OpenShift `api.pokprod001.ete14.res.ibm.com`, NVIDIA H100-80GB-HBM3
**vLLM:** `vllm/vllm-openai:v0.21.0`, `--gpu-memory-utilization 0.90`, `--max-num-seqs 128`
**Raw provenance:** `vllm-kv-startup.txt` (verbatim vLLM startup log lines)

## Question

Run16 raises `--max-num-seqs`, the optimizer search ceiling, and the Arm-B pin all to **128**
so that M* (not the server limit) is the binding constraint and the evaluator's per-replica
concurrency cap is honored by the real server. This only holds if a single H100 can actually hold
128 concurrent sequences at our operating token shapes without KV-cache preemption. If not, all
three values drop to 64.

## Method

vLLM computes and logs its KV-cache pool size at startup. Concurrent-sequence capacity at a given
token shape is:

```
max concurrent seqs ≈ GPU_KV_cache_tokens / (avg_in + avg_out)
```

Using the **full** nominal operating shape (every sequence at full length simultaneously — the
worst case, which already accounts for KV growth over a 2048-token decode):

| Model | GPU KV pool | Operating shape (in+out) | Max concurrent | Need @128 | Utilization @128 | Verdict |
|---|---|---|---|---|---|---|
| Llama-3.1-8B | 447,936 tok | 768 + 2048 = 2,816 | **159** | 360,448 tok | 80% | ✅ ~24% headroom |
| Qwen2.5-14B  | 226,992 tok | 1024 + 512 = 1,536 | **147** | 196,608 tok | 87% | ✅ ~15% headroom |

(The startup "Maximum concurrency … per request" line is reported at `max_model_len`
— 8192 Llama / 4096 Qwen — not at our shorter operating shape, so the effective capacity above is
higher than that 54–55× figure.)

## Verdict

**128 is safe for both models.** Both hold 128 concurrent at full operating-shape length with
headroom. No fallback to 64. Notably **Qwen (14B) is the tighter constraint** (147 vs Llama's 159):
its smaller weights-leftover KV pool outweighs its shorter sequences — confirming the a-priori
expectation that the binding model was not obvious from sequence length alone.

## Empirical confirmation (GuideLLM concurrent=128, 2026-06-17)

Drove each pod with GuideLLM `--rate-type concurrent --rate 128` against the real vLLM OpenAI
endpoint (via `oc port-forward`), forcing full-length output (`ignore_eos`, matching the
experiment evaluator's `ignoreEOS=true`), while sampling vLLM's own metrics every 15s. Verdict
comes from server-side `vllm:num_preemptions_total` / `vllm:num_requests_running` /
`vllm:kv_cache_usage_perc` — the authoritative signal. Raw traces: `llama-metrics.txt`,
`qwen-metrics.txt`.

| Model | Peak concurrent observed | KV-cache usage at peak | Preemptions | Requests waiting |
|---|---|---|---|---|
| Llama-8B (768/2048) | **128** (held steady) | — (metric name miss first run) | **0** | **0** |
| Qwen-14B (1024/512) | **127** | **~0.81** | **0** | 0 |

Both sustained ~128 concurrent at full forced output length with **zero preemptions** and **zero
queueing** — confirming the static verdict, including the dynamic decode-growth case. Qwen's
observed ~81% KV usage at 127 concurrent matches the predicted ~87% at 128 (the binding model, as
expected).

**Tooling caveat:** GuideLLM **v0.2.0**'s load generation worked correctly, but its post-run
aggregation/finalize **hung** (>20 min at constant CPU, never wrote `--output-path`), so no native
`guidellm-*.json` was produced. This did not affect the result — the verdict is read from vLLM's
metrics, not GuideLLM's report. For future runs, either patch/upgrade GuideLLM or keep relying on
the server-side metric scrape (which is what matters here).

## Related

- Future work: derive this ceiling automatically instead of hand-setting it — see the
  KV-derived-concurrency-ceiling note.
