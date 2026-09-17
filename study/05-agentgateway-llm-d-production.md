# 05 — agentgateway + llm-d: a production inference setup

> Study notes for design work and interview preparation · researched 2026-09-17 · llm-d v0.9
> Points marked *(general knowledge)* weren't checked against the docs during this study.

## 1. Two routing decisions

A production LLM request goes through **two separate routing decisions**:

| Decision | Question | Owner |
|---|---|---|
| **Model / provider routing** | Who is calling, are they allowed, what's their budget, which model or provider serves them? | **agentgateway** |
| **Replica routing** | Of the N replicas running this model, which one should take the request, given each one's cache and load? | **llm-d scheduler (Endpoint Picker, EPP)** |

They connect through the **Kubernetes Gateway API Inference Extension (GAIE)**. agentgateway doesn't reimplement llm-d's scheduling; it asks the EPP over a standard protocol.

> **Interview one-liner:** "The gateway decides **what** serves a request, and the inference scheduler decides **where** it runs. A normal Kubernetes Service can't make the second decision, because it knows nothing about KV caches or queue depth."

## 2. When replica routing is needed at all

The KV cache belongs to **one replica of one model**. Replica-aware routing only helps when the router has to **choose between replicas of the same model**.

| Setup | Replica-aware routing? | Why |
|---|---|---|
| Each model has exactly **1** vLLM replica | **No** | The model name already decides the backend; agentgateway's model → backend mapping is enough |
| Any model has **2+ replicas** | **Yes, for that model** | Round-robin sends turn 5 of a conversation to a replica that doesn't hold turns 1–4 (the whole prompt gets processed again) and ignores queue length |
| Several LoRA adapters on one base model | Usually yes | Requests can go to a replica that already has the adapter loaded |

- **Standalone agentgateway (no Kubernetes):** there's no InferencePool or EPP.
  - agentgateway's built-in load balancing (picking the better of two replicas by health, latency and load) handles busy replicas but can't see prefix caches.
  - Sending a whole session to one replica would recover most of the cache benefit. *Not verified whether agentgateway supports this kind of sticky routing.*
- **With agentgateway you add one EPP per model that has multiple replicas, not a second gateway.**
  The vLLM production-stack router would duplicate that capability; skip it.

## 3. Production architecture

```
                         ┌──────────────────────────────────────────────┐
  apps / agents ───────► │ Edge: cloud LB + WAF + TLS                   │
                         └──────────────────────┬───────────────────────┘
                                                ▼
 ┌─────────────────────────────────────────────────────────────────────────────┐
 │ agentgateway (Kubernetes mode, 3+ replicas, HPA)          "WHAT"            │
 │  • JWT / API-key auth, per-tool access control (CEL)                        │
 │  • per-tenant token rate limits + spend caps (shared counters in Redis)     │
 │  • model aliases, provider failover (priority groups), retries, timeouts   │
 │  • MCP gateway (tools) + A2A (agent to agent)                              │
 │  • OpenTelemetry traces / metrics / audit                                  │
 └───────┬─────────────────────────────┬─────────────────────────┬────────────┘
         │ HTTPRoute: model=llama-70b  │ model=qwen-7b           │ fallback / external
         ▼                             ▼                         ▼
 ┌────────────────────┐      ┌────────────────────┐     Bedrock / Anthropic / …
 │ InferencePool A    │      │ InferencePool B    │
 │  ▲ ext_proc (gRPC) │      │  ▲ ext_proc        │
 │  │                 │      │  │                 │
 │ EPP A (llm-d       │      │ EPP B              │   "WHERE"
 │ scheduler, HA)     │      │                    │
 │  filter→score→pick │      │                    │
 └──┬──────────────┬──┘      └────────┬───────────┘
    │              │                  │
    ▼              ▼                  ▼
 ┌─────────┐   ┌──────────────────┐   vLLM pods (combined prefill+decode)
 │ PREFILL │   │ DECODE pods      │
 │ vLLM    │◄──┤ vLLM + routing   │   ◄── KV cache transferred over RDMA (NIXL)
 │ pods    │   │ sidecar          │
 └────┬────┘   └────────┬─────────┘
      │ KV events (ZMQ) │
      └───────┬─────────┘
              ▼
   KV-cache indexer ──► feeds EPP's precise prefix-cache scoring
   Tiered KV offload: GPU memory → CPU RAM → local NVMe / shared storage

 Supporting pieces:
  • Autoscaling: KEDA (EPP queue depth) or HPA + WVA (Workload Variant Autoscaler)
  • GPU node pools: tainted, labelled by accelerator; RDMA NICs for prefill/decode split
  • Model weights: registry → S3 → pre-staged on node NVMe (fast model loading)
  • GitOps (Argo CD): Gateway, HTTPRoute, InferencePool, EPP config, vLLM Deployments
  • Observability: Prometheus (vLLM + EPP metrics), OTel → tracing, dashboards
```

llm-d also ships its own **"llm-d Router"**: a proxy that conforms to GAIE, plus the EPP.
In this design **agentgateway takes the place of that proxy**, and llm-d supplies the EPP and everything behind it.

An **InferencePool** is best thought of as "a Service optimised for LLMs": a group of pods serving the same base model on the same hardware and model server.
A **variant** is a subgroup of those pods, identified by pod labels.

## 4. What happens to one request

1. **Agent → agentgateway.** It sends `POST /v1/chat/completions` with `model: llama-70b` and a JWT.
2. **agentgateway applies policy.** It checks authentication, the per-tool access rule, and the token limit and budget.
   It then matches an `HTTPRoute` whose backend is **InferencePool A** rather than an ordinary Service.
3. **agentgateway asks EPP A which replica to use,** over Envoy's **ext_proc** gRPC protocol in streaming mode. agentgateway isn't Envoy, but it speaks this protocol.
4. **EPP A picks a replica:**
   - **Filter:** drop unhealthy pods and pods with the wrong role or adapter.
   - **Score:** rate each remaining pod on prefix-cache match, queue length, KV-cache usage, and optionally predicted latency.
   - **Pick:** take the best combined score.
5. **EPP returns its choice** in the header or metadata key **`x-gateway-destination-endpoint`** (`ip:port`).
   - A comma-separated list gives **ordered fallbacks**: with retries configured, the proxy works down the list.
   - If no pod is ready, EPP returns **503**. If the request should be dropped (load shedding), it returns **429**.
   - The gateway can offer a subset of candidate pods via `envoy.lb.subset_hint` → `x-gateway-destination-endpoint-subset`.
6. **When prefill and decode are split:**
   - EPP sets **`x-prefiller-host-port`**.
   - The request goes to a **decode** pod, whose **routing sidecar** coordinates both steps.
   - With vLLM's `nixlv2` protocol, the sidecar first sends the prompt to the prefill pod with **`max_tokens=1`**, which builds the KV cache.
   - It captures the KV transfer details from that response and forwards the enriched request to its local decode vLLM.
   - The decode vLLM pulls the KV blocks over **NIXL** and streams the tokens.
   - (With SGLang, the prefill request is sent in the background and the decode request is sent immediately.)
7. **Feedback.**
   - The gateway reports which pod actually served the request (`x-gateway-destination-endpoint-served`).
   - With precise routing, vLLM publishes **KV events** so the indexer knows exactly which blocks are on which pod.
8. **The response streams back** through agentgateway, which records token usage against the budget and emits traces.

## 5. Who owns what

| Concern | agentgateway | llm-d EPP | vLLM | Kubernetes / platform |
|---|---|---|---|---|
| Authentication, tenancy, per-tool access | ✅ | | | NetworkPolicy: pods reachable only through the gateway |
| Token limits, budgets | ✅ | | | Redis for shared counters |
| Model alias → pool, provider failover | ✅ | | | |
| Retries, timeouts (before the first token only) | ✅ | ordered fallback list | | |
| Removing unhealthy backends | ✅ (between providers and pools) | ✅ (between pods) | | readiness probes |
| Replica choice (cache hits, load) | | ✅ | | |
| Load shedding, priority, fairness | coarse (per tenant) | ✅ flow control | `max-num-seqs` | |
| KV cache, batching | | | ✅ PagedAttention, continuous batching | |
| Splitting prefill and decode | | ✅ decides | ✅ executes (NIXL) | RDMA networking |
| Autoscaling | | ✅ queue signals | ✅ KV-usage signals | KEDA / HPA + WVA |

## 6. llm-d building blocks

llm-d documents its tested configurations as **"well-lit paths"**:
- optimized baseline
- predicted latency
- precise prefix-cache routing
- tiered prefix cache
- P/D disaggregation
- wide expert parallelism
- flow control
- workload autoscaling
- multi-model routing
- fast model actuation

On top of these are workload guides: **agentic**, multimodal, batch.

### 6a. Prefix-cache-aware routing

| | **Approximate** | **Precise** |
|---|---|---|
| How it knows what's cached | Remembers where it recently sent each prefix (a hash chain over blocks) and **assumes** those pods still hold them | vLLM reports **KV events over ZeroMQ** to a **KV-cache indexer**, giving a consistent view of the whole fleet |
| Tokenisation | Estimated from a characters-per-token ratio | Exact, using vLLM's `/v1/completions/render` |
| Plugins | `approx-prefix-cache-producer`, `prefix-cache-scorer` | `token-producer`, `precise-prefix-cache-producer`, `prefix-cache-scorer`, indexer |
| Accuracy | Drifts when caches are evicted | Exact; routing decisions are recorded immediately to close timing gaps |
| Extra components | None | Tokenizer + ZeroMQ + indexer |
| Use for | Simple, uniform workloads | Large production, especially with prefill/decode split |

**Affinity vs load:**
- If every request follows its cached prefix, the pod holding a popular system prompt gets overloaded. llm-d's agentic guide calls this "hot-spotting".
- The fix is a **weighted combination** of prefix, queue and KV-usage scores, plus more replicas and tiered offload.

### 6b. Prefill/decode (P/D) disaggregation

- **Prefill** (processing the prompt) is limited by **compute**. **Decode** (generating tokens) is limited by **memory bandwidth**.
  Running both on the same pods lets one long prompt stall token generation for everyone else on that pod.
- **Benefit:** each side can be sized independently, for example more tensor parallelism on decode. For mixture-of-experts models with data/expert parallelism the split is essential, to avoid GPUs idling between pipeline stages.
- **Selective disaggregation:** a "decider" checks how much of the prompt is already cached on the chosen decode pod. Only requests with a large uncached portion go to a prefill pod first.
- **Pod roles** come from the label `llm-d.ai/role`: `prefill`, `decode` or `prefill-decode`.
  The EPP runs separate prefill and decode scheduling profiles, each doing filter → score → pick.
- **Transfer:** NIXL over UCX, UCCL or libfabric, on **InfiniBand, RoCE or AWS EFA**. Over TCP it is "extremely slow", suitable for development only.
  **RDMA is required in production.**

### 6c. Tiered KV offloading

KV cache overflows from GPU memory to CPU RAM, then to local or shared storage.
Agents pause during tool calls and human turns; offloading keeps those idle sessions instead of evicting them and recomputing later.

### 6d. Wide expert parallelism

For mixture-of-experts models (e.g. DeepSeek), experts are spread across many GPUs, and splitting prefill from decode is effectively required.
*(General knowledge)* These multi-node pods are typically deployed with LeaderWorkerSet.

### 6e. Autoscaling

| | **KEDA + EPP metrics** | **HPA + WVA** |
|---|---|---|
| Signal | Demand side: EPP queue length, active requests | Supply side: KV-cache usage, queue length |
| Scope | Each pool scales on its own; same hardware throughout | **Global** across models and GPU types |
| Strengths | Simple; scale-to-zero without a Kubernetes feature gate | Shares GPUs fairly when they're scarce, scales ahead of SLO breaches, counts pods still waiting to be scheduled |

**Never autoscale GPU inference on CPU usage.** GPU utilisation is also misleading, because a well-batched pod can sit near 100% and still be healthy.
Queue length and KV-cache pressure are what show real saturation.

### 6f. Agentic serving

Agent workloads differ from single requests:
- **Massive context reuse:** tool-loop turns, reasoning branches and sub-agents share most of their context.
- **The goal is end-to-end completion time**, not the latency of individual calls.
- **Mixed lifetimes:** durable system prompts and tools versus short-lived reasoning state.

Today, "the stack recomputes context it has already seen, evicts long-lived sessions under memory pressure, and hot-spots whichever replica holds a popular prefix."

llm-d's recipe combines four paths:
- optimized baseline
- tiered KV offload
- precise prefix-cache routing
- P/D disaggregation

## 7. Production checklist

- **High availability**
  - agentgateway: 3+ replicas across zones.
  - EPP highly available (llm-d v0.9 added router HA). A GPU pool should never depend on a single scheduler pod.
- **EPP failure mode.** Decide what happens when the EPP is unreachable:
  - **fail open:** plain load balancing; the service stays up but runs worse;
  - **fail closed:** 503; correctness is protected at the cost of availability.

  InferencePool has a setting for this; check the field name for your GAIE version.
- **Retry boundaries**
  - Retry only before the first token.
  - Rely on EPP's fallback list rather than stacking retries across layers.
- **Security**
  - mTLS inside the mesh.
  - vLLM reachable only from the gateway.
  - Weights bucket protected by IAM/IRSA.
  - Model artifact checksums verified at load time.
  - Prompts captured in logs only when a policy allows it.
- **Multi-tenancy**
  - Budgets and limits in agentgateway.
  - Priority and fairness in EPP flow control. *(General knowledge)* current GAIE expresses request priority through an `InferenceObjective` resource.
  - Never share response caches across tenants.
- **Model rollout**
  - Each new model version gets its own InferencePool.
  - Canary via **weighted HTTPRoute backends**, or mirror traffic for a shadow test first.
  - Promote only after evals and SLOs pass.
- **GPU infrastructure**
  - Dedicated, tainted node pools labelled by accelerator.
  - RDMA NICs only where prefill and decode are split.
  - Weights pre-staged on local NVMe, because model loading takes minutes.
- **Observability.** Metrics that matter:
  - TTFT (time to first token) and ITL (inter-token latency), p50/p99, per pool;
  - EPP queue length;
  - prefix-cache hit rate;
  - KV-cache usage;
  - prefill/decode transfer latency;
  - tokens per tenant.

  Tie each request's trace from the gateway through EPP to the vLLM pod.
- **Cost**
  - Size pools for peak load.
  - Use WVA across GPU types.
  - Send low-priority batch traffic through flow control rather than giving it its own GPUs.

## 8. When not to use it

| Situation | What to do |
|---|---|
| Each model has one replica | agentgateway → Service. No EPP. |
| No RDMA networking | Skip the prefill/decode split; keep prefix routing and load-aware routing |
| Short prompts with little shared context | Prefix routing gains little; load-aware scoring is enough |
| Development / learning without a GPU | `llm-d-inference-sim` behaves like vLLM without a model |

**Mapping to `AI-INFERENCE-PLATFORM-HLD.md`:**

| Phase | What it runs |
|---|---|
| P0 / P1 | agentgateway in front of Bedrock / Ollama |
| P2 | One vLLM pool per model, approximate prefix routing, KEDA |
| Later, on a funded GPU fleet with RDMA | Prefill/decode split and WVA |

## 9. Interview questions

1. **Why not put vLLM pods behind a normal Kubernetes Service?**
   Kube-proxy picks a pod at random or by connection. It can't see KV-cache contents or queue length, so it wastes cache hits, overloads busy pods, and time-to-first-token suffers.
2. **Why two routing layers?**
   They make different kinds of decision: policy about callers (who, budget, which model) versus the state of the fleet (cache, load). GAIE joins them, so either one can be replaced without touching the other.
3. **How does the gateway learn which pod was chosen?**
   Through ext_proc gRPC. EPP returns `x-gateway-destination-endpoint` (optionally an ordered list of fallbacks), or 503/429.
4. **Approximate vs precise prefix routing?**
   Approximate is cheap but assumes caches match its own routing history, so it drifts after evictions. Precise uses vLLM's KV events and exact tokenisation, so it's accurate but adds a tokenizer, ZeroMQ and an indexer.
5. **What is P/D disaggregation, and when does it hurt?**
   It separates compute-bound prefill from memory-bandwidth-bound decode.
   It hurts without RDMA, because the KV transfer costs more than the split saves. It doesn't help short or already-cached prompts, which is why llm-d decides per request.
6. **What do you autoscale on?**
   Queue length and KV-cache usage, not CPU and not raw GPU utilisation.
7. **What if the EPP goes down?**
   Run it highly available. Choose fail-open or fail-closed according to the SLO. Alert on EPP latency, since it's on every request's path.
8. **How do you avoid hot-spotting on a popular prefix?**
   Weighted scoring (prefix + queue + KV usage), more replicas, and tiered offload.
9. **How do you canary a new model?**
   A new InferencePool, weighted HTTPRoute (or mirrored traffic first), compare evals and TTFT/ITL, then promote or roll back through GitOps.
10. **Where do retries live?**
    At the gateway, only before the first token. EPP's fallback list covers retrying on another pod. Never stack retries across layers, because the counts multiply (see [02](02-resilience-placement.md)).
11. **Is a "KV cache service" something you build?**
    No. The KV cache lives inside vLLM (PagedAttention).
    - What you *build or configure* around it: prefix routing, offload tiers, provider prompt caching, and a response cache at the gateway.
    - Agent memory is a separate store (e.g. pgvector).

## 10. Laptop lab plan (not yet done)

1. Create a Kind cluster and install the Gateway API + GAIE CRDs.
2. Install agentgateway in Kubernetes mode.
3. Deploy 2–3 `llm-d-inference-sim` pods as one InferencePool, add an EPP, and route to it through an HTTPRoute. Watch which pod each request lands on.
4. Change the scorer weights and send repeated prefixes to see cache affinity at work.
5. Optionally, add the `vllm-cpu` container ([01](01-vllm-on-cpu-local-lab.md)) as a single-replica backend.

Memory is tight (7.5 GiB): stop `vllm-cpu` while running the sims.

## Sources
- [agentgateway + llm-d + GAIE](https://agentgateway.dev/blog/2026-03-19-agentgateway-llm-d-gaie-inference-serving/)
- [agentgateway inference routing](https://agentgateway.dev/docs/kubernetes/1.0.x/inference/)
- [agentgateway multiple inference pools](https://agentgateway.dev/docs/kubernetes/main/llm/multiple-inference-pools/)
- [llm-d architecture](https://llm-d.ai/docs/architecture)
- [llm-d P/D disaggregation](https://llm-d.ai/docs/architecture/advanced/disaggregation)
- [llm-d prefix-cache aware routing](https://llm-d.ai/docs/architecture/advanced/kv-management/prefix-cache-aware-routing)
- [llm-d agentic serving](https://llm-d.ai/docs/well-lit-paths/workloads/agentic-serving)
- [llm-d autoscaling](https://llm-d.ai/docs/architecture/advanced/autoscaling)
- [GAIE overview](https://gateway-api-inference-extension.sigs.k8s.io/)
- [GAIE Endpoint Picker Protocol](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/docs/proposals/004-endpoint-picker-protocol/README.md)
- [GAIE InferencePool](https://gateway-api-inference-extension.sigs.k8s.io/api-types/inferencepool/)
- [GAIE — Getting started](https://gateway-api-inference-extension.sigs.k8s.io/guides/implementers/)
- [GAIE issue #2018 — EPP per InferencePool](https://github.com/kubernetes-sigs/gateway-api-inference-extension/issues/2018)
