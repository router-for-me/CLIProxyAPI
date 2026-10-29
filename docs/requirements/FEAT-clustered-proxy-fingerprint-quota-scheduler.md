# FEAT-clustered-proxy-fingerprint-quota-scheduler

# Clustered Proxy, Anti-Detect Fingerprint Browser OAuth, and Quota Reset Priority Scheduling Specification

## 1. Executive Summary

### 1.1 Context & Motivation
In high-throughput multi-account proxy deployments, upstream providers (e.g., Cloudflare, Anthropic, OpenAI) enforce stringent risk-control and rate-limiting rules. The current architecture faces three core challenges:
1. **Account Correlation and Suspension Risk**: Multiple accounts share a single egress IP address. Interactive OAuth browser logins spawn the host system's default browser, exposing shared cookies, local storage, Canvas, WebGL, and OS characteristics across accounts, triggering anti-bot correlation heuristics.
2. **Homogeneous TLS Fingerprinting**: HTTP requests lack account-level TLS ClientHello diversification (JA3/JA4 signatures), preventing granular emulation of distinct client stacks per account.
3. **Inefficient Rolling Quota Utilization**: Model quotas operate within fixed or rolling time windows (e.g., 5-hour rolling windows for Claude, 3-hour limits for ChatGPT Plus). Standard round-robin or fill-first selectors are unaware of quota reset deadlines. If an account has unused capacity expiring within minutes, failing to prioritize it wastes that quota upon reset.

### 1.2 Core Objectives
This feature introduces three architectural pillars:
- **Clustered & Per-Account Egress Proxying**: Direct binding of individual accounts to dedicated egress proxies, along with proxy pool clustering, health checking, and sticky IP persistence across all request types (inference, WebSocket, token refresh, and OAuth exchange).
- **KunKun Fingerprint Browser Bridge & TLS Anti-Detection**: Deep integration with the local KunKun fingerprint browser framework (`/Users/crymang/Works/KunKun`), enforcing isolated sandbox profiles (Canvas, WebGL, Audio, Viewport, UA) during OAuth, paired with uTLS client emulation for API traffic.
- **Quota Reset Priority Scheduling**: A dedicated scheduling strategy (`ResetPrioritySelector`) that prioritizes accounts whose quotas are closest to resetting ($\min \Delta T$), exhausting expiring quotas before the new window opens.

---

## 2. System Architecture

```
                            [ Client HTTP / WebSocket Requests ]
                                             │
                                             ▼
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                             CLIProxyAPI Core Conductor                                 │
│                                                                                        │
│   ┌────────────────────────────────────────────────────────────────────────────────┐   │
│   │             Scheduling Engine: ResetPrioritySelector                           │   │
│   │                                                                                │   │
│   │   Candidates ──► Filter Cooldowns ──► Evaluate Time to Reset (ΔT)              │   │
│   │                                                │                               │   │
│   │   ┌────────────────────────────────────────────┴───────────────────────────┐   │   │
│   │   │ Tier 1: Urgent Reset (0 < ΔT ≤ 15 min) ──► Ascending ΔT (Consume First) │   │   │
│   │   │ Tier 2: Normal Availability (ΔT > 15 min) ──► Weighted Round-Robin     │   │   │
│   │   └────────────────────────────────────────────────────────────────────────┘   │   │
│   └────────────────────────────────────────────────────────────────────────────────┘   │
│                                            │ (Selected Auth Record)                    │
│                                            ▼                                           │
│   ┌────────────────────────────────────────────────────────────────────────────────┐   │
│   │                    Account Metadata & Isolation Layer                          │   │
│   │   - Account ID: id, provider, label                                            │   │
│   │   - Egress Proxy: proxy_url / proxy_pool_id (Dedicated Proxy Binding)          │   │
│   │   - TLS Fingerprint: HelloChrome_120 / HelloFirefox_120 / HelloSafari          │   │
│   │   - Browser Profile: kunkun_profile_id (Hardware device configuration)         │   │
│   │   - Quota State: QuotaState (next_reset_at, window_duration, signals)          │   │
│   └────────────────────────────────────────────────────────────────────────────────┘   │
└────────────────────────────────────────────┬───────────────────────────────────────────┘
                                             │
                     ┌───────────────────────┴───────────────────────┐
                     ▼                                               ▼
┌──────────────────────────────────────────┐   ┌──────────────────────────────────────────┐
│   Runtime Upstream Egress                │   │   Interactive OAuth (KunKun Browser)     │
│                                          │   │                                          │
│  - Dedicated Egress Proxy (SOCKS5/HTTP)  │   │  - Launch KunKun instance (CLI / API)    │
│  - uTLS ClientHello randomized profiles  │   │  - Inject account-dedicated egress proxy │
│  - Isolated WebSocket & SSE connections  │   │  - Inject DeviceData hardware spoofing   │
│  - Capture passive quota reset signals   │   │  - Isolated sandbox profiles/{account_id}│
│    (anthropic-ratelimit-*, x-codex-*)    │   │  - Redirect loopback to CLIProxyAPI port │
└──────────────────────────────────────────┘   └──────────────────────────────────────────┘
```

---

## 3. Functional Specifications

### 3.1 Module 1: Clustered & Per-Account Proxying

#### Requirements
1. **Static Account Binding**:
   - Each auth credential file (`auths/auth_*.json`) supports explicit `proxy_url` configuration (`http://`, `https://`, `socks5://`, `socks5h://` with authentication).
2. **Proxy Pool Management**:
   - `config.yaml` declares named proxy pools with automated health probes (e.g., periodic HEAD/GET to provider endpoints).
   - Accounts can specify `proxy_pool_id`. Unassigned accounts acquire proxy nodes according to:
     - `sticky`: Account binds to a chosen node on first use, maintaining IP consistency permanently across requests and refreshes.
     - `round-robin` / `least-connections`: Dynamic distribution across available healthy nodes.
3. **Full-Lifecycle Network Isolation**:
   - **Inference Requests**: Regular POST/GET, SSE streaming, and image generation routes egress through the assigned proxy.
   - **WebSocket Streams**: Handshake and frame delivery via dedicated proxy dialer.
   - **Token Refresh**: Background OAuth refresh token exchanges route through the assigned proxy.
   - **OAuth Code Exchange**: Code-to-token exchanges must route through the assigned proxy.

---

### 3.2 Module 2: KunKun Fingerprint Browser Bridge & TLS Diversification

#### Requirements
1. **KunKun Project Integration**:
   - Leverages the local KunKun fingerprint framework (`/Users/crymang/Works/KunKun`).
   - KunKun provides PySide6 + Playwright emulation with 50+ fingerprint characteristics (Canvas noise, WebGL unmasked vendor/renderer, AudioContext phase shifts, physical screen parameters, intact original User-Agents, and WebRTC leak blocking).
2. **Bridge Adapter Execution Modes**:
   - **Mode A: CLI Driver (Default)**:
     CLIProxyAPI executes KunKun via a subprocess command:
     ```bash
     uv run python main.py --oauth-mode \
       --url "<OAuth-Authorize-URL>" \
       --proxy "<Account-Proxy-URL>" \
       --profile-id "<Account-ID>" \
       --device-mode "browser" \
       --user-data-dir "./auths/kunkun_profiles/<Account-ID>"
     ```
     > Erratum: the implemented contract uses `--oauth-launch` (not `--oauth-mode`) plus `--callback-prefix <redirect_uri>`, and passes an absolute `--user-data-dir`. Mode B is not implemented; `driver-mode: "service"` returns an explicit error.
   - **Mode B: HTTP Service Driver**:
     KunKun runs a local daemon (such as `probe_service`), exposing a REST endpoint (`POST /api/v1/browser/launch-session`) for programmatic browser launching.
3. **Sandbox Storage Isolation**:
   - Each account maintains an isolated `user-data-dir`.
   - Cookies, LocalStorage, SessionStorage, and IndexedDB data are physically partitioned.
4. **TLS Fingerprint Diversification (Go Layer)**:
   - Built on `github.com/refraction-networking/utls`.
   - Accounts configure or randomly draw a TLS fingerprint preset (`HelloChrome_120`, `HelloFirefox_120`, `HelloSafari_16_0`), aligning JA3/JA4 signatures with the emulated client platform.
   > Erratum: the implemented values are `chrome_120` / `chrome_auto` / `firefox_120` / `safari_16` / `ios_16` / `random_chrome` / `chrome_131` / `chrome_133`, stored as the top-level auth JSON `tls_preset` and read from `Auth.Attributes["tls_preset"]`. Empty means `HelloChrome_Auto` (Chrome 133 in uTLS v1.8.2); `ios_16` approximates iOS 14; Claude/Anthropic traffic keeps `HelloCustom` and ignores the preset.

---

### 3.3 Module 3: Quota Reset Priority Scheduler (`ResetPrioritySelector`)

#### Algorithm Design
When multiple accounts provide the same model (e.g., `claude-3-7-sonnet` or `gpt-4o`):

1. **Quota Reset Signal Extraction**:
   - **Anthropic / Claude**:
     - Parse `anthropic-ratelimit-unified-reset` and `anthropic-ratelimit-reset`.
     > Erratum: the parser understands `anthropic-ratelimit-reset`, but passive collection only stores `anthropic-ratelimit-unified-*`, `retry-after`, and the Codex headers in `QuotaState.Signals`. The parser accepts Unix seconds only (not RFC3339) and does not consume `x-ratelimit-reset-*`.
   - **OpenAI / Codex**:
     - Parse `x-codex-primary-reset-at`, `x-codex-primary-reset-after-seconds`, and `x-ratelimit-reset-*`.
   - **Active Window Fallback**:
     - For accounts lacking explicit headers, calculate theoretical reset time from the anchor timestamp and configured window duration (e.g., 5-hour rolling window).
2. **Selection Flow**:
   - **Step 1: Filter Unusable Credentials**: Exclude disabled accounts and those in active cooldown ($T_{\text{recover}} > T_{\text{now}}$).
   - **Step 2: Calculate Delta ($\Delta T$)**: Compute $\Delta T_i = T_{\text{reset}}(a_i) - T_{\text{now}}$ for each eligible candidate $a_i$.
   - **Step 3: Tiered Prioritization**:
     - **Urgent Tier ($0 < \Delta T \le T_{\text{urgent}}$, default 15 minutes)**:
       - Accounts in this window receive absolute priority.
       - Sorted ascending by $\Delta T$ (imminent reset first).
       - Jitter Grouping: If accounts reset within a narrow window (e.g., within 60s), micro-round-robin is applied to prevent instantaneous 429 overloads.
     - **Normal Tier ($\Delta T > T_{\text{urgent}}$ or unrecorded reset)**:
       - Evaluated under configured fallback (Weighted Round-Robin or Fill-First).
   - **Step 4: Rapid 429 Failover**:
     - If an urgent account hits an upstream concurrency/rate limit (429), it enters a temporary cooldown, and the scheduler fails over to the next candidate immediately.

---

## 4. Configuration Schema

### 4.1 `config.yaml` Additions

```yaml
# Global scheduler configuration
scheduler:
  strategy: "reset-priority" # Options: round-robin, fill-first, weighted, reset-priority
  reset_priority:
    urgent_threshold_seconds: 900    # 15-minute window for imminent reset
    clustering_jitter_seconds: 60    # Group accounts resetting within 60s
    fallback_strategy: "weighted"    # Normal-tier fallback: weighted or round-robin

# Proxy cluster pools
proxy_pools:
  - id: "pool-us-res"
    strategy: "sticky"              # sticky / round-robin / random
    health_check:
      enabled: true
      test_url: "https://api.anthropic.com"
      interval_seconds: 60
      timeout_seconds: 5
    nodes:
      - "socks5://user:pass@us-node-1.proxy.net:1080"
      - "socks5://user:pass@us-node-2.proxy.net:1080"

# KunKun anti-detect browser integration
kunkun_browser:
  enabled: true
  driver_mode: "cli"                # cli or service
  project_root: "/Users/crymang/Works/KunKun"
  python_bin: "uv"
  profiles_dir: "./auths/kunkun_profiles"
  service_endpoint: "http://127.0.0.1:8088"
  default_device_mode: "browser"
  headless: false
```

> Erratum: the implemented keys are `routing.strategy` (not `scheduler.strategy`) with `routing.reset-priority-urgent-seconds` / `routing.reset-priority-jitter-seconds` / `routing.reset-priority-fallback` (defaults 900 / 60 / `weighted`); proxy pools use `proxy-pools` (`health-check` / `test-url` / `interval-seconds` / `timeout-seconds`, strategies `sticky` and `round-robin` only); KunKun uses `kunkun-browser` (`enabled` / `driver-mode` / `project-root` / `python-bin` / `profiles-dir` / `default-device-mode` / `headless` / `launch-timeout-seconds`). `service-endpoint` does not exist and the service driver is not implemented.

### 4.2 Account Credential Schema (`auths/*.json`)

```json
{
  "id": "claude-team-node01",
  "provider": "claude",
  "label": "US West Dedicated Claude Account",
  "disabled": false,
  "proxy_url": "socks5://user:pass@us-node-1.proxy.net:1080",
  "proxy_pool_id": "pool-us-res",
  "fingerprint": {
    "tls_preset": "HelloChrome_120",
    "kunkun_profile_id": "prof_us_west_01",
    "device_data_preset": "desktop_macos_sonoma",
    "user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
  },
  "quota_state": {
    "exceeded": false,
    "next_recover_at": "0001-01-01T00:00:00Z",
    "quota_reset_at": "2026-10-26T22:30:00Z",
    "window_duration_seconds": 18000,
    "signals": {
      "anthropic-ratelimit-unified-reset": "1793053800",
      "anthropic-ratelimit-unified-status": "allowed"
    }
  }
}
```

> Erratum: the runtime reads top-level `proxy_url`, `proxy_pool_id`, `tls_preset`, and `quota_window_seconds` from the auth JSON; there is no `fingerprint.*` nesting and `quota_state.*` is not configuration. `proxy_pool_id` and `tls_preset` are copied into `Auth.Attributes`. `quota_state.signals` is a runtime-only passive snapshot, and `kunkun_profile_id` is not written today. The window anchor is recorded by the first successful call in `Metadata["quota_window_anchor"]`.

---

## 5. Implementation Roadmap & Milestones

| Milestone | Scope | Components | Acceptance Criteria |
| :--- | :--- | :--- | :--- |
| **M1** | Quota Reset Priority Scheduler | `sdk/cliproxy/auth/selector.go`<br>`sdk/cliproxy/auth/quota_signals.go`<br>`sdk/cliproxy/auth/scheduler.go` | Unit tests verify candidate prioritizing accounts with minimum $\Delta T$; seamless failover on simulated 429. |
| **M2** | Clustered Proxy & Egress Routing | `internal/config/`<br>`sdk/proxyutil/`<br>`internal/runtime/executor/helps/utls_client.go` | Per-account proxy routing validated across HTTP, WebSocket, Token Refresh, and OAuth code exchanges. |
| **M3** | KunKun Browser Integration | `internal/browser/kunkun_driver.go`<br>`sdk/auth/`<br>`internal/api/handlers/management/` | Running login command launches KunKun with dedicated proxy and isolated hardware profile, completing OAuth callback. |
| **M4** | TLS Diversification & E2E Testing | `internal/runtime/executor/helps/utls_client.go`<br>`test/` cross-module tests | 100% green tests under `go test ./...` with verify compile clean. |

---

## 6. Implementation Alignment

The delivered implementation differs from this specification in the following ways. "Intentional" marks deliberate trade-offs; the rest are follow-up gaps.

1. **`HelloChrome_Auto` is Chrome 133**: in uTLS v1.8.2 it aliases `HelloChrome_133`, so unset presets present Chrome 133, not Chrome 120. Intentional (zero-config compatibility). Set `tls_preset: chrome_120` to pin the older profile.
2. **`ios_16` approximates iOS 14**: uTLS v1.8.2 has no iOS 16 profile, so `ios_16` maps to `HelloIOS_Auto` with a one-time warning. Intentional (stays in the iOS UA family). Revisit when uTLS adds a newer profile.
3. **Presets do not affect Claude**: Anthropic traffic uses `HelloCustom` with the Claude Code ClientHello; only Codex/ChatGPT consumes the account preset. Intentional (preserves Claude Code compatibility). A separate Anthropic mechanism would be required.
4. **`anthropic-ratelimit-reset` is not collected**: the parser supports it, but the passive whitelist only stores `anthropic-ratelimit-unified-*`, `retry-after`, and Codex headers. Intentional (matches observed upstream headers). Such accounts fall back to the window anchor.
5. **Legacy management routing endpoint rejects `reset-priority`**: `/v0/management/routing/strategy` returns 400 for the new value because that deprecated endpoint was left unchanged. Config files and hot reload work. Use the supported v8/new management surface instead.
6. **Web UI OAuth can open a second browser**: with KunKun enabled the server launches KunKun while the Web UI still opens the returned authorization URL. Intentional (keeps existing Web UI behavior). Suppressing one side or disabling `kunkun-browser` avoids it.
7. **`profile_id` is regenerated per login**: IDs are `provider-<uuid>` and are not written back to `Auth.Attributes["kunkun_profile_id"]`, so relogin does not reuse the sandbox. Follow-up: persist and reuse the profile ID.
8. **Explicit OAuth proxy outranks the pool**: the login proxy is persisted as `proxy_url`, so a failed node is not automatically rebound through the pool. Intentional (keeps login egress equal to runtime egress). Follow-up: record pool provenance and allow re-resolution only when the explicit proxy equals the historical node.
9. **Request-path dial failures do not trip the breaker**: `Manager.ReportDialResult` exists but is not called from request execution; health probes are the primary input. Intentional (keeps the hot path decoupled). Follow-up: report dial errors per pool/node.
10. **Config API keys cannot bind pools**: `claude-api-key` and similar config-defined credentials have no Auth load path and therefore no `proxy_pool_id` support; only `auths/*.json` or runtime-registered Auth records can bind pools. Follow-up: map pool IDs while synthesizing Auth records from config.
