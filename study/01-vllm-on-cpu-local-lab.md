# 01 — Running vLLM on a CPU-only laptop

> Study notes · verified 2026-09-17 · vLLM 0.29.0

## Question

*The laptop has no GPU. Can it run vLLM with small open-weight models, and which models fit?*

**Answer:** yes. vLLM has a CPU backend, but only small models fit. Use it to **exercise the serving setup**
(the OpenAI-compatible API, vLLM's flags and its `/metrics` endpoint), not for speed.

## The machine

| | Value | Consequence |
|---|---|---|
| CPU | Intel Core Ultra 5 225H, 14 cores | — |
| Instruction sets | **AVX2 yes, AVX-512 no** | vLLM supports x86 with AVX-512 (recommended) or AVX2 (**"limited features"**) |
| RAM | 15.4 GiB on the host; **WSL2 and Docker Desktop get 7.48 GiB** | This limit decides which models fit |
| GPU | None usable (no NVIDIA; the Intel iGPU isn't a realistic vLLM target) | CPU backend only |
| OS | Ubuntu 24.04 (glibc 2.39), Python 3.12, WSL2 | The prebuilt `+cpu` wheel (needs glibc ≥ 2.34) works |
| Docker | Docker Desktop 29.2.0 with WSL integration | "docker could not be found in this WSL 2 distro" only means Docker Desktop isn't running |

Useful checks:
```bash
for f in avx2 avx512f avx512_vnni avx512_bf16 amx_tile; do grep -qw $f /proc/cpuinfo && echo "$f yes" || echo "$f no"; done
free -h
docker info --format 'CPUs={{.NCPU}} Mem={{.MemTotal}}'
```

## Ways to install vLLM on CPU

- **Docker (easiest):** `vllm/vllm-openai-cpu:latest-x86_64`. About 1.84 GB compressed. Its entrypoint is `vllm serve`, so the model name is the first argument.
- **Prebuilt wheel** (available since v0.17): `uv pip install` the `vllm-<ver>+cpu-...whl` from the GitHub releases. The docs say this also needs Intel OpenMP in `LD_PRELOAD`; Docker handles that for you.
- **Build from source:** needs gcc/g++ ≥ 12.3.

Key settings:
- `VLLM_CPU_KVCACHE_SPACE`: how many GiB to reserve for the KV cache. Keep it at 1–2 on this machine.
- `VLLM_CPU_OMP_THREADS_BIND`: which cores the threads are pinned to (`auto` by default).
- `--dtype bfloat16`: the dtype vLLM recommends on CPU. If it fails on AVX2, use `float32`, which takes twice the memory.
- `--max-model-len`: the maximum context length. Keeping it small limits how much KV cache each sequence can take.

## Which models fit in 7.5 GiB

Rule of thumb: in bf16, weights take about **2 GB per billion parameters**. Add roughly 1–2 GB for vLLM itself plus whatever you reserve for the KV cache.

| Model | Weights | Fits? |
|---|---|---|
| `HuggingFaceTB/SmolLM2-360M-Instruct` | ~0.7 GB | ✅ smallest, quickest smoke test |
| `Qwen/Qwen2.5-0.5B-Instruct`, `Qwen/Qwen3-0.6B` | ~1 GB | ✅ **used for this lab** |
| `meta-llama/Llama-3.2-1B-Instruct`, `google/gemma-3-1b-it` | ~2–2.5 GB | ✅ gated on Hugging Face: accept the license, needs an HF token |
| `Qwen2.5-1.5B-Instruct`, `Qwen3-1.7B`, `SmolLM2-1.7B-Instruct` | ~3–3.5 GB | ⚠️ tight; set the KV cache to 1 GiB |
| 3B models | ~6 GB | ❌ unless the WSL memory limit is raised |
| 7B+ | 14 GB+ | ❌ not in bf16 |
| `BAAI/bge-small-en-v1.5` (embeddings) | ~130 MB | ✅ embedding models suit CPU well |

To raise the WSL limit, create `C:\Users\<you>\.wslconfig` with the following, then run `wsl --shutdown`:
```
[wsl2]
memory=12GB
```

## What was run

```bash
docker pull vllm/vllm-openai-cpu:latest-x86_64

docker run -d --name vllm-cpu -p 8000:8000 --shm-size=4g \
  -e VLLM_CPU_KVCACHE_SPACE=1 \
  -v ~/.cache/huggingface:/root/.cache/huggingface \
  vllm/vllm-openai-cpu:latest-x86_64 \
  Qwen/Qwen2.5-0.5B-Instruct --dtype bfloat16 --max-model-len 4096
```

Observed:
- The model download (0.92 GiB) took about 214 s without an HF token.
- `/health` returned OK after about **7.5 minutes** (image start, download, then loading the weights).
- The container's memory use was **5.39 GiB of 7.48 GiB** right after startup and about 4.5 GiB later. The model itself is under 1 GiB; the rest is vLLM runtime overhead plus the reserved KV cache.
- Chat completions and `/metrics` were tested afterwards; see [Benchmark](#benchmark-bf16-vs-float32) below.

Try it:
```bash
curl localhost:8000/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"Qwen/Qwen2.5-0.5B-Instruct","messages":[{"role":"user","content":"hi"}]}'
curl -s localhost:8000/metrics | grep -E 'vllm:(num_requests|kv_cache|time_to_first_token)' | head
docker stop vllm-cpu      # restarts are faster now: the weights are cached in ~/.cache/huggingface
```

### Benchmark: bf16 vs float32

Tested 2026-09-17: Qwen2.5-0.5B-Instruct, `temperature=0`, all numbers read from `/metrics` before and after each request.

The float32 container was started with the privileges vLLM's CPU docs recommend.
Those privileges removed the `numa_migrate_pages` / `numa_set_membind` "permission denied" warnings (errno 1) that appeared in the bf16 run.
```bash
docker run -d --name vllm-cpu -p 8000:8000 --shm-size=4g \
  --cap-add SYS_NICE --security-opt seccomp=unconfined \
  -e VLLM_CPU_KVCACHE_SPACE=1 \
  -v ~/.cache/huggingface:/root/.cache/huggingface \
  vllm/vllm-openai-cpu:latest-x86_64 \
  Qwen/Qwen2.5-0.5B-Instruct --dtype float32 --max-model-len 4096
```
`--dtype float64` isn't accepted. The options are `auto`, `bfloat16`, `float`/`float32`, `float16` and `half`.

| Measurement | bf16 (no extra privileges) | float32 (+ `SYS_NICE`, `seccomp=unconfined`) |
|---|---|---|
| Time until `/health` passes | ~442 s (includes the model download) | ~268 s (model already cached) |
| First request after start (warm-up / compile) | TTFT ~110 s | TTFT ~107 s |
| Short request, 35 tokens in: TTFT | ~6.0 s | **1.1 s** |
| Time per generated token (ITL) | ~1.3–1.5 s (~0.7 tok/s) | ~1.05–1.12 s (~0.9–1.0 tok/s) |
| 64-token answer, end to end | — | 71.7 s |
| 460-token prompt, nothing cached: TTFT | 3.0 s | 2.4 s |
| Same system prompt, second request: TTFT | 1.8 s (384/459 tokens cached) | 1.35 s (384/459 tokens cached) |
| Container memory after start | 5.39 GiB | 5.27 GiB |

What this shows:
- **float32 plus the privileges made prompt processing much faster** (short-prompt TTFT dropped from 6 s to 1.1 s).
  **Token generation barely improved** (about 25%), and it is still about 1 s per token.
- **Generation isn't slowed by the model's actual maths.** The model reads 460 prompt tokens in about 2.4 s, so producing one token should take a small fraction of a second.
  The time is probably going to overhead on each generation step. Neither bf16 nor the missing privileges was the main cause.
  Still to try:
  - `--enforce-eager` (rules out the compiled path);
  - `VLLM_CPU_OMP_THREADS_BIND` set to fewer cores, or `nobind` (the Core Ultra 5 225H mixes core types, and threads that run in lockstep wait for the slowest one);
  - an explicit `OMP_NUM_THREADS`.

  vLLM logs "Reducing Torch threads from 14 to 1 for serving", which is worth investigating.
- **The prefix cache works in whole 128-token blocks** (`block_size=128`): 384 = 3 × 128.
  The final partial block is never reused, so short prompts get no cache hits. Reusing the cache cut TTFT by about 40–45%.
- **The first request after every container start costs about 110 s** for compilation.
  The compile cache lives at `/root/.cache/vllm` inside the container. Mounting it (`-v ~/.cache/vllm:/root/.cache/vllm`) should avoid repeating that work on restarts (not yet tested).
- **Sampling defaults come from the model**, not vLLM.
  Qwen's `generation_config.json` sets `temperature 0.7, top_k 20, top_p 0.8, repetition_penalty 1.1` for requests that don't specify them. Use `--generation-config vllm` to get vLLM's own defaults.
- `vllm:cache_config_info` shows how the cache is set up: `enable_prefix_caching=True`, `block_size=128`, `kv_cache_memory_bytes=1073741824` (1 GiB), 682 blocks.

Metrics used above, all present in v0.29.0:
`time_to_first_token_seconds`, `inter_token_latency_seconds`, `request_prefill_time_seconds`, `request_decode_time_seconds`, `request_queue_time_seconds`,
`prefix_cache_hits_total` / `prefix_cache_queries_total`, `prompt_tokens_cached_total`, `num_requests_running` / `num_requests_waiting`, `kv_cache_usage_perc`, `num_preemptions_total`.

### Hugging Face token

- It's stored at `~/.cache/huggingface/token` with mode `600`. That's the default location for the HF libraries and CLI, so no `HF_TOKEN` environment variable is needed.
- The container reads it through the mounted cache folder.
- It deliberately isn't in `/etc/environment`, because every user on the machine can read that file.
- Never commit the token. Rotate it if it has been pasted anywhere, such as a chat or a log.

## Can the recommendations service use this directly?

Not yet, and adding a gateway alone wouldn't change that.

- `videostreamingplatform-recommendations` has three providers: `ollama`, `bedrock` and `anthropic`.
  The Ollama provider calls Ollama's own endpoints (`/api/chat`, `/api/embeddings`), not the OpenAI API that vLLM serves (`/v1/chat/completions`).
- Gateways such as LiteLLM and agentgateway **also expose the OpenAI API**, so the service needs an **OpenAI-compatible provider** either way (about 40 lines).
  Once it has one, it can call vLLM directly, and the same client also works against Ollama's `/v1` endpoint.
- **Embeddings are the harder part:**
  - One provider uses the same model for chat and `embed()`.
  - A chat model served by vLLM isn't configured to return embeddings.
  - pgvector columns are fixed at `vector(EMBEDDING_DIMENSION)` (default 1536), so switching embedding models means recreating the table and re-embedding.
- **With one caller and one backend, a gateway only adds a network hop and a container**, and memory is already tight here.
  It starts paying off once there are several backends (for example chat on vLLM, embeddings elsewhere, and Bedrock as fallback) or several callers.

## vLLM or Ollama on a CPU laptop?

- **vLLM on CPU:** use it to test the P2 serving setup: the same API, flags and metrics.
- **Ollama / llama.cpp (4-bit GGUF models):** use them to actually *work with* a model on a laptop. They're better tuned for CPUs, fit 3B–7B models in 7.5 GiB, and are usually faster.
- Both expose OpenAI-compatible APIs, so a gateway treats them the same way.

## Sources
- [vLLM CPU installation](https://docs.vllm.ai/en/latest/getting_started/installation/cpu.html)
