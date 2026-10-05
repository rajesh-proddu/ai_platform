# Gateway plane (P0)

`config.yaml` is the [agentgateway](https://agentgateway.dev) **local ("standalone") configuration**
for the P0 row of [`AI-INFERENCE-PLATFORM-HLD.md`](../AI-INFERENCE-PLATFORM-HLD.md) §10.

It is the config the [`ai_security`](https://github.com/rajesh-proddu/ai_security) e2e suite consumes
(its `docs/DESIGN.md` §7 and `docs/ROADMAP.md` "Cross-repo dependencies"), so the names and ports
below are a **contract**, not an implementation detail.

## Version checked against

| | |
|---|---|
| agentgateway release | **v1.5.0**, published 2026-08-27 |
| Schema | `schema/config.json` at tag `v1.5.0` (`LocalConfig`) |
| Checked | 2026-09-18 |

`config.yaml` **validates cleanly** against that schema:

```bash
curl -sSLO https://raw.githubusercontent.com/agentgateway/agentgateway/v1.5.0/schema/config.json
python3 - <<'PY'
import json, yaml, jsonschema
jsonschema.Draft202012Validator(json.load(open('config.json'))).validate(
    yaml.safe_load(open('gateway/config.yaml')))
print('ok')
PY
```

Schema validation proves **shape**, not behaviour. The full stack (with vLLM) has **not** been run end to
end here — the dev box is CPU-only WSL2 with ~7.5 GiB and vLLM needs ~7.5 min to become healthy (see
[`study/01`](../study/01-vllm-on-cpu-local-lab.md)).

**Run live, 2026-09-29/30:** the `ai_security` Phase 1 spike
([`docs/spikes/agentgateway-extproc.md`](https://github.com/rajesh-proddu/ai_security/blob/phase-0-scaffold/docs/spikes/agentgateway-extproc.md))
ran the agentgateway v1.5.0 release binary on this config, with only hostnames changed, against a fake
OpenAI-compatible backend and `@modelcontextprotocol/server-everything`. The guardrail hooks,
`failureMode` and body modes below are therefore **observed**, not just schema-checked. The config as
committed also starts cleanly on that binary.

## Contract with `ai_security`

`ai_platform` defines these; `ai_security` must match them.

| Thing | Value | Notes |
|---|---|---|
| Gateway HTTP | `:3000` | LLM under `/v1/*`, MCP under `/mcp` and `/sse` |
| Gateway readiness | `:15021` | `config.readinessAddr` |
| Gateway metrics | `:15020` | `config.statsAddr` |
| Inspection service, gRPC | `ai-security-inspector:9000` | LLM route: Envoy `ext_proc`. MCP route: agentgateway `ExtMcp` (`mcpGuardrails`). One listener serves both. |
| Inspection service, webhook | `ai-security-inspector:8080` (HTTP) | only if the webhook guard is used instead |
| Caller-facing model alias | `chat-default` | virtual model; failover vLLM → Bedrock |
| Local vLLM | `vllm:8000`, model `Qwen/Qwen2.5-0.5B-Instruct` | |
| `failureMode` | `failClosed` on both hooks | must match ai_security's policy default |
| Session key | `x-session-id` request header | read by ext_proc from headers; passed to ExtMcp as `metadata.session` |

On the LLM route only **one** guardrail hook should be active at a time: `ext_proc` is the default, and the
webhook prompt-guard variant is present but commented out in `config.yaml`. The MCP route uses
`mcpGuardrails` rather than `ext_proc`, because raw `ext_proc` there sees SSE-framed JSON-RPC and replies
that don't name their method; ExtMcp passes the method, `params` and `result` as JSON.

`failOpen` is **not** a way to run without the inspector: with `fullDuplexStreamed`, the LLM hook fails every
request that has a body even when set to `failOpen` (observed). For gateway-only work, remove the two hook
blocks instead.

## What is verified, and how

### Tier 1 — confirmed by an official example at v1.5.0

Fields below appear verbatim in agentgateway's own `examples/` at tag `v1.5.0` or in its docs:

| Field | Source |
|---|---|
| `gateways.<name>.port` | [`examples/traffic-unified-gateway/config.yaml`](https://github.com/agentgateway/agentgateway/blob/v1.5.0/examples/traffic-unified-gateway/config.yaml) |
| `config.readinessAddr`, `config.statsAddr` | [`examples/llm-basic/config.yaml`](https://github.com/agentgateway/agentgateway/blob/v1.5.0/examples/llm-basic/config.yaml) |
| `llm.providers[].{name,provider,params}`, `llm.models[].provider.reference` | `examples/traffic-unified-gateway/config.yaml` |
| `llm.virtualModels[].routing` | same (with `weighted`; `failover` is tier 2, below) |
| `params.apiKey: $ENV_VAR` expansion | `examples/llm-basic/config.yaml` (`apiKey: $OPENAI_API_KEY`) |
| `provider: bedrock` + `params.awsRegion` | [Bedrock provider docs](https://agentgateway.dev/docs/standalone/latest/llm/providers/bedrock/) |
| `provider: openAI` + `params.baseUrl` for a self-hosted OpenAI-compatible server (vLLM) | [OpenAI-compatible providers](https://agentgateway.dev/docs/standalone/latest/llm/providers/), [vLLM provider](https://agentgateway.dev/docs/kubernetes/latest/llm/providers/vllm/) |
| `mcp.targets[].stdio.{cmd,args}` | [`examples/mcp-basic/config.yaml`](https://github.com/agentgateway/agentgateway/blob/v1.5.0/examples/mcp-basic/config.yaml) |
| LLM **and** MCP attached to one gateway | `examples/traffic-unified-gateway/config.yaml` |
| `extProc.{host,failureMode,processingOptions}` | [ExtProc docs](https://agentgateway.dev/docs/standalone/main/configuration/traffic-management/extproc/) |

### Tier 2 — schema-verified only (no official example found)

These are valid per the `v1.5.0` `LocalConfig` schema and the config validates, but agentgateway's
published examples do not demonstrate them. Treat as "should work", confirm in the Phase 1 spike:

- ~~**`llm.policies.extProc`** and **`mcp.policies.extProc`**~~ — now **observed** (see "Run live" above).
  `llm.policies.extProc` sees the caller-facing request (`model: chat-default`) before alias resolution.
  `mcp.policies.extProc` was replaced by **`mcp.policies.mcpGuardrails`** (`kind: remote`), also observed:
  pass, mutate and reject on `tools/list` and `tools/call`, and `metadata` CEL values delivered on both phases.
- **`llm.virtualModels[].routing.failover`** with `targets[].{model,priority}`
  (`LocalLLMFailoverRouting` / `LocalLLMFailoverTarget`). The only published virtual-model example uses
  `weighted`.
- ~~**`processingOptions.*BodyMode: fullDuplexStreamed`**~~ — now **observed**. The inspector must send
  every body chunk back as a streamed body mutation; a reply without one empties the body.
- The commented-out **webhook prompt guard** block (`llm.policies.guardrails.request[].webhook`,
  `PromptGuard` / `RequestGuard` / `Webhook` / `WebhookFailureMode`, `scope: ContentScope`).
  The published prompt-guard example uses the deprecated `binds:` layout and a `regex` guard.

### Tier 3 — TODO, deliberately not written

Guessing schema here would be worse than an honest gap, so these are `TODO:` comments in
`config.yaml` rather than plausible-looking YAML:

| Gap | HLD | Where to look |
|---|---|---|
| Exact-match response cache (Redis) | §8 | No cache policy exists in v1.5.0's policy set. <https://agentgateway.dev/docs/standalone/latest/reference/configuration/> |
| Token-aware admission: TPM + per-key budgets | §5.4 | `llm.policies.localRateLimit` / `remoteRateLimit` exist; token-aware shape unverified. <https://agentgateway.dev/docs/standalone/latest/configuration/resiliency/rate-limits/> |
| JWT auth against the platform's shared HS256 secret | §5.2 | `llm.policies.jwtAuth` (`LocalJwtConfig`) exists; HS256-secret wiring unverified. <https://agentgateway.dev/docs/standalone/latest/configuration/> |
| OTel `gen_ai.*` export to Jaeger/Prometheus | §11 | `config.tracing` / `config.metrics` exist; unverified. |
| Env-var expansion outside `apiKey` | — | Only `apiKey: $VAR` is demonstrated. `awsRegion` and `params.model` are therefore literals. **Observed:** expansion runs over the raw file, **comments included** — a dollar-prefixed name in a comment stops startup when that variable is unset. |

Also **not** verified, and a named selection criterion in HLD §5.3: whether agentgateway suppresses
retries once the first token has been flushed. `RetryPolicy` has a `precondition` CEL expression that
looks like the right lever, but this needs a behavioural test, not a schema read.

## Known contradiction with the HLD

HLD §5.6 and §10 still recommend **LiteLLM** and name **Ollama** as the dev backend.
This config uses **agentgateway** and **local vLLM**, per the studies
([`study/README.md`](../study/README.md) item 6, [`03`](../study/03-open-source-llm-gateways.md),
[`04`](../study/04-agent-router-vs-agentgateway.md)) and a later decision that supersedes the older text.
The HLD has intentionally **not** been edited here — resolving §5.6 is HLD open decision 2 and needs
an owner.

`ai_security` Phase 2 requires a LiteLLM config equivalent to this one. That is a separate file in
this directory when it lands, not a rewrite of this one.

## Running it

See [`deploy/local/`](../deploy/local/). The gateway alone:

```bash
docker run --rm -p 3000:3000 -p 15020:15020 -p 15021:15021 \
  -v "$PWD/gateway/config.yaml:/etc/agentgateway/config.yaml:ro" \
  -e VLLM_API_KEY=EMPTY \
  ghcr.io/agentgateway/agentgateway:v1.5.0 -f /etc/agentgateway/config.yaml
```
