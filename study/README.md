# Study notes — AI inference platform

Research notes gathered while designing [`AI-INFERENCE-PLATFORM-HLD.md`](../AI-INFERENCE-PLATFORM-HLD.md), used for design work and interview preparation.
Researched 2026-09-17. These projects move fast: re-check versions and which features are paid before acting on them.

| # | Topic | Main takeaway |
|---|---|---|
| [01](01-vllm-on-cpu-local-lab.md) | vLLM on a CPU-only laptop | Works with AVX2 ("limited features"); ≤1.5B models in 7.5 GiB; use it to test the serving setup, and Ollama for everyday local use |
| [02](02-resilience-placement.md) | Where breakers, retries and fallbacks live | Centralise them in one gateway; agents keep only a deadline, a single retry to the gateway, and their own fallback behaviour; retry in one layer only |
| [03](03-open-source-llm-gateways.md) | Open-source LLM gateways | LiteLLM (most complete budgets; Prometheus is paid), Bifrost (Go), Agent Router (Envoy), agentgateway (Rust, MCP/A2A) |
| [04](04-agent-router-vs-agentgateway.md) | Agent Router vs agentgateway | Envoy AI Gateway was renamed Agent Router; agentgateway is lighter and also covers MCP tool traffic |
| [05](05-agentgateway-llm-d-production.md) | agentgateway + llm-d in production | The gateway decides *what* serves a request, llm-d's EPP decides *where*; only needed once a model has 2+ replicas; includes interview questions |

## Takeaways

1. **There's no "KV cache service" to build.** The KV cache lives inside vLLM; prefix routing and offload tiers are built around it.
2. **Two routing decisions:** model/provider (gateway) and replica (inference scheduler).
3. **Retry in one layer only, and never after the first token has been sent.**
4. **Autoscale GPU inference on queue length and KV-cache usage**, not CPU.
5. **Prefill/decode splitting needs RDMA**; without it, skip it.
6. **The HLD hasn't been updated yet:** §5.6 still recommends LiteLLM, while notes 03–05 point to agentgateway for agent workloads.
