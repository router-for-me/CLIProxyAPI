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
| 3 | Anthropic HTTP | **HTTP/1.1 request header order** matches undici's exact insertion order — source-traced from `@anthropic-ai/sdk` (`buildHeaders`/`detect-platform`) and undici's fetch/h1 writer: `Connection` first (right after Host), then `Accept → User-Agent → X-Stainless-* → anthropic-version → auth → anthropic-beta → x-app → x-stainless-helper-method → content-type/length → accept-encoding` — instead of Go's alphabetical sort, over a keep-alive pooled uTLS connection. | (same as #2) |
| 4 | Anthropic Accept-Encoding | Non-stream requests advertise undici's exact default **`br, gzip, deflate, zstd`** (set + order) rather than a Go/ad-hoc value; zstd responses are still decoded. Streams use `identity`. | — |
| 5 | uTLS transport | Shared TLS session cache for the built-in (Chrome) profile; the custom Node spec stays cache-less to keep its ClientHello byte-stable. | — |
| 6 | Codex UA | On OAuth (ChatGPT backend) paths — **both** WebSocket and HTTP `/responses` — a downstream `User-Agent` is forwarded only if it is a first-party Codex CLI (`codex-tui/`, `codex_cli_rs/`, `codex-exec/`); anything else is normalized to the canonical Codex UA. Config UA always wins; API-key/BYOK is unchanged. | `codex-header-defaults.user-agent` |
| 7 | Codex Originator | Same guard for the `Originator` header (OAuth forwards only Codex-family values, else normalizes). | — |
| 8 | Codex cookies | **Per-account persistent cookie jar** stores & replays Cloudflare clearance cookies (`cf_clearance`, `__cf_bm`, `_cfuvid`, `__cflb`, `cf_chl_*`), mirroring the real Codex CLI's reqwest jar — per OpenAI's own source this is the primary anti-403 lever. Keyed by `auth.ID`; process-lifetime. | `disable-upstream-cookie-jar` |
| 9 | Anti-cluster | **Per-account device-fingerprint diversification.** When a client omits device headers, the Claude device profile (UA / `X-Stainless-Package-Version` / `Runtime-Version` / `Os` / `Arch`) and the Codex UA are filled with values drawn **deterministically per account** (`fnv(auth scope)`) from a realistic real-client distribution — the same account stays stable across requests/restarts, different accounts spread — so the fleet does not collapse onto the fixed defaults every stock CLIProxyAPI / codex2api / sub2api instance shares (which upstream could otherwise cluster and ban together). Client-supplied and config values always win; the TLS JA3/JA4 stays fixed to the real client. | `disable-fingerprint-randomization` |

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
disable-upstream-cookie-jar: false      # false = persist Cloudflare cookies per account (recommended)
disable-fingerprint-randomization: false # false = per-account device/UA diversification (recommended)
claude-header-defaults:
  stabilize-device-profile: true         # optional: pin per-account platform baseline
```

## Verification

`internal/runtime/executor/helps/utls_fpverify_test.go` is a gated, evidence-grade
self-check: with `FP_VERIFY=1` it drives each uTLS profile against a live JA3/JA4
reporter and asserts the **server-observed** fingerprint. The Node/h1 profile is
confirmed byte-exact against a real Claude Code capture — JA3 `44f88fca027f27bab4bb08d4af15f23e`,
JA4 `t13d1714h1_5b57614c22b0_7baf387fc6ff` — and chatgpt.com negotiates a valid
Chrome h2 fingerprint. Run it after any uTLS/profile change or `utls` upgrade:

```bash
FP_VERIFY=1 go test ./internal/runtime/executor/helps/ -run TestFingerprintAgainstReporter -v
```

## Escape hatch

If the Node/HTTP-1.1 Anthropic profile ever causes handshake or transport problems in your
network, set `disable-node-tls-fingerprint: true` to revert `api.anthropic.com` to the previous
generic Chrome/HTTP-2 behavior.

## Known limitation

The TLS layer is byte-verified (see Verification) and the HTTP header **order/value** tells are
source-calibrated against the Anthropic SDK + undici. Two finer points remain approximate and would
benefit from a real Claude Code capture: (a) exact header-name **casing** (undici lowercases some
names while the SDK sets others mixed-case; this writer canonicalizes), and (b) the precise wire
slot of transport-injected `Content-Length`. These are deeper than what common JA3/JA4 + header-order
detectors key on, which are addressed. For the ChatGPT/Codex path, faithful rustls impersonation is
deliberately **not** attempted — OpenAI's own rustls ClientHello is sometimes 403'd by Cloudflare, so
a clean Chrome profile plus the cookie jar (#8) is the safer, higher-fidelity choice.
