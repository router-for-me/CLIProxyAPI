# CLIProxyAPI-Extended

> Fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) with unified Canonical IR translation architecture and new providers (Kiro, Cline, Ollama)

[![Original Repo](https://img.shields.io/badge/Original-router--for--me%2FCLIProxyAPI-blue)](https://github.com/router-for-me/CLIProxyAPI)

## 📋 Contributing Notice

**This is an experimental fork.** I'm sharing this work for the community to use and build upon.

- **Cherry-pick what you need** — feel free to take individual features or fixes that are useful for your projects
- **Limited maintenance** — I have limited time to review extensive change requests
- **Tested but experimental** — the code works in my testing environment, but your mileage may vary
- **Clear solutions only** — if you report an issue, please provide a specific fix or clear reproduction steps; I don't have time to investigate vague problem descriptions

Contributions are welcome! Simple bug fixes with ready-to-merge code will likely be accepted. For larger changes or feature requests, consider forking — this gives you full control over the direction of your modifications.

---

## What's Added in This Fork

| Component | Description |
|-----------|-------------|
| **Canonical IR Translator** | Hub-and-spoke architecture for format translation |
| **Ollama API** | Full implementation of Ollama-compatible server |
| **Kiro (Amazon Q)** | New provider with access to Claude via Amazon Q |
| **Cline** | Provider with free models (MiniMax M2, Grok) |
| **Model Registry** | Support for provider:modelID keys, visual prefixes |
| **ThinkingSupport** | Metadata for reasoning-capable models |

---

> **62% codebase reduction** — from 13,930 to 5,302 lines  
> **86% Google providers unification** — from 5,651 to 780 lines  
> **New providers:** Ollama, Kiro (Amazon Q), Cline (free models)

## Problem

Legacy translator used **N×M architecture** — each source→target pair required a separate directory with files:

```
internal/translator/
├── openai/          → claude/, gemini/, gemini-cli/, openai/
├── claude/          → gemini/, gemini-cli/, openai/
├── codex/           → claude/, gemini/, gemini-cli/, openai/
├── gemini/          → claude/, gemini/, gemini-cli/, openai/
├── gemini-cli/      → claude/, gemini/, openai/
└── antigravity/     → claude/, gemini/, openai/
```

**6 sources × 4-5 targets = 27 translation paths, 84 files, massive code duplication.**

## Solution

**Hub-and-spoke architecture** with unified Intermediate Representation (IR):

```
    OpenAI ─────┐                       ┌───── OpenAI
    Claude ─────┤                       ├───── Claude
    Ollama ─────┼─────► Canonical ◄─────┼───── Gemini (AI Studio)
      Kiro ─────┤       IR              ├───── Gemini CLI
     Cline ─────┘                       ├───── Antigravity
                                        ├───── Ollama
                                        └───── Cline
```

**Result:** 15 files (5 parsers + 5 emitters + 5 IR core), minimal duplication.

## Metrics

| Metric                    | Legacy        | Canonical IR  | Δ         |
|---------------------------|---------------|---------------|-----------|
| Files                     | 84            | 15            | **−82%**  |
| Lines of code             | 13,930        | 5,302         | **−62%**  |
| Translation paths         | 27            | 10            | **−63%**  |
| Google providers (lines)  | 5,651         | 780           | **−86%**  |

### Google Providers Breakdown

| Provider     | Legacy  | Canonical | Note                            |
|--------------|--------:|----------:|---------------------------------|
| Gemini       | 2,547   | 780       | Unified into 2 files:           |
| Gemini CLI   | 1,520   | (shared)  | `to_ir/gemini.go` (220 lines)   |
| Antigravity  | 1,584   | (shared)  | `from_ir/gemini.go` (560 lines) |
| **Total**    | **5,651** | **780** | **−86%**                        |

## Provider Support

| Provider      | Parsing (to_ir)      | Generation (from_ir) |
|---------------|:--------------------:|:--------------------:|
| OpenAI        | ✅ Req/Resp/Stream   | ✅ Req/Resp/Stream   |
| Claude        | ✅ Req/Resp/Stream   | ✅ Req/Resp/SSE      |
| Gemini        | ✅ Resp/Stream       | ✅ Req/Resp/Stream   |
| Gemini CLI    | ✅ (shared w/ Gemini)| ✅ GeminiCLIProvider |
| Antigravity   | ✅ (shared w/ Gemini)| ✅ (via GeminiCLI)   |
| Ollama        | ✅ Req/Resp/Stream   | ✅ Req/Resp/Stream   |
| Kiro          | ✅ Resp/Stream       | ✅ Req               |
| Cline         | ✅ (via OpenAI)      | ✅ (via OpenAI)      |

**Cline** — provider with free models (MiniMax M2, Grok Code Fast 1), uses OpenAI-compatible format.

**Kiro (Amazon Q)** — new provider with access to Claude models via Amazon Q:
- Claude Sonnet 4.5, Claude 4 Opus, Claude 3.7 Sonnet, Claude 3.5 Sonnet/Haiku
- Uses binary AWS Event Stream protocol

### Ollama as Output Format

The proxy acts as an **Ollama-compatible server** with full API implementation. Incoming Ollama requests are parsed directly into IR format (no intermediate OpenAI conversion on input). The request is then converted to the target provider's format for execution, and the response is converted back through IR to Ollama format.

**Server is recommended to run on standard port 11434** to avoid client compatibility issues.

```
Ollama client (/api/chat)
    ↓ parse directly to IR
Canonical IR
    ↓ convert to provider format
Provider (Gemini/Claude/OpenAI/etc.)
    ↓ response
Canonical IR
    ↓ convert to Ollama format
Ollama response
```

**Use case:** IDEs with Ollama support but without OpenAI-compatible API (e.g., Copilot Chat).

## Structure

```
translator_new/
├── ir/           # Core (5 files, 1,239 lines)
│   ├── types.go            # UnifiedChatRequest, UnifiedEvent, Message
│   ├── util.go             # ID generation, finish reason mapping
│   ├── message_builder.go  # Message parsing
│   ├── response_builder.go # Response building
│   └── claude_builder.go   # Claude SSE utilities
│
├── to_ir/        # Parsers (5 files, 1,530 lines)
│   ├── openai.go   # Chat Completions + Responses API (+ Cline)
│   ├── claude.go   # Messages API
│   ├── gemini.go   # AI Studio + CLI + Antigravity
│   ├── ollama.go   # /api/chat + /api/generate
│   └── kiro.go     # Amazon Q
│
└── from_ir/      # Emitters (5 files, 2,533 lines)
    ├── openai.go   # Chat Completions + Responses API (+ Cline)
    ├── claude.go   # Messages API + SSE streaming
    ├── gemini.go   # GeminiProvider + GeminiCLIProvider
    ├── ollama.go   # /api/chat + /api/generate
    └── kiro.go     # KiroProvider
```

## Key Features

- **Reasoning/Thinking** — unified handling of thinking blocks with `reasoning_tokens` tracking
- **Tool Calls** — unified ID generation and argument parsing
- **Multimodal** — images, PDF, inline data
- **Streaming** — SSE (OpenAI/Claude) and NDJSON (Gemini/Ollama)
- **Responses API** — full support for `/v1/responses`
- **ThinkingSupport** — model metadata for reasoning-capable models


## Limitations and Status

### Testing
- ✅ **Tested:** Cursor, Copilot Chat and similar UI clients
- ⚠️ **Not tested:** CLI agents (Codex CLI, Aider, etc.)
- ⚠️ **Claude (Anthropic):** implemented without API access, requires testing

### Antigravity Provider — UI Client Testing
| Model | Status | Note |
|-------|:------:|------|
| Claude Sonnet 4.5 | ✅ | Fully tested in Cursor/Copilot Chat |
| Gemini models | ✅ | Fully tested in Cursor/Copilot Chat |
| GPT-OSS | ⚠️ | **Thinking disabled** — model gets stuck in planning loops |

> **TODO:** Fix GPT-OSS thinking mode. The model enters infinite planning loops when thinking is enabled, repeatedly generating the same plan without executing actions. Temporarily disabled via `delete(genConfig, "thinkingConfig")` in `antigravity_executor.go`.

### Executors with Canonical IR Support
| Executor           | Status | Note |
|--------------------|:------:|------|
| gemini             | ✅     | AI Studio, tested |
| gemini_vertex      | ✅     | Vertex AI, tested |
| gemini_cli         | ✅     | Google, tested |
| antigravity        | ✅     | Google, tested (Claude Sonnet, Gemini) |
| aistudio           | ✅     | AI Studio, tested |
| openai_compat      | ✅     | OpenAI-compatible, tested |
| cline              | ✅     | Free models, tested |
| kiro               | ✅     | Amazon Q (new translator only) |
| claude             | ⚠️     | Anthropic — not tested |
| **codex**          | ❌     | Requires migration |
| **qwen**           | ❌     | Requires migration |
| **iflow**          | ❌     | Requires migration |

## Authentication for New Providers

> **Note:** Unlike Gemini/Claude (full OAuth2 flow with auto browser opening), Cline and Kiro use a **semi-manual method** — tokens are extracted from IDE manually.

### Cline
- Uses long-lived refresh token for authentication
- Refresh token is automatically exchanged for short-lived JWT access token (~10 minutes)
- JWT token is used with `workos:` prefix for API requests
- **Important:** Obtaining the refresh token requires modification of the Cline extension source code

### Kiro (Amazon Q)
- Tokens are automatically loaded from JSON file in auth directory (watcher) if you're logged into Kiro IDE, or can be configured manually
- Supports two authentication methods:
  - **Social auth** (Google, etc.) — via `prod.*.auth.desktop.kiro.dev`
  - **IAM/SSO auth** — via AWS OIDC endpoint
- Tokens are automatically refreshed via the corresponding endpoint

## Compatibility and Migration

**All changes in this fork do not affect the main system operation** — new functionality is activated via feature flags:

| Flag | Description | Default |
|------|-------------|---------|
| `use-canonical-translator` | Enables new IR translation architecture | `false` |
| `show-provider-prefixes` | Visual provider prefixes in model list | `false` |

With `use-canonical-translator: false` the system runs on legacy translator without changes.  
New providers (Kiro, Cline, Ollama API) only work with the flag enabled.

**About provider prefixes:** The `show-provider-prefixes` flag adds visual prefixes (e.g., `[Gemini CLI] gemini-2.5-flash`) to distinguish identical models from different providers. Prefixes are purely cosmetic — models can be called with or without the prefix.

**Provider selection:** When calling a model without a prefix (or with prefixes disabled), the system uses **round-robin** — providers are selected in turn among available ones. This provides load balancing between multiple accounts/providers with the same model.

---

## Original CLIProxyAPI Features

> For complete documentation of the original project, see [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)

- OpenAI/Gemini/Claude compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Claude Code support via OAuth login
- Qwen Code support via OAuth login
- iFlow support via OAuth login
- Streaming and non-streaming responses
- Function calling/tools support
- Multimodal input support (text and images)
- Multiple accounts with round-robin load balancing
- Simple CLI authentication flows
- Reusable Go SDK for embedding the proxy

## Getting Started

CLIProxyAPI Guides: [https://help.router-for.me/](https://help.router-for.me/)

## Management API

see [MANAGEMENT_API.md](https://help.router-for.me/management/api)

## Amp CLI Support

CLIProxyAPI includes integrated support for [Amp CLI](https://ampcode.com) and Amp IDE extensions, enabling you to use your Google/ChatGPT/Claude OAuth subscriptions with Amp's coding tools:

- Provider route aliases for Amp's API patterns (`/api/provider/{provider}/v1...`)
- Management proxy for OAuth authentication and account features
- Smart model fallback with automatic routing
- **Model mapping** to route unavailable models to alternatives (e.g., `claude-opus-4.5` → `claude-sonnet-4`)
- Security-first design with localhost-only management endpoints

**→ [Complete Amp CLI Integration Guide](docs/amp-cli-integration.md)**

## SDK Docs

- Usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- Advanced (executors & translators): [docs/sdk-advanced.md](docs/sdk-advanced.md)
- Access: [docs/sdk-access.md](docs/sdk-access.md)
- Watcher: [docs/sdk-watcher.md](docs/sdk-watcher.md)
- Custom Provider Example: `examples/custom-provider`

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Who is with us?

Those projects are based on CLIProxyAPI:

### [vibeproxy](https://github.com/automazeio/vibeproxy)

Native macOS menu bar app to use your Claude Code & ChatGPT subscriptions with AI coding tools - no API keys needed

### [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator)

Browser-based tool to translate SRT subtitles using your Gemini subscription via CLIProxyAPI with automatic validation/error correction - no API keys needed

### [CCS (Claude Code Switch)](https://github.com/kaitranntt/ccs)

CLI wrapper for instant switching between multiple Claude accounts and alternative models (Gemini, Codex, Antigravity) via CLIProxyAPI OAuth - no API keys needed

> [!NOTE]  
> If you developed a project based on CLIProxyAPI, please open a PR to add it to this list.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

**Original project:** [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
