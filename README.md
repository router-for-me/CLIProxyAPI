# CLI Proxy API

English | [中文](README_CN.md)

A proxy server that provides OpenAI/Gemini/Claude/Codex compatible API interfaces for CLI.

It now also supports OpenAI Codex (GPT models) and Claude Code via OAuth.

So you can use local or multi-account CLI access with OpenAI(include Responses)/Gemini/Claude-compatible clients and SDKs.

## Sponsor

[![z.ai](https://assets.router-for.me/english.png)](https://z.ai/subscribe?ic=8JVLJQFSKB)

This project is sponsored by Z.ai, supporting us with their GLM CODING PLAN.

GLM CODING PLAN is a subscription service designed for AI coding, starting at just $3/month. It provides access to their flagship GLM-4.6 model across 10+ popular AI coding tools (Claude Code, Cline, Roo Code, etc.), offering developers top-tier, fast, and stable coding experiences.

Get 10% OFF GLM CODING PLAN：https://z.ai/subscribe?ic=8JVLJQFSKB

## Overview

- OpenAI/Gemini/Claude compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Claude Code support via OAuth login
- Qwen Code support via OAuth login
- iFlow support via OAuth login
- Amp CLI and IDE extensions support with provider routing
- Streaming and non-streaming responses
- Function calling/tools support
- Multimodal input support (text and images)
- Multiple accounts with round-robin load balancing (Gemini, OpenAI, Claude, Qwen and iFlow)
- Simple CLI authentication flows (Gemini, OpenAI, Claude, Qwen and iFlow)
- Generative Language API Key support
- AI Studio Build multi-account load balancing
- Gemini CLI multi-account load balancing
- Claude Code multi-account load balancing
- Qwen Code multi-account load balancing
- iFlow multi-account load balancing
- OpenAI Codex multi-account load balancing
- OpenAI-compatible upstream providers via config (e.g., OpenRouter)
- Reusable Go SDK for embedding the proxy (see `docs/sdk-usage.md`)

## Operational Enhancements

This fork includes additional "proxy ops" features beyond the mainline release to improve third-party provider integrations:

### Core Features
- Environment-based secret loading via `os.environ/NAME`
- Strict YAML parsing via `strict-config` / `CLIPROXY_STRICT_CONFIG`
- Optional encryption-at-rest for `auth-dir` credentials + atomic/locked writes
- Prometheus metrics endpoint (configurable `/metrics`) + optional auth gate (`metrics.require-auth`)
- In-memory response cache (LRU+TTL) for non-streaming JSON endpoints
- Rate limiting (global / per-key parallelism + per-key RPM + per-key TPM)
- Request/response size limits (`limits.max-*-size-mb`)
- Request body guardrail (reject `api_base` / `base_url` by default)
- Virtual keys (managed client keys) + budgets + pricing-based spend tracking
- Fallback chains (`fallback-chains`) + exponential backoff retries (`retry-policy`)
- Pass-through endpoints (`pass-through.endpoints[]`) for forwarding extra routes upstream
- Health endpoints (`/health/liveness`, `/health/readiness`) + optional background probes
- Sensitive-data masking (request logs + redacted management config view)

### Health-Based Routing & Smart Load Balancing

CLIProxyAPIPlus now includes intelligent routing and health tracking based on production-grade proxy patterns:

#### Features

**Health Tracking System**
- Automatic monitoring of credential health based on failure rates and response latency
- Four health status levels: HEALTHY, DEGRADED, COOLDOWN, ERROR
- Rolling window metrics (configurable 60-second default)
- Per-credential and per-model statistics tracking
- P95/P99 latency percentile calculations
- Automatic cooldown integration

**Advanced Routing Strategies**
- **`fill-first`**: Drain one credential to rate limit/cooldown before moving to the next to stagger rolling windows
- **`round-robin`**: Sequential credential rotation (default)
- **`random`**: Random credential selection
- **`least-busy`**: Select credential with fewest active requests (load balancing)
- **`lowest-latency`**: Select credential with best P95 latency (performance optimization)

**Health-Aware Routing**
- Automatically filter out COOLDOWN and ERROR credentials
- Prefer HEALTHY credentials over DEGRADED when `prefer-healthy: true`
- Graceful fallback to all credentials when no healthy ones available

#### Configuration Example

```yaml
# Health tracking configuration
health-tracking:
  enable: true
  window-seconds: 60              # Rolling window for failure rate calculation
  failure-threshold: 0.5          # 50% failure rate triggers ERROR status
  degraded-threshold: 0.1         # 10% failure rate triggers DEGRADED status
  min-requests: 5                 # Minimum requests before tracking
  cleanup-interval: 300           # Cleanup old data every 5 minutes

# Enhanced routing configuration
routing:
  strategy: "least-busy"          # fill-first, round-robin, random, least-busy, lowest-latency
  health-aware: true              # Filter unhealthy credentials (COOLDOWN, ERROR)
  prefer-healthy: true            # Prioritize HEALTHY over DEGRADED credentials
```

#### Routing Strategy Comparison

| Strategy | Best For | How It Works |
|----------|----------|--------------|
| `fill-first` | Staggering rolling caps | Uses the first available credential (by ID) until it hits rate limit/cooldown, then moves to the next |
| `round-robin` | Even distribution, predictable | Cycles through credentials sequentially |
| `random` | Simple load balancing | Randomly selects from available credentials |
| `least-busy` | Optimal load distribution | Selects credential with fewest active requests |
| `lowest-latency` | Performance-critical apps | Selects credential with best P95 latency |

#### Health Status Levels

- **HEALTHY**: Normal operation, low failure rates
- **DEGRADED**: Elevated failure rates (above degraded-threshold but below failure-threshold)
- **COOLDOWN**: Temporarily unavailable due to errors or rate limits
- **ERROR**: High failure rates (above failure-threshold) or persistent errors

#### Benefits

- **Improved reliability** by avoiding unhealthy credentials when `health-aware` routing is enabled
- **Better tail latency** when `lowest-latency` is enabled and health tracking has enough data
- **Smarter load balancing** with `least-busy` using in-flight request counts
- **Automatic recovery** from cooldown windows as health improves

See:
- `docs/operations.md`

### Future work

These are high-value ideas that remain on the roadmap:
- OpenTelemetry tracing + external integrations (Langfuse/Sentry/webhooks)
- Redis-backed distributed cache/rate limits for multi-instance deployments
- DB-backed virtual key store + async spend log writer
- Broader endpoint coverage via native translators (beyond pass-through)

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

**→ [Complete Amp CLI Integration Guide](https://help.router-for.me/agent-client/amp-cli.html)**

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

### [ProxyPal](https://github.com/heyhuynhgiabuu/proxypal)

Native macOS GUI for managing CLIProxyAPI: configure providers, model mappings, and endpoints via OAuth - no API keys needed.

> [!NOTE]  
> If you developed a project based on CLIProxyAPI, please open a PR to add it to this list.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
