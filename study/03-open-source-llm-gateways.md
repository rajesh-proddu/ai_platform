# 03 — Open-source LLM gateways

> Study notes · researched 2026-09-17. **Which features are free and which are paid changes often: check current docs before choosing.**

## Question

*Is there an open-source gateway that covers authentication, routing, circuit breaking, retries, fallbacks, budgets and so on?*

**Answer:** yes, several. They differ mainly in **which features are paid** and **how many pods they need to run**.
Pod count matters on a small cluster (for example `max-pods=4` on t3.micro nodes).

## Main candidates

| | **LiteLLM Proxy** | **Bifrost** | **Agent Router** (formerly Envoy AI Gateway) | **agentgateway** |
|---|---|---|---|---|
| Language / license | Python · open source with a paid enterprise tier | **Go** · Apache 2.0 | Go on Envoy · Apache 2.0 | **Rust** · Apache 2.0 |
| Governance | One company (BerriAI) | One company (Maxim) | AAIF (Bloomberg, Tetrate, Google, …) | Linux Foundation / AAIF (started at Solo.io) |
| OpenAI-compatible API | ✅ 100+ providers | ✅ 25+ providers | ✅ 16+ providers | ✅ OpenAI, Anthropic, Gemini, Bedrock, … |
| Retries / fallback / load balancing | ✅ | ✅ | ✅ | ✅ (priority groups + load balancing) |
| Circuit breaking | ✅ "cooldowns" | ✅ | ✅ Envoy's circuit breaking | ✅ health policy removes bad backends |
| Token-based rate limits | ✅ | ✅ | ✅ | ✅ |
| Virtual keys / budgets | ✅ free (key, user, team) | ✅ nested key → team → customer | token quotas | ✅ spend controls |
| JWT / OIDC | ✅ free per LiteLLM's feature page (a third-party guide says paid) | check | ✅ Envoy's policies | ✅ JWT, API keys, OAuth |
| Prometheus metrics | ❌ **enterprise only** | ✅ | ✅ | OpenTelemetry metrics |
| MCP / A2A | — | MCP | MCP gateway | **MCP + A2A are core** |
| Routing across self-hosted replicas (GAIE) | — | — | ✅ InferencePool | ✅ InferencePool |
| Deployment footprint | 1 container + Redis + Postgres | 1 container | Controller + Envoy + rate-limit service + Redis | 1 binary/container (standalone) or its own control plane (Kubernetes) |

Also worth knowing:
- **Portkey Gateway** (TypeScript)
- **Kong** and **Apache APISIX** AI plugins. Kong and Portkey keep many governance features in their paid or hosted tiers.
- **Higress** (Envoy-based)

## A second layer: routing across your own vLLM replicas

The gateways above choose a **model or provider**. Once one model runs on several vLLM replicas, you also need a router that chooses the **replica**, based on which one holds the prompt's prefix in its KV cache and how long each queue is.
- **Kubernetes Gateway API Inference Extension (GAIE):** the standard; use it.
- **llm-d:** builds on GAIE's Endpoint Picker; adds prefill/decode splitting, precise prefix routing, and a fleet-wide autoscaler (WVA).
- **vLLM production-stack router:** a separate router that duplicates what GAIE + agentgateway already give you.

See [05](05-agentgateway-llm-d-production.md).

## Recommendation (as of this study)

| Situation | Choice |
|---|---|
| Small cluster, need Python-level flexibility, the most complete key/team/budget model | **LiteLLM**. Its Prometheus metrics are paid, so export OpenTelemetry instead; vLLM's own `/metrics` stays free. |
| Go shop, single container, Prometheus included | **Bifrost**. Check which features are paid and how actively it's maintained. |
| Agents that use **MCP tools** as well as LLMs; small footprint; later GAIE / llm-d | **agentgateway**. One component can be both the LLM gateway and the MCP tool gateway. |
| Already on Envoy Gateway, or a larger Kubernetes/GitOps setup | **Agent Router** |

Whichever you choose, test two behaviours first:
1. **No retry once the first token has been sent** to the client.
2. **Token limits that count real usage**, not just requests.

> Note: `AI-INFERENCE-PLATFORM-HLD.md` §5.6 still recommends LiteLLM. This study points to agentgateway for the agent use case; the HLD hasn't been updated.

## Sources
- [LiteLLM — Free vs Enterprise](https://www.litellm.ai/features)
- [LiteLLM pricing](https://www.litellm.ai/pricing)
- [TrueFoundry — LiteLLM Enterprise](https://www.truefoundry.com/blog/litellm-enterprise)
- [LiteLLM budget routing](https://docs.litellm.ai/docs/proxy/provider_budget_routing)
- [Bifrost docs](https://docs.getbifrost.ai/overview)
- [Bifrost routing, fallback & governance](https://www.getmaxim.ai/articles/llm-gateway-routing-fallback-and-governance-in-bifrost/)
- [Agent Router capabilities](https://theagentrouter.ai/docs/capabilities/)
- [agentgateway](https://github.com/agentgateway/agentgateway)
- [Vercel — open-source AI gateways compared](https://vercel.com/i/open-source-ai-gateways)
