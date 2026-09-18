# ai_platform

The **model-serving plane**: one authenticated, observable front door for LLM inference, plus the
state and registry planes behind it.

- [`AI-INFERENCE-PLATFORM-HLD.md`](AI-INFERENCE-PLATFORM-HLD.md) — high-level design
- [`study/`](study/README.md) — research notes: local vLLM lab, where resilience controls belong,
  gateway comparisons, agentgateway + llm-d in production, interview questions

## Status

**P0 scaffold** (HLD §10, P0 row): gateway configuration plus interfaces and runnable skeletons for
the state and registry planes. There is **no business logic yet** — no routing code, no context
assembly, no eviction. HTTP handlers reply `501` where the work is P1; the types, interfaces and
in-memory reference implementations behind them are real and tested.

Two decisions supersede older text in the HLD:

| | HLD says | Actual |
|---|---|---|
| Gateway | LiteLLM (§5.6) | **agentgateway** — see [`study/03`](study/03-open-source-llm-gateways.md), [`04`](study/04-agent-router-vs-agentgateway.md), and `study/README.md` item 6 |
| Dev / e2e model backend | Ollama (§10) | **local vLLM**, CPU-only — see [`study/01`](study/01-vllm-on-cpu-local-lab.md) |

Bedrock remains the prod backend. The HLD has not been edited; resolving §5.6 is its open decision 2.

This scaffold also settles HLD open decision 6 in one direction: **`ai_platform` is self-contained**.
Gateway config and the local stack live here, not in a separate `-infra` repo.

## Layout

```
gateway/           agentgateway configuration — LLM route (vLLM dev / Bedrock prod), MCP route,
                   ai_security guardrail hook. See gateway/README.md for what is verified.
deploy/local/      docker compose stack: agentgateway + vLLM (CPU) + Redis + sample MCP server
internal/session/  state plane (HLD §7) — ONE service, TWO scopes: session + memory
internal/registry/ model registry plane (HLD §9) — cards, versions, bindings, rollout
internal/httpx/    shared HTTP boilerplate: env config, /healthz, JSON, graceful shutdown
cmd/stateservice/  state-plane service, :8090
cmd/registry/      model-registry service, :8091
build/Dockerfile   one image build for both services (--build-arg SERVICE=…)
```

### What each plane owns

**`internal/session` — state plane (HLD §7).** One `Store` interface spanning both scopes, because
they share an identity model, a lifecycle and a consumer. The session scope holds turns, run state
and TTL; the memory scope holds durable facts with **mandatory provenance** (source session,
observed-at, confidence, TTL) — a memory write without it is rejected, not defaulted. P0 ships
`InMemoryStore`; Redis and pgvector are P1 and add no dependency today.

**`internal/registry` — model registry (HLD §9).** Model cards carrying the §9 field list, deployment
bindings (`model_version → runtime + hardware + replicas`), and the lifecycle
`registered → shadow → canary → production → deprecated → retired` with its legal transitions
enforced. Retire is a state, never a delete: retired versions stay resolvable for audit.

## Ports

| Component | Port | |
|---|---|---|
| agentgateway | 3000 | OpenAI-compatible LLM API (`/v1/*`) and MCP (`/mcp`) |
| agentgateway metrics / readiness | 15020 / 15021 | |
| vLLM | 8000 | dev + e2e model backend |
| Redis | 6379 | P1 session scope and response cache |
| sample MCP server | 3001 | behind the gateway's MCP route |
| ai_security inspection service | 9000 (ext_proc gRPC), 8080 (webhook) | separate repo; optional in the compose file |
| stateservice | 8090 | `STATE_HTTP_ADDR` |
| registry | 8091 | `REGISTRY_HTTP_ADDR` |

These are a cross-repo contract: [`ai_security`](https://github.com/rajesh-proddu/ai_security)'s e2e
suite runs the gateway from this repo's configuration (its `docs/DESIGN.md` §7).

## Getting started

```bash
# Go services
make check                 # gofmt + vet + build + test
make run-stateservice      # :8090/healthz
make run-registry          # :8091/healthz

# Local stack (vLLM needs ~5-8 min to become healthy on CPU — see study/01)
cp deploy/local/.env.example deploy/local/.env
make up
curl localhost:15021       # gateway readiness
curl localhost:3000/v1/models

# Gateway config, validated against the pinned agentgateway schema
make gateway-validate      # needs python3 with jsonschema + pyyaml
```

`make help` lists the rest.

## Not in P0

Recorded so the gaps are explicit: token-aware admission (TPM, budgets) and JWT auth at the gateway,
the exact-match response cache, the context assembler, long-running sessions with checkpointing,
token metering, and durable stores for both planes. Each is a `TODO` at the place it will land —
`gateway/config.yaml`, `gateway/README.md`, or the relevant Go package.
