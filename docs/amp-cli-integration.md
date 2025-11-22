# Amp CLI Integration Guide

This guide explains how to use CLIProxyAPI with Amp CLI and Amp IDE extensions, enabling you to use your existing Google/ChatGPT/Claude subscriptions (via OAuth) with Amp's CLI.

## Table of Contents

- [Overview](#overview)
  - [Which Providers Should You Authenticate?](#which-providers-should-you-authenticate)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Setup](#setup)
- [Usage](#usage)
- [Troubleshooting](#troubleshooting)

## Overview

The Amp CLI integration adds specialized routing to support Amp's API patterns while maintaining full compatibility with all existing CLIProxyAPI features. This allows you to use both traditional CLIProxyAPI features and Amp CLI with the same proxy server.

### Key Features

- **Provider route aliases**: Maps Amp's `/api/provider/{provider}/v1...` patterns to CLIProxyAPI handlers
- **Management proxy**: Forwards OAuth and account management requests to Amp's control plane
- **Smart fallback**: Automatically routes unconfigured models to ampcode.com
- **Secret management**: Configurable precedence (config > env > file) with 5-minute caching
- **Security-first**: Management routes restricted to localhost by default
- **Automatic gzip handling**: Decompresses responses from Amp upstream

### What You Can Do

- Use Amp CLI with your Google account (Gemini 3 Pro Preview, Gemini 2.5 Pro, Gemini 2.5 Flash)
- Use Amp CLI with your ChatGPT Plus/Pro subscription (GPT-5, GPT-5 Codex models)
- Use Amp CLI with your Claude Pro/Max subscription (Claude Sonnet 4.5, Opus 4.1)
- Use Amp IDE extensions (VS Code, Cursor, Windsurf, etc.) with the same proxy
- Run multiple CLI tools (Factory + Amp) through one proxy server
- Route unconfigured models automatically through ampcode.com

### Which Providers Should You Authenticate?

**Important**: The providers you need to authenticate depend on which models and features your installed version of Amp currently uses. Amp employs different providers for various agent modes and specialized subagents:

- **Smart mode**: Uses Google/Gemini models (Gemini 3 Pro)
- **Rush mode**: Uses Anthropic/Claude models (Claude Haiku 4.5)
- **Oracle subagent**: Uses OpenAI/GPT models (GPT-5 medium reasoning)
- **Librarian subagent**: Uses Anthropic/Claude models (Claude Sonnet 4.5)
- **Search subagent**: Uses Anthropic/Claude models (Claude Haiku 4.5)
- **Review feature**: Uses Google/Gemini models (Gemini 2.5 Flash-Lite)

For the most current information about which models Amp uses, see the **[Amp Models Documentation](https://ampcode.com/models)**.

#### Fallback Behavior

CLIProxyAPI uses a smart fallback system:

1. **Provider authenticated locally** (`--login`, `--codex-login`, `--claude-login`):
   - Requests use **your OAuth subscription** (ChatGPT Plus/Pro, Claude Pro/Max, Google account)
   - You benefit from your subscription's included usage quotas
   - No Amp credits consumed

2. **Provider NOT authenticated locally**:
   - Requests automatically forward to **ampcode.com**
   - Uses Amp's backend provider connections
   - **Requires Amp credits** if the provider is paid (OpenAI, Anthropic paid tiers)
   - May result in errors if Amp credit balance is insufficient

**Recommendation**: Authenticate all providers you have subscriptions for to maximize value and minimize Amp credit usage. If you don't have subscriptions to all providers Amp uses, ensure you have sufficient Amp credits available for fallback requests.

## Architecture

### Request Flow

```
Amp CLI/IDE
  ↓
  ├─ Provider API requests (/api/provider/{provider}/v1/...)
  │   ↓
  │   ├─ Model configured locally?
  │   │   YES → Use local OAuth tokens (OpenAI/Claude/Gemini handlers)
  │   │   NO  → Forward to ampcode.com (reverse proxy)
  │   ↓
  │   Response
  │
  └─ Management requests (/api/auth, /api/user, /api/threads, ...)
      ↓
      ├─ Localhost check (security)
      ↓
      └─ Reverse proxy to ampcode.com
          ↓
          Response (auto-decompressed if gzipped)
```

### Components

The Amp integration is implemented as a modular routing module (`internal/api/modules/amp/`) with these components:

1. **Route Aliases** (`routes.go`): Maps Amp-style paths to standard handlers
2. **Reverse Proxy** (`proxy.go`): Forwards management requests to ampcode.com
3. **Fallback Handler** (`fallback_handlers.go`): Routes unconfigured models to ampcode.com
4. **Secret Management** (`secret.go`): Multi-source API key resolution with caching
5. **Main Module** (`amp.go`): Orchestrates registration and configuration

## Configuration

### Basic Configuration

Add these fields to your `config.yaml`:

```yaml
# Amp upstream control plane (required for management routes)
amp-upstream-url: "https://ampcode.com"

# Optional: Override API key (otherwise uses env or file)
# amp-upstream-api-key: "your-amp-api-key"

# Security: restrict management routes to localhost (recommended)
amp-restrict-management-to-localhost: true
```

### Secret Resolution Precedence

The Amp module resolves API keys using this precedence order:

| Source | Key | Priority | Cache |
|--------|-----|----------|-------|
| Config file | `amp-upstream-api-key` | High | No |
| Environment | `AMP_API_KEY` | Medium | No |
| Amp secrets file | `~/.local/share/amp/secrets.json` | Low | 5 min |

**Recommendation**: Use the Amp secrets file (lowest precedence) for normal usage. This file is automatically managed by `amp login`.

### Security Settings

**`amp-restrict-management-to-localhost`** (default: `true`)

When enabled, management routes (`/api/auth`, `/api/user`, `/api/threads`, etc.) only accept connections from localhost (127.0.0.1, ::1). This prevents:
- Drive-by browser attacks
- Remote access to management endpoints
- CORS-based attacks
- Header spoofing attacks (e.g., `X-Forwarded-For: 127.0.0.1`)

#### How It Works

This restriction uses the **actual TCP connection address** (`RemoteAddr`), not HTTP headers like `X-Forwarded-For`. This prevents header spoofing attacks but has important implications:

- ✅ **Works for direct connections**: Running CLIProxyAPI directly on your machine or server
- ⚠️ **May not work behind reverse proxies**: If deploying behind nginx, Cloudflare, or other proxies, the connection will appear to come from the proxy's IP, not localhost

#### Reverse Proxy Deployments

If you need to run CLIProxyAPI behind a reverse proxy (nginx, Caddy, Cloudflare Tunnel, etc.):

1. **Disable the localhost restriction**:
   ```yaml
   amp-restrict-management-to-localhost: false
   ```

2. **Use alternative security measures**:
   - Firewall rules restricting access to management routes
   - Proxy-level authentication (HTTP Basic Auth, OAuth)
   - Network-level isolation (VPN, Tailscale, Cloudflare Access)
   - Bind CLIProxyAPI to `127.0.0.1` only and access via SSH tunnel

3. **Example nginx configuration** (blocks external access to management routes):
   ```nginx
   location /api/auth { deny all; }
   location /api/user { deny all; }
   location /api/threads { deny all; }
   location /api/internal { deny all; }
   ```

**Important**: Only disable `amp-restrict-management-to-localhost` if you understand the security implications and have other protections in place.

## Setup

### 1. Configure CLIProxyAPI

Create or edit `config.yaml`:

```yaml
port: 8317
auth-dir: "~/.cli-proxy-api"

# Amp integration
amp-upstream-url: "https://ampcode.com"
amp-restrict-management-to-localhost: true

# Other standard settings...
debug: false
logging-to-file: true
```

### 2. Authenticate with Providers

Run OAuth login for the providers you want to use:

**Google Account (Gemini 2.5 Pro, Gemini 2.5 Flash, Gemini 3 Pro Preview):**
```bash
./cli-proxy-api --login
```

**ChatGPT Plus/Pro (GPT-5, GPT-5 Codex):**
```bash
./cli-proxy-api --codex-login
```

**Claude Pro/Max (Claude Sonnet 4.5, Opus 4.1):**
```bash
./cli-proxy-api --claude-login
```

Tokens are saved to:
- Gemini: `~/.cli-proxy-api/gemini-<email>.json`
- OpenAI Codex: `~/.cli-proxy-api/codex-<email>.json`
- Claude: `~/.cli-proxy-api/claude-<email>.json`

### 3. Start the Proxy

```bash
./cli-proxy-api --config config.yaml
```

Or run in background with tmux (recommended for remote servers):

```bash
tmux new-session -d -s proxy "./cli-proxy-api --config config.yaml"
```

### 4. Configure Amp CLI

#### Option A: Settings File

Edit `~/.config/amp/settings.json`:

```json
{
  "amp.url": "http://localhost:8317"
}
```

#### Option B: Environment Variable

```bash
export AMP_URL=http://localhost:8317
```

### 5. Login and Use Amp

Login through the proxy (proxied to ampcode.com):

```bash
amp login
```

Use Amp as normal:

```bash
amp "Write a hello world program in Python"
```

### 6. (Optional) Configure Amp IDE Extension

The proxy also works with Amp IDE extensions for VS Code, Cursor, Windsurf, etc.

1. Open Amp extension settings in your IDE
2. Set **Amp URL** to `http://localhost:8317`
3. Login with your Amp account
4. Start using Amp in your IDE

Both CLI and IDE can use the proxy simultaneously.

## Usage

### Supported Routes

#### Provider Aliases (Always Available)

These routes work even without `amp-upstream-url` configured:

- `/api/provider/openai/v1/chat/completions`
- `/api/provider/openai/v1/responses`
- `/api/provider/anthropic/v1/messages`
- `/api/provider/google/v1beta/models/:action`

Amp CLI calls these routes with your OAuth-authenticated models configured in CLIProxyAPI.

#### Management Routes (Require `amp-upstream-url`)

These routes are proxied to ampcode.com:

- `/api/auth` - Authentication
- `/api/user` - User profile
- `/api/meta` - Metadata
- `/api/threads` - Conversation threads
- `/api/telemetry` - Usage telemetry
- `/api/internal` - Internal APIs

**Security**: Restricted to localhost by default.

### Model Fallback Behavior

When Amp requests a model:

1. **Check local configuration**: Does CLIProxyAPI have OAuth tokens for this model's provider?
2. **If YES**: Route to local handler (use your OAuth subscription)
3. **If NO**: Forward to ampcode.com (use Amp's default routing)

This enables seamless mixed usage:
- Models you've configured (Gemini, ChatGPT, Claude) → Your OAuth subscriptions
- Models you haven't configured → Amp's default providers

### Example API Calls

**Chat completion with local OAuth:**
```bash
curl http://localhost:8317/api/provider/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**Management endpoint (localhost only):**
```bash
curl http://localhost:8317/api/user
```

## Troubleshooting

### Common Issues

| Symptom | Likely Cause | Fix |
|---------|--------------|-----|
| 404 on `/api/provider/...` | Incorrect route path | Ensure exact path: `/api/provider/{provider}/v1...` |
| 403 on `/api/user` | Non-localhost request | Run from same machine or disable `amp-restrict-management-to-localhost` (not recommended) |
| 401/403 from provider | Missing/expired OAuth | Re-run `--codex-login` or `--claude-login` |
| Amp gzip errors | Response decompression issue | Update to latest build; auto-decompression should handle this |
| Models not using proxy | Wrong Amp URL | Verify `amp.url` setting or `AMP_URL` environment variable |
| CORS errors | Protected management endpoint | Use CLI/terminal, not browser |

### Diagnostics

**Check proxy logs:**
```bash
# If logging-to-file: true
tail -f logs/requests.log

# If running in tmux
tmux attach-session -t proxy
```

**Enable debug mode** (temporarily):
```yaml
debug: true
```

**Test basic connectivity:**
```bash
# Check if proxy is running
curl http://localhost:8317/v1/models

# Check Amp-specific route
curl http://localhost:8317/api/provider/openai/v1/models
```

**Verify Amp configuration:**
```bash
# Check if Amp is using proxy
amp config get amp.url

# Or check environment
echo $AMP_URL
```

### Security Checklist

- ✅ Keep `amp-restrict-management-to-localhost: true` (default)
- ✅ Don't expose proxy publicly (bind to localhost or use firewall/VPN)
- ✅ Use the Amp secrets file (`~/.local/share/amp/secrets.json`) managed by `amp login`
- ✅ Rotate OAuth tokens periodically by re-running login commands
- ✅ Store config and auth-dir on encrypted disk if handling sensitive data
- ✅ Keep proxy binary up to date for security fixes

## Additional Resources

- [CLIProxyAPI Main Documentation](https://help.router-for.me/)
- [Amp CLI Official Manual](https://ampcode.com/manual)
- [Management API Reference](https://help.router-for.me/management/api)
- [SDK Documentation](sdk-usage.md)

## Disclaimer

This integration is for personal/educational use. Using reverse proxies or alternate API bases may violate provider Terms of Service. You are solely responsible for how you use this software. Accounts may be rate-limited, locked, or banned. No warranties. Use at your own risk.
