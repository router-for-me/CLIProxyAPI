# cliproxyapi++ 🚀

[![Go Report Card](https://goreportcard.com/badge/github.com/KooshaPari/cliproxyapi-plusplus)](https://goreportcard.com/report/github.com/KooshaPari/cliproxyapi-plusplus)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker Pulls](https://img.shields.io/docker/pulls/kooshapari/cliproxyapi-plusplus.svg)](https://hub.docker.com/r/kooshapari/cliproxyapi-plusplus)
[![GitHub Release](https://img.shields.io/github/v/release/KooshaPari/cliproxyapi-plusplus)](https://github.com/KooshaPari/cliproxyapi-plusplus/releases)

English | [中文](README_CN.md)

**cliproxyapi++** is the definitive high-performance, security-hardened fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). Designed with a "Defense in Depth" philosophy and a "Library-First" architecture, it provides an OpenAI-compatible interface for proprietary LLMs with enterprise-grade stability.

---

## 🏆 Deep Dive: The `++` Advantage

Why choose **cliproxyapi++** over the mainline? While the mainline focus is on open-source stability, the `++` variant is built for high-scale, production environments where security, automated lifecycle management, and broad provider support are critical.

Full feature-by-feature change reference:

- **[Feature Changes in ++](./docs/FEATURE_CHANGES_PLUSPLUS.md)**

### 📊 Feature Comparison Matrix

| Capability | Mainline | CLIProxyAPI+ | **cliproxyapi++** | Granular Notes |
| :--- | :---: | :---: | :---: | :--- |
| **OpenAI-compatible `chat/completions`** | ✅ | ✅ | ✅ | Full request/response surface parity remains present in all variants. |
| **OpenAI-compatible `models` listing** | ✅ | ✅ | ✅ | Endpoint behavior is feature-compatible across variants. |
| **Advanced provider routing table** | ⚠️ | ✅ | ✅ | Route policy and fallback behavior is centralized and tunable in `++`. |
| **Prefix/model mapping rules** | ⚠️ | ✅ | ✅ | `++` enforces stricter provider/model translation for operational control. |
| **Streaming behavior (`stream=true`)** | ⚠️ | ⚠️ | ✅ | Streaming paths hardened for lower drop-off under token pressure in `++`. |
| **Management API (`/v0/*`)** | ⚠️ | ✅ | ✅ | `++` keeps operational endpoints and control APIs in the service plane. |
| **TUI auth management surface** | ❌ | ✅ | ✅ | Provider list, detail view, and account-level actions available in `+` and `++`. |
| **Auth add/upload flow (TUI + API)** | ❌ | ⚠️ | ✅ | New `a` and file-upload handler path in `++` supports add/import workflows. |
| **TheGent login integration** | ❌ | ⚠️ | ✅ | `--thegent-login` dispatch uses TheGent-compatible flow in `++`. |
| **Roo / Kilo / Copilot / Kiro parity** | ⚠️ | ⚠️ | ✅ | `++` includes explicit dedicated flows and richer lifecycle recovery. |
| **Background token refresh worker** | ❌ | ❌ | ✅ | Auto-refresh job handles expiry churn before request-time failure. |
| **Token lifecycle visibility** | ❌ | ⚠️ | ✅ | `++` exposes stronger token metadata and status checks in management. |
| **Cooldown / rate governance** | ❌ | ⚠️ | ✅ | `++` applies provider state-aware cooldown and retry discipline. |
| **Provider failover continuity** | ⚠️ | ⚠️ | ✅ | Route redirection favors healthy upstreams during transient faults. |
| **Config path discovery** | ⚠️ | ⚠️ | ✅ | `++` surfaces env-driven, mount-aware candidate resolution and explicit hints. |
| **Strict config validation mode** | ⚠️ | ⚠️ | ✅ | `--config-validate` runs strict YAML schema + runtime validation path. |
| **Core code importability** | ❌ | ❌ | ✅ | `++` exposes `pkg/llmproxy` for external package consumption. |
| **Library-first composition** | ⚠️ | ⚠️ | ✅ | Proxy core and auth helpers are reusable modules in `++`. |
| **Security hardening** | Basic | Basic | ✅ | Hardened defaults, guardrails, and anti-abuse defaults are expanded in `++`. |
| **Container image posture** | Basic | Basic | ✅ | `++` uses a production-oriented Docker base and deployment profile. |
| **Release workflow automation** | ⚠️ | ⚠️ | ✅ | Tagged push triggers GoReleaser, builds binaries, uploads archive artifacts, and refreshes release notes. |
| **Automated release batching** | ⚠️ | ⚠️ | ✅ | Pushes to `main` run `releasebatch --mode create --target main` to mint the next version batch, publish tag, and publish release notes. |
| **Version-to-release linting** | ⚠️ | ⚠️ | ✅ | `task quality:release-lint` + CI checks required pre-push/PR. |
| **Cross-platform artifact coverage** | ⚠️ | ⚠️ | ✅ | Linux, macOS, Windows archives are generated and checksummed. |
| **Release tag protocol** | ⚠️ | ⚠️ | ✅ | Supports `v<major>.<minor>.<patch>` and `v<major>.<minor>.<patch>-<batch>` workflows used by `releasebatch`. |
| **Release notes generation** | ⚠️ | ⚠️ | ✅ | CI regenerates changelog body from git commit range and updates the GitHub release notes payload. |
| **Release signing / trust chain** | ⚠️ | ⚠️ | ⚠️ | GoReleaser publishes `checksums.txt`; checksum-only artifacts are currently available, no code-signing certificate integration yet. |
| **CLI-first provider auth orchestration** | ⚠️ | ⚠️ | ✅ | `--<provider>-login` and `--thegent-login=<provider>` flows plus `--setup` for guided onboarding. |
| **Provider management ergonomics** | ⚠️ | ⚠️ | ✅ | Management endpoints plus optional TUI expose auth-file lifecycle, status, model mappings, and key rotation controls. |
| **Config file operation UX** | ⚠️ | ⚠️ | ✅ | `--show-config-paths`, `--config-validate`, and explicit failure hints reduce startup ambiguity. |
| **Production-readiness target** | Community baseline | Enhanced fork | **Enterprise-grade** | `++` targets long-running agent workloads with stronger ops controls. |

---

## 🔍 Technical Differences & Hardening

### 1. Architectural Evolution: `pkg/llmproxy`
Unlike the mainline which keeps its core logic in `internal/` (preventing external Go projects from importing it), **cliproxyapi++** has refactored its entire translation and proxying engine into a clean, public `pkg/llmproxy` library.
*   **Reusability**: Import the proxy logic directly into your own Go applications.
*   **Decoupling**: Configuration management is strictly separated from execution logic.

### 2. Enterprise Authentication & Lifecycle
*   **Full GitHub Copilot Integration**: Not just an API wrapper. `++` includes a full OAuth device flow, per-credential quota tracking, and intelligent session management.
*   **Kiro (AWS CodeWhisperer) 2.0**: A custom-built web UI (`/v0/oauth/kiro`) for browser-based AWS Builder ID and Identity Center logins.
*   **Background Token Refresh**: A dedicated worker service monitors tokens and automatically refreshes them 10 minutes before expiration, ensuring zero downtime for your agents.

### 3. Security Hardening ("Defense in Depth")
*   **Path Guard**: A custom GitHub Action workflow (`pr-path-guard`) that prevents any unauthorized changes to critical `internal/translator/` logic during PRs.
*   **Device Fingerprinting**: Generates unique, immutable device identifiers to satisfy strict provider security checks and prevent account flagging.
*   **Hardened Docker Base**: Built on a specific, audited Alpine 3.22.0 layer with minimal packages, reducing the potential attack surface.

### 4. High-Scale Operations
*   **Intelligent Cooldown**: Automated "cooling" mechanism that detects provider-side rate limits and intelligently pauses requests to specific providers while routing others.
*   **Unified Model Converter**: A sophisticated mapping layer that allows you to request `claude-3-5-sonnet` and have the proxy automatically handle the specific protocol requirements of the target provider (Vertex, AWS, Anthropic, etc.).

---

## 🚀 Getting Started

### Prerequisites
- [Docker](https://docs.docker.com/get-docker/) and [Docker Compose](https://docs.docker.com/compose/install/)
- OR [Go 1.26+](https://golang.org/dl/)

### One-Command Deployment (Docker)

```bash
# Setup deployment
mkdir -p ~/cliproxy && cd ~/cliproxy
curl -o config.yaml https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.example.yaml

# Create compose file
cat > docker-compose.yml << 'EOF'
services:
  cliproxy:
    image: KooshaPari/cliproxyapi-plusplus:latest
    container_name: cliproxyapi++
    ports: ["8317:8317"]
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
      - ./logs:/CLIProxyAPI/logs
    restart: unless-stopped
EOF

docker compose up -d
```

---

## 🧭 Provider-First Quickstart

Users care most about provider behavior. This is the fastest safe production baseline:

1. Configure one direct primary provider (latency/reliability).
2. Configure one aggregator fallback provider (breadth/failover).
3. Enforce prefixes for workload isolation (`force-model-prefix: true`).
4. Verify `v1/models` and `v1/metrics/providers` before sending production traffic.

Minimal pattern:

```yaml
api-keys:
  - "prod-client-key"

force-model-prefix: true

claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "core"

openrouter:
  - api-key: "sk-or-v1-..."
    prefix: "fallback"
```

Validation:

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer prod-client-key" | jq '.data[:5]'

curl -sS http://localhost:8317/v1/metrics/providers | jq
```

---

## 🛠️ Provider and Routing Capabilities

`cliproxyapi++` supports a broad provider registry:

- **Direct**: Claude, Gemini, OpenAI/Codex, Mistral, Groq, DeepSeek.
- **Aggregators**: OpenRouter, Together AI, Fireworks AI, Novita AI, SiliconFlow.
- **OAuth / Session**: Kiro, GitHub Copilot, Roo Code, Kilo AI, MiniMax, Cursor.

### API Specification
The proxy provides two main API surfaces:
1.  **OpenAI Interface**: `/v1/chat/completions` and `/v1/models` (Full parity).
2.  **Management Interface**:
    *   `GET /v0/config`: Inspect current (hot-reloaded) config.
    *   `GET /v0/oauth/kiro`: Interactive Kiro auth UI.
    *   `GET /v0/logs`: Real-time log inspection.

---

## 🤝 Contributing

We maintain strict quality gates to preserve the "hardened" status of the project:
1.  **Linting**: Must pass `golangci-lint` with zero warnings.
2.  **Coverage**: All new translator logic MUST include unit tests.
3.  **Governance**: Changes to core `pkg/` logic require a corresponding Issue discussion.
4.  **Daily QOL flow**:
   - `task quality:fmt` to auto-format all Go files.
   - `task quality:quick` for a fast local loop (format + selected tests; set `QUALITY_PACKAGES` to scope).
   - `QUALITY_PACKAGES='./pkg/...' task quality:quick` for package-scoped smoke.
   - `task quality:fmt-staged` for staged file format + lint before commit.
   - `task quality:ci` for PR-scope non-mutating checks (fmt/vet/staticcheck/lint diff).
   - `task test:smoke` for startup + control-plane smoke.
   - `task verify:all` for a single-command local audit (fmt/lint/vet/quality + tests).
   - `task hooks:install` to register the pre-commit hook.

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for more details.

---

## 📚 Documentation

- **Provider Docs (new focus):**
  - [Provider Usage](./docs/provider-usage.md)
  - [Provider Catalog](./docs/provider-catalog.md)
  - [Provider Operations Runbook](./docs/provider-operations.md)
  - [Routing and Models Reference](./docs/routing-reference.md)
  - [Release Batching Guide](./docs/guides/release-batching.md)
- **Planning and Delivery Boards:**
  - [Planning Index](./docs/planning/index.md)
  - [2000-Item Execution Board](./docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.md)
  - [GitHub Project Import CSV](./docs/planning/GITHUB_PROJECT_IMPORT_CLIPROXYAPI_2000_2026-02-22.csv)
  - [Board Workflow (source -> solution mapping)](./docs/planning/board-workflow.md)
- **[Docsets](./docs/docsets/)** — Role-oriented documentation sets.
  - [Developer (Internal)](./docs/docsets/developer/internal/)
  - [Developer (External)](./docs/docsets/developer/external/)
  - [Technical User](./docs/docsets/user/)
  - [Agent Operator](./docs/docsets/agent/)
- **Research**: [AgentAPI + cliproxyapi++ tandem and alternatives](./docs/planning/agentapi-cliproxy-integration-research-2026-02-22.md)
- **Research (300 repo sweep)**: [coder org + 97 adjacent repos](./docs/planning/coder-org-plus-relative-300-inventory-2026-02-22.md)
- **[Feature Changes in ++](./docs/FEATURE_CHANGES_PLUSPLUS.md)** — Comprehensive list of `++` differences and impacts.
- **[Docs README](./docs/README.md)** — Core docs map.

---

## 🚢 Docs Deploy

Local VitePress docs:

```bash
cd docs
npm install
npm run docs:dev
npm run docs:build
```

GitHub Pages:

- Workflow: `.github/workflows/vitepress-pages.yml`
- URL convention: `https://<owner>.github.io/cliproxyapi-plusplus/`

---

## 📜 License

Distributed under the MIT License. See [LICENSE](LICENSE) for more information.

---

<p align="center">
  <b>Hardened AI Infrastructure for the Modern Agentic Stack.</b><br>
  Built with ❤️ by the community.
</p>
