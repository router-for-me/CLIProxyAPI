# CLI Proxy API

---

## 🔔 Important: Amp CLI Support Fork

**This is a specialized fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that adds support for the Amp CLI tool.**

### Why This Fork Exists

The **Amp CLI** requires custom routing patterns to function properly. The upstream CLIProxyAPI project maintainers opted not to merge Amp-specific routing support into the main codebase.

### Which Version Should You Use?

- **Use this fork** if you want to run **both Factory CLI and Amp CLI** with the same proxy server
- **Use upstream** ([router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)) if you only need Factory CLI support

### 📖 Complete Setup Guide

**→ [USING_WITH_FACTORY_AND_AMP.md](USING_WITH_FACTORY_AND_AMP.md)** - Comprehensive guide for using this proxy with both Factory CLI (Droid) and Amp CLI and IDE extensions, including OAuth setup, configuration examples, and troubleshooting.

### Key Differences

This fork includes:
- ✅ **Amp CLI route aliases** (`/api/provider/{provider}/v1...`)
- ✅ **Amp upstream proxy support** for OAuth and management routes
- ✅ **Automatic gzip decompression** for Amp upstream responses
- ✅ **Smart secret management** with precedence: config > env > file
- ✅ **All Factory CLI features** from upstream (fully compatible)

All Amp-specific code is isolated in the `internal/api/modules/amp` module, making it easy to sync upstream changes with minimal conflicts.

---

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

### Fork-Specific: Amp CLI Support 🔥
- **Full Amp CLI integration** via provider route aliases (`/api/provider/{provider}/v1...`)
- **Amp upstream proxy** for OAuth authentication and management routes
- **Smart secret management** with configurable precedence (config > env > file)
- **Automatic gzip decompression** for Amp upstream responses
- **5-minute secret caching** to reduce file I/O overhead
- **Zero conflict** with Factory CLI - use both tools simultaneously
- **Modular architecture** for easy upstream sync (90% reduction in merge conflicts)

## Getting Started

CLIProxyAPI Guides: [https://help.router-for.me/](https://help.router-for.me/)

## Management API

see [MANAGEMENT_API.md](https://help.router-for.me/management/api)

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

> [!NOTE]
> If you developed a project based on CLIProxyAPI, please open a PR to add it to this list.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
