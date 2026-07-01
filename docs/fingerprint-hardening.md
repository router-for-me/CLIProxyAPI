# Fingerprint Hardening (fork enhancements)

This fork adds anti-fingerprinting improvements on top of upstream
[`router-for-me/CLIProxyAPI`](https://github.com/router-for-me/CLIProxyAPI),
so that outbound traffic to the Anthropic and ChatGPT (Codex OAuth) backends is
harder to distinguish from a genuine Claude Code / Codex CLI client. It is a
**soft fork**: the Go module path is unchanged, every change is additive and
behind a config switch, so upstream can still be merged.

Findings were cross-referenced against the upstream project plus
[`james-6-23/codex2api`](https://github.com/james-6-23/codex2api) and
[`Wei-Shaw/sub2api`](https://github.com/Wei-Shaw/sub2api).

## What changed

| # | Area | Change | Toggle |
|---|------|--------|--------|
| 1 | Anthropic body | Normalize the **"dateline" steganographic fingerprint** — Claude Code encodes ~3 bits in the `Today's date is YYYY-MM-DD.` sentence (apostrophe glyph U+0027/2019/02BC/02B9 + `-`/`/` separator) when it detects a non-official base URL. Rewritten to canonical ASCII on OAuth accounts, **before** cch signing. Scoped to `system` + `<system-reminder>` blocks only. | `disable-dateline-normalization` |
| 2 | Anthropic TLS | Per-host uTLS profile. `api.anthropic.com` now uses a **Node.js 24 / Claude Code** ClientHello (JA3 `44f88fca…`, ALPN `http/1.1`) instead of generic Chrome, removing the "Node UA but Chrome JA3" cross-layer tell. `chatgpt.com` keeps Chrome/HTTP-2. | `disable-node-tls-fingerprint` |
| 3 | Anthropic HTTP | **HTTP/1.1 request header order** now matches undici's insertion order (`Accept → User-Agent → X-Stainless-* → anthropic-version → auth → anthropic-beta → content-type → per-request`) instead of Go's alphabetical sort, over a keep-alive pooled uTLS connection. | (same as #2) |
| 4 | uTLS transport | Shared TLS session cache for the built-in (Chrome) profile; the custom Node spec stays cache-less to keep its ClientHello byte-stable. | — |
| 5 | Codex UA | On OAuth (ChatGPT backend) paths — **both** WebSocket and HTTP `/responses` — a downstream `User-Agent` is forwarded only if it is a first-party Codex CLI (`codex-tui/`, `codex_cli_rs/`, `codex-exec/`); anything else is normalized to the canonical Codex UA. Config UA always wins; API-key/BYOK is unchanged. | `codex-header-defaults.user-agent` |
| 6 | Codex Originator | Same guard for the `Originator` header (OAuth forwards only Codex-family values, else normalizes). | — |

## Already provided by upstream (verified, not re-implemented)

- **Per-account device profile pinning** — `claude_device_profile.go` caches a per-account
  (`auth:<id>`) profile with a 7-day KV TTL. Enable strict platform pinning with
  `claude-header-defaults.stabilize-device-profile: true`.
- **Encrypted reasoning replay across turns** (Codex) — `internal/cache/codex_reasoning_replay_cache.go`.
- **Per-request upstream identity isolation** (Codex) — `CodexConfig.IdentityConfuse`.

## Config switches (all default to the hardened behavior)

```yaml
disable-dateline-normalization: false   # false = normalize (recommended)
disable-node-tls-fingerprint: false     # false = Node/h1 profile for Anthropic (recommended)
claude-header-defaults:
  stabilize-device-profile: true         # optional: pin per-account platform baseline
```

## Escape hatch

If the Node/HTTP-1.1 Anthropic profile ever causes handshake or transport problems in your
network, set `disable-node-tls-fingerprint: true` to revert `api.anthropic.com` to the previous
generic Chrome/HTTP-2 behavior.

## Known limitation

Exact byte-for-byte parity of undici's HTTP framing (e.g. the precise placement of
transport-injected headers like `Accept-Encoding`/`Connection`) is approximated from the Anthropic
SDK's construction order; a real Claude Code capture would let it be tuned further. The TLS/JA3
and header-order tells — the ones common detectors key on — are addressed.
