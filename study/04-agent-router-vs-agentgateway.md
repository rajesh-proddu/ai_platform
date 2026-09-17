# 04 — Agent Router (formerly Envoy AI Gateway) vs agentgateway

> Study notes · researched 2026-09-17

**Name change:** Envoy AI Gateway is now **Agent Router**, an Agentic AI Foundation (AAIF) project with "same code, same maintainers".
Its docs redirect from `aigateway.envoyproxy.io` to `theagentrouter.ai`.

## Side by side

| | **Agent Router** | **agentgateway** |
|---|---|---|
| Origin / governance | Envoy community (Bloomberg, Tetrate, Google, Tencent, Nutanix) → AAIF · Apache 2.0 | Solo.io → Linux Foundation (2025) → AAIF (2026) · Apache 2.0 |
| How it's built | **An extension of Envoy Gateway**: the Envoy proxy plus AI-specific components | **Its own Rust proxy**, not based on Envoy (built with lessons from Istio's ztunnel) |
| Running it | Kubernetes with Envoy Gateway. `aigw run` works standalone on Linux/macOS (serves on `:1975`, admin on `:1064`), **but quotas and rate limits need Envoy's rate-limit service + Redis; the full feature set is Kubernetes-only** | **Standalone:** binary, container or one Deployment, configured by a file plus a UI. **Kubernetes:** its own control plane (Gateway API + custom resources, two Helm charts) |
| Main focus | Traffic from apps to LLM providers | Agents → LLMs, **agents → tools (MCP)**, **agents → agents (A2A)** |
| Fallback | Automatic failover between providers | Priority groups, with load balancing inside each group |
| Circuit breaking | Envoy's, the most battle-tested option | Health policy removes backends on 5xx/429 |
| Retries / timeouts | Envoy's | Configurable attempts, backoff, which status codes trigger a retry |
| Token limits | Token-aware limits and quotas | Checked when the request arrives **and** after the response |
| Budgets | Token quotas | Spend controls |
| Auth | Envoy's security policies; credentials for upstream providers | JWT (issuer/audience/JWKS), API keys, OAuth; access rules in CEL |
| MCP | MCP gateway (combines servers, routes tools, OAuth) | **Core focus:** MCP OAuth 2.1, access control per tool, audit |
| A2A | Not in the docs | ✅ |
| Routing across self-hosted replicas | InferencePool | InferencePool; the first gateway to pass GAIE v1.4.0 conformance (per its own blog) |
| Observability | Prometheus `/metrics`, OpenTelemetry | OpenTelemetry metrics, logs and traces; per-token, time-to-first-token (TTFT) and per-tool metrics |

## How they differ

1. **Proxy underneath.** Agent Router inherits about a decade of hardened Envoy behaviour and a large ecosystem.
   agentgateway is newer Rust code built for AI protocols.
   Both projects publish performance numbers that favour themselves; **benchmark before believing them**.
2. **Scope.** Agent Router is mainly an LLM gateway that has added MCP.
   agentgateway treats LLM calls, MCP tools and agent-to-agent traffic as **one data plane**.
3. **Footprint.** agentgateway standalone is one container.
   Agent Router needs the Envoy Gateway controller, AI gateway components, Envoy pods, the rate-limit service and Redis.
4. **When token limits apply.** agentgateway lets the request that crosses the budget finish and rejects the *next* one with 429, so a single large request can overshoot.
   The stricter version is to estimate tokens before admitting a request and reconcile afterwards.

## Choosing

- **agentgateway** fits a small or starting platform:
  - It runs as a single container.
  - It can be **both** the LLM gateway and the MCP tool gateway that the agent-platform design needs (subsystem #6: typed tools, per-call audit, access control).
- **Agent Router** fits a team already on Envoy Gateway, or a platform with a real node group that wants Envoy's maturity under a Kubernetes/GitOps setup.
- **Before choosing agentgateway, check** how complete its virtual keys and team budgets are. LiteLLM's key/team/budget model is still the most complete, which matters if per-team chargeback is needed on day one.

## Sources
- [Agent Router capabilities](https://theagentrouter.ai/docs/capabilities/)
- [AAIF proposal #18 — Agent Router (previously Envoy AI Gateway)](https://github.com/aaif/project-proposals/issues/18)
- [aigw run](https://aigateway.envoyproxy.io/docs/cli/aigwrun/)
- [Envoy AI Gateway hands-on before Kubernetes (DEV)](https://dev.to/kanywst/envoy-ai-gateway-a-hands-on-tour-you-can-run-before-touching-kubernetes-136p)
- [agentgateway GitHub](https://github.com/agentgateway/agentgateway)
- [agentgateway introduction](https://agentgateway.dev/docs/standalone/latest/documentation/about/introduction/)
- [Designing agentgateway](https://agentgateway.dev/blog/2026-06-04-designing-agentgateway-unified-gateway/)
- [agentgateway model failover](https://agentgateway.dev/docs/kubernetes/main/llm/failover/)
- [agentgateway LLM rate limiting](https://agentgateway.dev/docs/kubernetes/2.2.x/llm/rate-limit/)
- [agentgateway spend control](https://agentgateway.dev/docs/llm/spending/)
- [agentgateway v1.3.0](https://agentgateway.dev/blog/2026-06-17-agentgateway-v1.3.0/)
