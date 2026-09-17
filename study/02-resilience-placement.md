# 02 — Where circuit breakers, routing, fallbacks and retries live

> Study notes · 2026-09-17

## Question

*When many agents run across the organisation, where do circuit breakers, router fallbacks, retries and similar controls belong?*

**Answer:** put them in **one shared LLM gateway**. Each agent keeps only a **thin layer** for the things the gateway can't know.

## Why centralise

- **A circuit breaker only works if it sees all the traffic.**
  If 20 agents each keep their own breaker, each has to fail separately before it trips, so a broken upstream gets hit 20 times over.
  A breaker in the gateway sees the combined error rate and trips once for everyone.
- **Provider quotas apply to the whole account.**
  Bedrock and Anthropic count tokens per minute across the AWS or API account, not per agent.
  Only a central point can share that quota fairly and stop one noisy agent from getting everyone throttled.
- **Retries multiply across layers.**
  If the agent, the gateway and the SDK each retry 3 times, one failure becomes 9–27 upstream calls, which is enough to keep a struggling provider down.
  Retry in **one** layer.
- **Routing and fallback are policy.**
  "`chat-default` now falls back to Haiku" should be one config change, not a redeploy of every agent.

## What goes where

| Concern | Where | Why |
|---|---|---|
| Circuit breaker for each (provider, model, region) | **Gateway** | Needs everyone's traffic to judge health |
| Retries to providers (limited, backoff with jitter) | **Gateway** | The one retry layer |
| Model aliases, routing, fallback chains | **Gateway** | Org-wide policy |
| Token limits, budgets, quotas | **Gateway**, with counters shared in Redis | Provider quotas cover the whole account |
| Response cache, usage metering, audit | **Gateway** | One consistent view |
| Overall deadline for each call | **Agent** | Only the caller knows its deadline |
| One retry *to the gateway* (connection errors only, with an idempotency key) | **Agent** | Covers a gateway pod restart without repeating the gateway's retries |
| Fallback that changes behaviour (e.g. a non-LLM answer path) | **Agent** | Only the agent knows what a sensible degraded answer is |
| Rejecting a weaker model (`allow_fallback=false`, checking `x-served-model`) | **Agent** | Only the agent knows whether weaker output is acceptable |
| Retrying and checkpointing a step in a long-running agent | **Agent runtime / session service** | About the workflow, not the LLM call |
| Tool-call retries and breakers (SQL, kubectl, APIs) | **Tool gateway (MCP)** | Tool calls have different idempotency rules |
| Choosing a replica for a self-hosted model | **Inference scheduler (llm-d EPP)** | See [05](05-agentgateway-llm-d-production.md) |

## Ordering inside the gateway (innermost first)

1. **Timeouts.** Keep connection, time-to-first-token and total budgets **separate**. With a single total timeout, a healthy long generation looks the same as a hung upstream.
2. **Retries.** Retry only on 429, 5xx and connection or first-token timeouts. Never on 4xx. Keep them within a retry budget.
   **Retry only before the first token has been sent.** After that, the client already has partial output and a retry can't take it back.
3. **Circuit breaker.** One per deployment, tripping on the rolling error rate and p99 time-to-first-token, with a few test requests while half-open.
4. **Fallback chain.** Declared per model alias. **The response must say which model actually served it**, so a silent fall back to a weaker model can be detected.
5. **Hedging.** Off by default: sending a duplicate request cuts tail latency but doubles token spend.

## Consequences of centralising

- **The gateway becomes a critical dependency.** Keep it stateless, run 2+ replicas with autoscaling, and give it a tighter SLO than any single agent.
- **Decide where each piece of state lives:**
  - **Rate-limit and budget counters must be shared (Redis).** Otherwise N replicas let through N times the limit.
  - **Breaker state can stay in each replica's memory.** Replicas trip a few seconds apart, which is acceptable, and it avoids a Redis call on every request.
- **No bypass.** Use NetworkPolicy and IAM so that **only the gateway's identity** can reach the providers. Otherwise budgets and breakers can simply be ignored.
