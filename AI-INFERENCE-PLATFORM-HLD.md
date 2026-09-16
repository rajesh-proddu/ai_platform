# AI Inference Platform — High-Level Design (HLD)

> **Status:** Design proposal · **Owner:** Rajesh · **Date:** 2026-09-16
> **Scope:** The platform for *serving* LLM inference — gateway, model routing & resilience, self-hosted model serving (vLLM), caching tiers, session & memory state, model registry, and observability.

**Relationship to existing docs.** This is the **model-serving plane**. Its sibling, [`AI-AGENT-PLATFORM-HLD.md`](https://github.com/rajesh-proddu/videostreamingplatform-docs/blob/main/AI-AGENT-PLATFORM-HLD.md), owns the **agent-execution plane** (agent loops, sandboxing, tool gateway, credential brokering). That doc's agents are *callers* of this platform. Governance primitives defined there — JWT/IRSA identity, OTLP→Jaeger+Prometheus, audit, human-confirm — are **reused by reference here, not restated**. Eval gates remain owned by [`AI-AGENTS-ADLC.md`](https://github.com/rajesh-proddu/videostreamingplatform-docs/blob/main/AI-AGENTS-ADLC.md).

---

## 0. TL;DR

- **One service is being asked for that should not be built.** There is no such thing as a "KV cache service." The request conflates four distinct caching/state layers that live in four different places (§3). Separating them removes a component from the build list and re-targets the real work onto the two layers that *are* services: the **response cache** (gateway) and the **memory store** (pgvector).
- **The dominant constraint is GPU cost, and it is this platform's `max-pods=4`.** Self-hosted vLLM cannot run on the current free-tier `t3.micro` footprint under any configuration — not a tuning problem, a hardware-class problem. A GPU node is a different hardware class, is **always-on**, and bills whether or not anyone calls it (§2). Price it before committing (§6) — the break-even against per-token Bedrock spend is a number to compute, not assume.
- **Therefore the platform phases along the same build-vs-buy discipline as the sibling HLD:** everything that is *not* GPU-bound — gateway, model registry, sessions, memory, caching, metering — ships first on the existing cluster, routing to **Bedrock (prod)** and **Ollama (dev)**. **vLLM self-hosting is P2, gated on a funded GPU node group** (§10).
- **Buy the gateway, don't build it** (§5.6). LiteLLM already provides routing, fallbacks, retries, budgets and multi-provider adapters. A bespoke Go gateway is justified only by a named driver, and the design keeps that migration a deployment change rather than a rewrite.
- **Sessions and memory are one service with two scopes** (§7), not two services. Splitting them before a driver exists would be speculative.

---

## 1. Purpose & scope

**Purpose.** Provide a single, authenticated, observable entry point for all LLM inference across the org — whether the model runs on our GPUs or a third-party provider — so that callers write to one API and the platform owns routing, resilience, cost and state.

**In scope:** LLM gateway (auth, routing, resilience, budgets), self-hosted serving via vLLM, model registration & lifecycle, caching tiers, session/conversation state, agent memory & context assembly, inference-specific observability and cost metering.

**Out of scope (owned elsewhere):**
- Agent loops, sandboxing, tool execution, credential brokering → sibling HLD.
- Whether a use case *should* use an LLM, eval gates, guardrail policy → ADLC.
- Fine-tuning / training pipelines. **Explicitly deferred** — this platform serves and registers model artifacts; it does not produce them. Adding training now would double scope for no near-term driver.
- Embedding *generation* for the recommendations path (already exists in `videostreamingplatform-recommendations`); this platform absorbs it only when that service migrates to the gateway.

---

## 2. Drivers & constraints

| Driver | Consequence for this platform |
|---|---|
| **GPUs are the cost floor.** The smallest practical single-GPU node for vLLM is a different hardware class from the current free-tier `t3.micro` fleet (1 GiB RAM, no accelerator) — it needs dedicated VRAM the existing fleet does not have at any replica count, and it bills continuously. | **vLLM self-hosting is gated on a funded GPU node group.** Every pre-GPU capability must be designed to work with hosted providers first. |
| **Inference cost is per-token and unbounded by default.** Unlike the video stack, a single caller can spend arbitrarily. | **Budgets, quotas and token-aware rate limiting are P0 correctness features, not P2 nice-to-haves** (§5.4). |
| **Streaming is the default UX.** Responses are SSE token streams, not atomic JSON. | Breaks transparent retry (§5.3), complicates caching, and changes the SLIs (TTFT/ITL, not just p99 latency). Designed for explicitly. |
| **Bedrock is the established prod LLM path**; Ollama is the established dev path (both already wired in `videostreamingplatform-recommendations`). | The gateway's first job is to **unify what already exists**, not to introduce a new inference substrate. |
| **Cold start on GPU is measured in minutes**, not seconds (weights S3 → disk → HBM). | Scale-to-zero is not free. Warm pools or a pinned minimum replica are required, which reinforces the always-on cost above. |
| **Reuse existing platform stacks** — JWT (`utils/auth`), rate limiting (`utils/middleware`), OTLP→Jaeger+Prometheus, pgvector, Redis, Kafka. | No new observability, auth or datastore platform. This doc adds *one* new datastore concern at most. |

> **Central tension, stated plainly:** the value of self-hosting is control, unit-cost at scale, and data residency. The cost is a standing GPU bill that dwarfs the current platform spend and is incurred **whether or not anyone is calling it**. This design resolves it by making self-hosting a *routing target the platform can adopt later* rather than a foundation it is built on — so the gateway, registry and state planes deliver value at near-zero marginal cost today.

---

## 3. "KV cache" — what was actually asked for

The request "K,V cache for LLMs to store memory" fuses four layers that live in four places and have nothing in common but the word *cache*. Getting this wrong produces a service that should not exist while leaving the real caching wins unimplemented.

| # | Layer | What it actually holds | Where it lives | Keyed by | Do we build it? |
|---|---|---|---|---|---|
| 1 | **KV cache** (PagedAttention) | Attention key/value tensors for tokens generated so far | **GPU HBM, inside the vLLM process** | Per-request, lives and dies with the request | **No.** It is vLLM internals. We *tune* it (`--gpu-memory-utilization`, `--max-model-len`, `--max-num-seqs`). |
| 2 | **Prefix cache** | Shared KV blocks for identical prompt prefixes across requests | vLLM, HBM → optionally spilled to CPU RAM / NVMe (LMCache) | Prefix hash | **No — it is a flag** (`--enable-prefix-caching`) plus an offload tier decision. |
| 3 | **Provider prompt cache** | Same idea, provider-side | Bedrock / Anthropic | Explicit cache breakpoints in the request | **No.** We *use* it — mark stable system prompts + tool definitions as cacheable. |
| 4 | **Response cache** | Whole completions | **Redis, at the gateway** | Exact prompt hash, or embedding (semantic) | **Yes.** This is a real component (§8). |

**And the thing the phrase "store memory" actually refers to** — an agent remembering facts across sessions — is **none of the above**. That is the memory store (§7), backed by pgvector, and it is unrelated to attention KV.

**Conclusion:** there is **no KV-cache service to build**. Layers 1–3 are configuration of components we either run (vLLM) or call (Bedrock). The build list keeps layer 4 and the memory store.

> **Why this matters beyond terminology:** layers 1–3 are where the large latency and cost wins live (prefix reuse on a long stable system prompt can dominate everything else), and they are unlocked by flags and prompt structure, not by new services. Budgeting engineering time to build a "KV cache service" would spend it in the one place that returns nothing.

---

## 4. Component view — the planes

```
 ┌───────────────────────────────────────────────────────────────────────────┐
 │  CALLERS                                                                   │
 │  agent runtimes (sibling HLD) · recommendations · web BFF · batch jobs     │
 └───────────────────────────────┬───────────────────────────────────────────┘
                                 │  OpenAI-compatible API (+ SSE streaming)
                                 ▼
 ┌───────────────────────────────────────────────────────────────────────────┐
 │  GATEWAY PLANE                                        §5                   │
 │  (1) AuthN/Z — JWT (utils/auth) · virtual keys per team/project            │
 │  (2) Admission — token-aware rate limits · budgets · load shedding         │
 │  (3) Router — model alias → deployment; weighted / latency / cost          │
 │  (4) Resilience — timeouts · retries · circuit breaker · fallback chain    │
 │  (5) Response cache (Redis)                           §8                   │
 └──────┬──────────────────────────────────────────────┬─────────────────────┘
        │                                              │
        ▼ self-hosted                                  ▼ third-party
 ┌──────────────────────────────┐            ┌──────────────────────────────┐
 │  SERVING PLANE     §6        │            │  PROVIDER ADAPTERS           │
 │  vLLM (OpenAI-compatible)    │            │  Bedrock (prod) · Ollama(dev)│
 │  PagedAttention · continuous │            │  Anthropic API               │
 │  batching · prefix caching   │            │  (provider prompt caching)   │
 │  ⚠ requires GPU node group   │            └──────────────────────────────┘
 └──────────────┬───────────────┘
                │ weights + config pulled at pod start
                ▼
 ┌───────────────────────────────────────────────────────────────────────────┐
 │  MODEL REGISTRY PLANE                                 §9                   │
 │  model cards · versions · deployment bindings · rollout state · eval refs  │
 │  artifacts in S3 · metadata in MySQL/Postgres                              │
 └───────────────────────────────────────────────────────────────────────────┘
 ┌───────────────────────────────────────────────────────────────────────────┐
 │  STATE PLANE                                          §7                   │
 │  Session/Context service — ONE service, TWO scopes:                        │
 │    • session scope  → turns, run state, TTL        (Redis)                 │
 │    • memory scope   → durable facts + embeddings   (pgvector)              │
 │  Context assembler — budget, retrieve, compact, order                      │
 └───────────────────────────────────────────────────────────────────────────┘
 ┌───────────────────────────────────────────────────────────────────────────┐
 │  OBSERVABILITY & COST PLANE                           §11                  │
 │  OTel GenAI spans → Jaeger · TTFT/ITL/queue/KV-util → Prometheus           │
 │  token metering → per-key cost attribution · audit log                     │
 └───────────────────────────────────────────────────────────────────────────┘
```

Reading the diagram: **the gateway is the only front door.** No caller talks to Bedrock or a vLLM pod directly. That single chokepoint is what makes auth, budgets, fallback, metering and audit enforceable rather than advisory — and it is what lets vLLM be added in P2 as a routing target with **zero caller changes**.

---

## 5. Gateway plane

### 5.1 API surface
**OpenAI-compatible** (`/v1/chat/completions`, `/v1/embeddings`, `/v1/models`). Not for fashion — it is the de-facto interop standard, so every SDK, LangGraph/LangChain integration and vLLM's own server already speak it. Callers migrate by changing a base URL.

### 5.2 Auth & identity
- **User/agent calls:** existing HS256 JWT via `utils/auth`, verified with `middleware.JWTAuth` — identical to the `dataservice` download paywall. No second auth system.
- **Service-to-service & batch:** **virtual keys** — a gateway-issued credential bound to a team/project/environment, carrying its own model allow-list, rate limit and budget. This is the unit of cost attribution (§11) and the unit of revocation.
- **Authorization:** model access is an allow-list per key. A key scoped to `haiku-class` cannot spend on `opus-class` by changing one string — the most common way LLM spend escapes.

### 5.3 Resilience: timeouts, retries, circuit breaking, fallback
Ordered innermost → outermost, because the order is what makes them compose rather than multiply:

1. **Timeouts** — separate **connect**, **TTFT** and **total** budgets. A single total timeout is wrong for streaming: a healthy long generation and a hung upstream look identical until you separate time-to-*first*-token from total duration.
2. **Retries** — bounded (2–3), exponential backoff **with jitter**, and only on **retryable** classes: 429, 5xx, connect/TTFT timeout. **Never** on 400-class (a malformed prompt retried three times is three times the latency and the same error).
   - **The streaming caveat, stated explicitly:** once the first token has been flushed to the client, the request is **no longer transparently retryable** — you cannot un-send tokens. Retry applies **only pre-first-token**. After that the only honest options are fail-forward with an error event in the stream, or a client-visible restart. Gateways differ in whether they get this right; it is a selection criterion (§5.6).
   - Retries must be **budgeted** (retry-budget / token-bucket), never unconditional, or a partial upstream brownout becomes a self-inflicted DDoS.
3. **Circuit breaker** — **per (provider, model, region) deployment**, not global. Trip on rolling error-rate + TTFT p99; half-open with a small probe quota. Tripping sheds a bad deployment in seconds instead of burning every caller's retry budget on it.
4. **Fallback chain** — declared per model alias, e.g. `chat-default → [self-hosted-llama, bedrock-sonnet, bedrock-haiku]`. Fallback is a **routing** decision made after the breaker opens.
   - **Fallback must be explicit about degradation.** Falling back to a materially weaker model silently changes output quality. The response carries the model actually served (`x-served-model`), and quality-sensitive callers may pin `allow_fallback=false`.
5. **Hedging — default off.** Racing a second request at p95 cuts tail latency but **doubles token spend** on every hedged call. Enable per-route only where latency is worth more than tokens.

### 5.4 Admission control: the LLM-specific part
Standard RPM limiting is **insufficient** here, and this is the most common gateway design error:

> One request with a 200k-token prompt and one with 50 tokens are identical to an RPM limiter and differ by ~4000× in cost and GPU occupancy.

Therefore admission is **token-aware**:
- **TPM (tokens/min) alongside RPM**, per key and globally. Enforced on *estimated* input tokens at admit time, **reconciled against actual usage** post-response.
- **Budgets** — hard spend caps per key/day and per key/month, with a soft-warn threshold. Exhaustion → `429` with a typed reason, plus a Kafka `budget-exhausted` event so notifications can alert the owner.
- **Concurrency caps** per key — protects the GPU queue, which degrades non-linearly once saturated.
- **Queue + load shed** — bounded queue with age-based rejection. Better to fail fast at admit than to accept work the GPU cannot drain before the client times out.

### 5.5 Routing
Model **alias** (`chat-default`, `summarize-cheap`) → one of N **deployments**. Aliases are the contract; deployments are swappable — this is what makes "shift 10% to the self-hosted model" a config change (§10).

Strategies: **weighted** (canary/shadow), **least-busy** (best for GPU — queue depth, not latency, is the true saturation signal), **latency-based** p95 EWMA, **cost-priority** (cheapest capable model first, escalate on failure).

### 5.6 Build vs buy — **recommendation: buy first**

| | Adopt LiteLLM (recommended, P0) | Build bespoke Go gateway |
|---|---|---|
| Routing, fallback, retries, budgets, virtual keys | Built-in | Build all of it |
| Provider adapters | 100+ maintained | We maintain each |
| Native `utils/auth` JWT + entitlement integration | Adapter/proxy layer | Native |
| OTel wiring matching the Go services | Configured | Native |
| Time to first value | Days | Quarters |

**Decision: adopt an off-the-shelf gateway for P0.** The resilience semantics above are table-stakes features it already implements; rebuilding them in Go buys integration polish at the price of the entire roadmap. **Build a bespoke gateway only on a named driver** — a latency floor the proxy hop cannot meet, a JWT/entitlement rule that cannot be expressed as a plugin, or an ops requirement to run Go-only services.

The design stays portable either way: callers see an OpenAI-compatible API, so replacing the gateway implementation is a deployment change, not a caller rewrite. **Verify before committing** that the chosen gateway handles the §5.3 streaming-retry boundary and §5.4 token-aware limiting correctly — those are where implementations diverge.

---

## 6. Serving plane — vLLM (P2, GPU-gated)

**Why vLLM:** PagedAttention (KV memory paging, eliminates fragmentation), **continuous batching** (new requests join an in-flight batch — the single largest throughput win over naive per-request serving), prefix caching, tensor parallelism, and an OpenAI-compatible server that slots in behind the gateway as just another deployment.

**Sizing knobs that matter:**

| Knob | Effect |
|---|---|
| `--gpu-memory-utilization` | Fraction of VRAM for weights + KV. Too high → OOM under concurrency; too low → shallow batching. |
| `--max-model-len` | Hard cap on context. Directly sets worst-case KV per sequence. |
| `--max-num-seqs` | Concurrency ceiling. The throughput/latency dial. |
| `--enable-prefix-caching` | Cross-request prefix reuse (§3 layer 2). Large win with a stable system prompt. |
| tensor-parallel size | Shards a model too large for one GPU; adds interconnect sensitivity. |
| quantization (AWQ/GPTQ/FP8) | Cuts VRAM and cost per token, at some quality cost — **must be eval-gated**, not assumed free. |

**Operational realities to design for, not discover:**
- **Cold start is minutes.** Weights pull (S3 → node) + load to HBM. Mitigate: bake weights into the image or pre-stage on node NVMe, keep a warm minimum replica, prefer smaller models. **Scale-to-zero trades a standing bill for a multi-minute first-request latency** — an explicit product decision, not a default.
- **Autoscale on queue depth / KV-cache utilization, not CPU.** CPU is meaningless on a GPU server; HPA on CPU will never fire correctly.
- **One model per pod.** Multiplexing models on a GPU is a later optimization.
- **Node group:** dedicated, tainted (`dedicated=gpu:NoSchedule`) so nothing else squats on it — the same pattern already used for `ebs-csi` nodes in the video platform.

> **Cost gate.** Before any vLLM commitment, price the target instance for the chosen model class and compare against projected Bedrock spend at real QPS:
> ```
> aws ec2 describe-spot-price-history --instance-types g5.xlarge \
>   --product-descriptions "Linux/UNIX" --region us-east-1 --max-items 5
> ```
> Self-hosting wins on **sustained high utilization**, data residency, or a model no provider offers. At low/bursty QPS, per-token Bedrock is almost always cheaper than an idle GPU — and the break-even point is a number this platform should compute, not assume.

---

## 7. State plane — sessions & memory (**one service, two scopes**)

The request named a memory service and a session service separately. They share the same identity model, the same lifecycle hooks and the same consumer (context assembly), and the overlap is large enough that two services would mean two APIs, two datastores and a distributed join on every turn. **Design them as one service exposing two scopes.** Split later only if their scaling profiles genuinely diverge — a service split is cheap to do later and expensive to undo.

### 7.1 Session scope — short-term (Redis)
Conversation turns, tool-call results, run status, token accounting. Keyed `session_id`, TTL-bounded, cheap to read every turn.

**Short-running vs long-running** — the distinction the request called out:

| | Short-running | Long-running |
|---|---|---|
| Shape | Synchronous request→response | Async job; may span hours/days, survive restarts |
| State | Redis, TTL minutes–hours | **Checkpointed durably** (Postgres) + Redis working set |
| Resumption | Not needed | **Required** — resume from last checkpoint after crash/deploy |
| API | `POST /sessions/{id}/messages` | `POST /runs` → `run_id`; poll/stream/webhook |
| Failure | Client retries | Platform resumes; idempotency key prevents double execution |

Long-running is where correctness is actually hard: **every step must be idempotent and checkpointed**, or a pod restart silently re-executes side effects. That is a P1 capability, deliberately after the synchronous path.

### 7.2 Memory scope — long-term (pgvector)
Durable, cross-session knowledge for an agent/user:
- **Semantic** — extracted facts/preferences, embedded, retrieved by similarity.
- **Episodic** — summaries of past sessions.
- **Provenance is mandatory** — every memory row carries source session, timestamp and a confidence/TTL. Without it, memory accumulates stale and contradictory facts and quality degrades in a way that is very hard to debug.

Reuses the **pgvector** StatefulSet already running (`pgvector.infra.svc.cluster.local:5432`) — no new datastore.

**Write path is asynchronous.** Fact extraction is itself an LLM call; doing it inline would add a full round-trip to every turn. Emit a Kafka event post-turn and extract in a consumer — the same producer/consumer pattern the video platform already uses.

### 7.3 Context assembler — the real work
Neither store is useful without deciding **what actually goes in the prompt** under a fixed token budget:

```
budget = context_window − max_output_tokens − safety_margin

  [ system prompt + tools ]   ← stable, marked cacheable (§3 layer 3) — put FIRST
  [ retrieved memory (top-k) ] ← from pgvector, relevance-filtered
  [ rolling summary ]          ← compacted older turns
  [ recent turns verbatim ]    ← most recent N, always included
```
Rules: **stable content first** (a cacheable prefix only works if the prefix is byte-identical across calls — any variable content placed early destroys the cache hit for everything after it); compact oldest-first when over budget; **never silently truncate mid-tool-call**, which corrupts the tool protocol; emit `context_tokens_used` per turn so budget pressure is observable before it becomes a failure.

---

## 8. Caching tiers (the ones we own)

Per §3, layers 1–3 are configuration. Layer 4 is a component:

**Response cache (Redis, at the gateway).**
- **Exact-match** — SHA-256 of (model, normalized messages, temperature, tools, seed). Safe, and the default. Only cache `temperature=0` / deterministic calls; caching sampled output makes responses inconsistent for no benefit.
- **Semantic (embedding-similarity) — default OFF.** A near-miss above threshold returns an answer to a *different question*. The failure is silent, plausible-looking and extremely hard to detect in production. Enable per-route, only for tolerant workloads (FAQ-style), with a conservative threshold and a `x-cache: semantic` header so it is attributable.
- Never cache across **tenant/auth scope** — cache key must include the entitlement scope, or one caller's answer leaks to another.
- TTL per route; explicit invalidation on model-version change (a cached answer from a retired model version is a correctness bug).

**Expected win ordering (highest first):** provider/vLLM prefix caching on a long stable system prompt → exact response cache → semantic cache. The first is a flag and prompt-ordering discipline; it is where to start.

---

## 9. Model registry plane

The registry answers: *what models exist, which version is live where, and is it allowed to be.*

**Model card** (one per model+version): `model_id`, `version`, `provider` (self-hosted | bedrock | ollama | anthropic), `task` (chat | embedding | rerank), `context_window`, `quantization`, `weights_uri` (S3), `tokenizer`, `license`, `eval_scorecard_ref`, `cost_per_1k_tokens`, `owner`, `rollout_state`.

**Deployment binding:** `model_version → serving runtime + hardware profile + replica policy`. This is what the gateway router resolves an alias to, and what makes model rollout a registry write rather than a redeploy.

**Lifecycle:** `registered → shadow → canary → production → deprecated → retired`.
- **Shadow** — mirror live traffic, compare outputs, serve none of it. The only safe way to validate a self-hosted model against the Bedrock baseline before it takes real traffic.
- **Canary** — weighted routing (§5.5), promote on eval + operational metrics.
- **Promotion is gated on the ADLC eval suite** — the registry stores the scorecard reference; it does not redefine eval policy.
- **Retire ≠ delete** — retired versions stay resolvable for audit reproducibility (which model produced this output?).

**Artifacts:** weights in S3, versioned and checksummed; metadata in a relational store. **Checksum verification at pod start is a supply-chain control** — a model artifact is executable content, and silently loading a mutated one is the model-serving equivalent of pulling an unpinned image.

---

## 10. Deployment paths & phasing

| Phase | What ships | Where it runs | Prereqs |
|---|---|---|---|
| **P0 — Gateway & state** | Gateway (auth, aliases, routing, timeouts/retries/CB/fallback, TPM+budgets), model registry, session scope, exact response cache. Routes to **Bedrock (prod) + Ollama (dev)**. | Existing cluster. **No GPU.** | Gateway selection (§5.6); Redis; JWT secret sharing |
| **P1 — Memory & metering** | Memory scope (pgvector) + async extraction consumer, context assembler, long-running sessions w/ checkpointing, token metering → per-key cost, OTel GenAI dashboards | Existing cluster | pgvector schema; Kafka topic |
| **P2 — Self-hosted serving** *(conditional)* | vLLM deployment, GPU autoscaling, prefix caching, shadow→canary rollout vs Bedrock baseline | **Dedicated GPU node group** | **Funded GPU node group** + a break-even analysis (§6) showing sustained utilization |
| **P3 — Optimization** *(conditional)* | KV offload tiering (LMCache), semantic cache where safe, multi-model packing, cost-priority routing | GPU node group | P2 live with real traffic data |

**The phasing is the design decision.** P0+P1 deliver the entire control plane — one authenticated front door, budgets, fallback, memory, cost attribution — at near-zero marginal infra cost, on the cluster that exists today. They are also **exactly the prerequisites** for P2: once the gateway owns routing and the registry owns rollout, adding vLLM is registering a deployment and shifting weight. **No caller changes, no rewrite.** If the GPU is never funded, P0+P1 remain independently valuable — which is the property that makes this phasing correct rather than merely cautious.

---

## 11. Observability, cost & governance

**Reuses the existing stack** — OTLP→Jaeger + Prometheus + JSON logs with trace correlation (`utils/observability` on the Go side, `src/observability.py` on the Python side). No new platform.

**LLM-specific SLIs** (standard p99 latency alone is misleading for streaming):

| Metric | Why it matters |
|---|---|
| **TTFT** (time to first token) | The perceived-latency SLI for streaming. |
| **ITL** (inter-token latency) / tokens-per-sec | Steady-state generation speed; degrades first under batch pressure. |
| **Queue depth & queue wait** | The true saturation signal on GPU — **the autoscaling trigger**. |
| **KV cache utilization** | vLLM's real memory pressure; predicts preemption before it happens. |
| **Prefix / response cache hit rate** | Validates §8 actually pays off — otherwise the cache is pure complexity. |
| **Tokens in/out per request, per key** | The cost primitive. Everything in §5.4 depends on it. |
| **Fallback rate & served-model distribution** | Silent quality degradation detector — a rising fallback rate means callers are quietly getting a weaker model. |
| **Time-to-recover / breaker state** | Resilience actually working. |

**Tracing:** OTel **GenAI semantic conventions** (`gen_ai.*`) so spans carry model, token counts and finish reason in a standard shape — one span per inference, child of the caller's trace, joining the existing Jaeger graph.

**Cost metering:** every response's usage → a metering event (Kafka) → per-key/team/model rollup. This is what makes budgets (§5.4) enforceable and chargeback possible. **Reconcile metered tokens against the provider bill** — drift means a metering bug, and an unnoticed metering bug means unenforced budgets.

**Governance:** audit every inference with caller identity, model version served, token counts and cache/fallback disposition. Prompt/response bodies are **sensitive by default** — log references and hashes, gate body capture behind an explicit per-route flag with retention limits, and never let them reach a third-party trace backend (consistent with the sibling HLD's no-egress stance on traces).

---

## 12. Open decisions

1. **Is a GPU node group fundable?** This gates P2 entirely. If the answer is a durable no, the platform is a gateway + state plane over hosted providers — which is a perfectly coherent end state and should be stated as such rather than left as permanently-deferred P2.
2. **Gateway: adopt vs build** (§5.6). Recommendation is adopt; the decision needs an owner and a verification pass on streaming-retry and token-aware limiting.
3. **Which model class justifies self-hosting first?** Embeddings and small rerankers have far better GPU economics (high throughput, small VRAM) than a large chat model, and are the more likely first self-hosted workload. Worth evaluating before assuming chat.
4. **Does `videostreamingplatform-recommendations` migrate onto the gateway?** It has working Bedrock/Ollama providers already. Migration is the proof that the gateway abstraction holds — but it is also a change to a live service. Recommend migrating it in P1, after budgets and metering exist.
5. **Session/memory split** (§7) — confirm one service is acceptable, or name the driver that forces two.
6. **Repo & chart ownership** — does this platform follow the workspace convention (service repo owns code, `-infra` owns Helm charts), or is `ai_platform` self-contained? Affects P0 scaffolding.
7. **Multi-tenancy model** — is a "tenant" a team, or an end customer? Changes the isolation requirements on the cache key (§8) and memory scope (§7.2) substantially.

---

## Appendix — glossary

- **KV cache** — attention key/value tensors for already-processed tokens, held in GPU memory *inside the inference engine*. **Not** a datastore, **not** agent memory (§3).
- **PagedAttention** — vLLM's paged KV memory management; eliminates fragmentation and enables high concurrency.
- **Continuous batching** — admitting new requests into an in-flight GPU batch instead of waiting for the batch to drain. The main throughput mechanism.
- **Prefix caching** — reusing KV blocks across requests that share a prompt prefix.
- **TTFT / ITL** — time-to-first-token / inter-token latency. The two streaming SLIs.
- **Virtual key** — gateway-issued credential carrying a model allow-list, rate limit and budget; the unit of cost attribution and revocation.
- **Model alias** — stable caller-facing name (`chat-default`) resolved by the router to a concrete deployment. The indirection that makes model swaps a config change.
- **Shadow traffic** — mirroring live requests to a candidate model whose output is compared but never served.
