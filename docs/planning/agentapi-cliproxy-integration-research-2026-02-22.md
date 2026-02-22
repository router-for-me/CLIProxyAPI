# AgentAPI + cliproxyapi++ integration research (2026-02-22)

## Executive summary

- `agentapi` and `cliproxyapi++` are complementary rather than redundant.
- `agentapi` is strong at **agent session lifecycle** (message, status, events, host attachment) with terminal-backed adapters.
- `cliproxyapi++` is strong at **model/protocol transport** (OpenAI-style APIs, provider matrix, OAuth/session refresh, routing/failover).
- A practical tandem pattern is:
  - use `agentapi` for agent orchestration control,
  - use `cliproxyapi++` as the model transport or fallback provider layer,
  - connect both through a thin orchestration service with clear authz/routing boundaries.

## What agentapi is good at (as of 2026-02-22)

From the upstream repo:
- Provides HTTP control for coding agents such as Claude Code, Goose, Aider, Gemini, Codex, Cursor CLI, etc.
- Documents 4 conversation endpoints:
  - `POST /message` to send user input,
  - `GET /messages` for history,
  - `GET /status` for running/stable state,
  - `GET /events` SSE for event streaming.
- Includes a documented OpenAPI schema and `/docs` UI.
- Explicitly positions itself as a backend in MCP server compositions (one agent controlling another).
- Roadmap notes MCP + Agent2Agent support as pending features.

## Why cliproxyapi++ in tandem

`cliproxyapi++` is tuned for provider transport and protocol normalization (OpenAI-compatible paths and OAuth/session-heavy provider support). That gives you:
- Stable upstream-facing model surface for clients expecting OpenAI/chat-style APIs.
- Centralized provider switching, credential/session handling, and health/error routing.
- A predictable contract for scaling many consumer apps without binding each one to specific CLI quirks.

This does not solve all `agentapi` lifecycle semantics by itself; `agentapi` has terminal-streaming/session parsing behaviors that are still value-add for coding CLI automation.

## Recommended tandem architecture (for your stack)

1. **Gateway plane**
   - Keep `cliproxyapi++` as the provider/generative API layer.
   - Expose it internally as `/v1/*` and route non-agent consumers there.

2. **Agent-control plane**
   - Run `agentapi` per workflow (or shared multi-tenant host with strict isolation).
   - Use `/message`, `/messages`, `/status`, and `/events` for orchestration state and long-running control loops.

3. **Orchestrator service**
   - Introduce a small orchestrator that translates high-level tasks into:
     - model calls (via `cliproxyapi++`) for deterministic text generation/translation,
     - session actions (via `agentapi`) when terminal-backed agent execution is needed.

4. **Policy plane**
   - Add policy on top of both layers:
   - secret management and allow-lists,
   - host/origin/CORS constraints,
   - request logging + tracing correlation IDs across both control and model calls.

5. **Converge on protocol interoperability**
 - Track `agentapi` MCP/A2A roadmap and add compatibility tests once MCP is GA or when A2A adapters are available.

## Alternative/adjacent options to evaluate

### Multi-agent orchestration frameworks
- **AutoGen**
  - Good for message-passing and multi-agent collaboration patterns.
  - Useful when you want explicit conversation routing and extensible layers for tools/runtime.
- **LangGraph**
  - Strong for graph-based stateful workflows, durable execution, human-in-the-loop, and long-running behavior.
- **CrewAI**
  - Role-based crew/fleet model with clear delegation, crews/flights-style orchestration, and tool integration.
- **OpenAI Agents SDK**
  - Useful when you are already on OpenAI APIs and need handoffs + built-in tracing/context patterns.

### Protocol direction (standardization-first)
- **MCP (Model Context Protocol)**
  - Open standard focused on model ↔ data/tool/workflow interoperability, intended as a universal interface.
  - Particularly relevant for reducing N×M integration work across clients/tools.
- **A2A (Agent2Agent)**
  - Open protocol for inter-agent communication, task-centric workflows, and long-running collaboration.
  - Designed for cross-framework compatibility and secure interop.

### Transport alternatives
- Keep OpenAI-compatible proxying if your clients are already chat/completion API-native.
- If you do not need provider-heavy session orchestration, direct provider SDK routing (without cliproxy) is a simpler but less normalized path.

## Suggested phased pilot

### Phase 1: Proof of contract (1 week)
- Spin up `agentapi` + `cliproxyapi++` together locally.
- Validate:
  - `/message` lifecycle and SSE updates,
  - `/v1/models` and `/v1/metrics` from cliproxy,
  - shared tracing correlation between both services.

### Phase 2: Hardened routing (2 weeks)
- Add orchestrator that routes:
  - deterministic API-style requests to `cliproxyapi++`,
  - session-heavy coding tasks to `agentapi`,
  - shared audit trail plus policy checks.
- Add negative tests around `agentapi` command-typing and cliproxy failovers.

### Phase 3: Standards alignment (parallel)
- Track A2A/MCP progress and gate integration behind a feature flag.
- Build adapter layer so either transport (`agentapi` native endpoints or MCP/A2A clients) can be swapped with minimal orchestration changes.

## Research links

- AgentAPI repository: https://github.com/coder/agentapi
- AgentAPI OpenAPI/roadmap details: https://github.com/coder/agentapi
- MCP home: https://modelcontextprotocol.io
- A2A protocol: https://a2a.cx/
- OpenAI Agents SDK docs: https://platform.openai.com/docs/guides/agents-sdk/ and https://openai.github.io/openai-agents-python/
- AutoGen: https://github.com/microsoft/autogen
- LangGraph: https://github.com/langchain-ai/langgraph and https://docs.langchain.com/oss/python/langgraph/overview
- CrewAI: https://docs.crewai.com/concepts/agents

## Research appendix (decision-focused)

- `agentapi` gives direct control-plane strengths for long-lived terminal sessions:
  - `/message`, `/messages`, `/status`, `/events`
  - MCP and Agent2Agent are on roadmap, so native protocol parity is not yet guaranteed.
- `cliproxyapi++` gives production proxy strengths for model-plane demands:
  - OpenAI-compatible `/v1` surface expected by most clients
  - provider fallback/routing logic under one auth and config envelope
  - OAuth/session-heavy providers with refresh workflows (Copilot, Kiro, etc.)
- For projects that mix command-line agents with OpenAI-style tooling, `agentapi` + `cliproxyapi++` is the least disruptive tandem:
  - keep one stable model ingress (`/v1/*`) for downstream clients
  - route agent orchestration through `/message` and `/events`
  - centralize auth/rate-limit policy in the proxy side, and process-level isolation on control-plane side.

### Alternatives evaluated

1. **Go with `agentapi` only**
   - Pros: fewer moving parts.
   - Cons: you inherit provider-specific auth/session complexity that `cliproxyapi++` already hardened.

2. **Go with `cliproxyapi++` only**
   - Pros: strong provider abstraction and OpenAI compatibility.
   - Cons: missing built-in terminal session lifecycle orchestration of `/message`/`/events`.

3. **Replace with LangGraph or OpenAI Agents SDK**
   - Pros: strong graph/stateful workflows and OpenAI-native ergonomics.
   - Cons: meaningful migration for existing CLI-first workflows and provider idiosyncrasies.

4. **Replace with CrewAI or AutoGen**
   - Pros: flexible multi-agent frameworks and role/task orchestration.
   - Cons: additional abstraction layer to preserve existing CLIs and local session behavior.

5. **Protocol-first rewrite (MCP/A2A-first)**
   - Pros: long-run interoperability.
   - Cons: both `agentapi` protocol coverage and our local integrations are still evolutionary, so this is best as a v2 flag.

### Recommended near-term stance

- Keep the tandem architecture and make it explicit via:
  - an orchestrator service,
  - policy-shared auth and observability,
  - adapter contracts for `message`-style control and `/v1` model calls,
  - one shared correlation-id across both services for auditability.
- Use phase-gate adoption:
  - Phase 1: local smoke on `/message` + `/v1/models`
  - Phase 2: chaos/perf test with provider failover + session resume
  - Phase 3: optional MCP/A2A compatibility layer behind flags.

## Full research inventory (2026-02-22)

I pulled all `https://github.com/orgs/coder/repositories` payload and measured the full `coder`-org working set directly:

- Total repos: 203
- Archived repos: 19
- Active repos: 184
- `updated_at` within ~365 days: 163
- Language distribution top: Go (76), TypeScript (25), Shell (16), HCL (11), Python (5), Rust (4)
- Dominant topics: ai, ide, coder, go, vscode, golang

### Raw inventories (generated artifacts)

- `/tmp/coder_org_repos_203.json`: full payload with index, full_name, language, stars, forks, archived, updated_at, topics, description
- `/tmp/coder_org_203.md`: rendered table view of all 203 repos
- `/tmp/relative_top60.md`: top 60 adjacent/relative repos by recency/star signal from GitHub search

Local generation command used:

```bash
python - <<'PY'
import json, requests
rows = []
for page in range(1, 6):
    data = requests.get(
        "https://api.github.com/orgs/coder/repos",
        params={"per_page": 100, "page": page, "type": "all"},
        headers={"User-Agent": "codex-research"},
    ).json()
    if not data:
        break
    rows.extend(data)

payload = [
    {
        "idx": i + 1,
        "full_name": r["full_name"],
        "html_url": r["html_url"],
        "language": r["language"],
        "stars": r["stargazers_count"],
        "forks": r["forks_count"],
        "archived": r["archived"],
        "updated_at": r["updated_at"],
        "topics": ",".join(r.get("topics") or []),
        "description": r["description"],
    }
    for i, r in enumerate(rows)
]
open("coder_org_repos_203.json", "w", encoding="utf-8").write(json.dumps(payload, indent=2))
PY
PY
```

### Top 20 coder repos by stars (for your stack triage)

1. `coder/code-server` (76,331 stars, TypeScript)
2. `coder/coder` (12,286 stars, Go)
3. `coder/sshcode` (5,715 stars, Go)
4. `coder/websocket` (4,975 stars, Go)
5. `coder/claudecode.nvim` (2,075 stars, Lua)
6. `coder/ghostty-web` (1,852 stars, TypeScript)
7. `coder/wush` (1,413 stars, Go)
8. `coder/agentapi` (1,215 stars, Go)
9. `coder/mux` (1,200 stars, TypeScript)
10. `coder/deploy-code-server` (980 stars, Shell)

### Top 60 additional relative repos (external, adjacent relevance)

1. `langgenius/dify`
2. `x1xhlol/system-prompts-and-models-of-ai-tools`
3. `infiniflow/ragflow`
4. `lobehub/lobehub`
5. `dair-ai/Prompt-Engineering-Guide`
6. `OpenHands/OpenHands`
7. `hiyouga/LlamaFactory`
8. `FoundationAgents/MetaGPT`
9. `unslothai/unsloth`
10. `huginn/huginn`
11. `microsoft/monaco-editor`
12. `jeecgboot/JeecgBoot`
13. `2noise/ChatTTS`
14. `alibaba/arthas`
15. `reworkd/AgentGPT`
16. `1Panel-dev/1Panel`
17. `alibaba/nacos`
18. `khoj-ai/khoj`
19. `continuedev/continue`
20. `TauricResearch/TradingAgents`
21. `VSCodium/vscodium`
22. `feder-cr/Jobs_Applier_AI_Agent_AIHawk`
23. `CopilotKit/CopilotKit`
24. `viatsko/awesome-vscode`
25. `voideditor/void`
26. `bytedance/UI-TARS-desktop`
27. `NvChad/NvChad`
28. `labring/FastGPT`
29. `datawhalechina/happy-llm`
30. `e2b-dev/awesome-ai-agents`
31. `assafelovic/gpt-researcher`
32. `deepset-ai/haystack`
33. `zai-org/Open-AutoGLM`
34. `conwnet/github1s`
35. `vanna-ai/vanna`
36. `BloopAI/vibe-kanban`
37. `datawhalechina/hello-agents`
38. `oraios/serena`
39. `qax-os/excelize`
40. `1Panel-dev/MaxKB`
41. `bytedance/deer-flow`
42. `coze-dev/coze-studio`
43. `LunarVim/LunarVim`
44. `camel-ai/owl`
45. `SWE-agent/SWE-agent`
46. `dzhng/deep-research`
47. `Alibaba-NLP/DeepResearch`
48. `google/adk-python`
49. `elizaOS/eliza`
50. `NirDiamant/agents-towards-production`
51. `shareAI-lab/learn-claude-code`
52. `AstrBotDevs/AstrBot`
53. `AccumulateMore/CV`
54. `foambubble/foam`
55. `graphql/graphiql`
56. `agentscope-ai/agentscope`
57. `camel-ai/camel`
58. `VectifyAI/PageIndex`
59. `Kilo-Org/kilocode`
60. `langbot-app/LangBot`
