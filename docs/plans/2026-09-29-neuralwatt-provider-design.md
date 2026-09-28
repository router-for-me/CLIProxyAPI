# Neuralwatt Upstream Provider Integration — Design

**Date:** 2026-09-29  
**Status:** Approved Design  
**Scope:** Add Neuralwatt as a built-in NixLLM upstream provider with full cost, energy, and flex-tier support.

## Overview

Neuralwatt is an OpenAI-compatible LLM API provider offering:
- Bearer token authentication (API key)
- Per-request cost reporting via response headers (non-streaming) or SSE comments (streaming)
- Energy/carbon metrics (joules, CO₂ equivalent)
- Flex tier (discounted, capacity-shed) routing
- Reasoning effort levels with per-model capabilities

This design integrates Neuralwatt as a first-class NixLLM provider (like Meta, Claude, OpenAI), surfacing its unique billing and energy metadata into NixLLM's usage accounting, policy, and dashboard layers.

---

## 1. Provider Architecture & Executor

### Thin Wrapper Pattern

NixLLM will register `neuralwatt` as a built-in provider key, following the Meta pattern:

```
NeuralwattExecutor (thin wrapper)
  └─ OpenAICompatExecutor("neuralwatt")
       └─ HTTP + Bearer auth to https://api.neuralwatt.com/v1/chat/completions
```

### Files to Create/Modify

- **`internal/runtime/executor/neuralwatt_executor.go`** — Thin wrapper; delegates inference to compat executor, supplies Neuralwatt-specific credential resolution
- **`internal/runtime/executor/neuralwatt_executor_auth.go`** — Resolve baseURL + API key from auth metadata; no OAuth (unlike Meta), just static key
- **`internal/runtime/executor/init.go`** — Register `neuralwatt` in executor factory

### Credentials Model

- **`Auth.APIKey`** — Neuralwatt API key (e.g., `sk-...`)
- **`Auth.BaseURL`** — Optional override; defaults to `https://api.neuralwatt.com/v1`
- Both set via config `auths/neuralwatt.yaml` or environment variable `NEURALWATT_API_KEY`

### Usage

Operators configure a Neuralwatt upstream provider in `config.yaml`:

```yaml
providers:
  - name: neuralwatt
    type: openai-compat
    base_url: https://api.neuralwatt.com/v1
    api_key: ${NEURALWATT_API_KEY}
    models:
      - deepseek-v4-pro
      - deepseek-v4-flash
      - gemma-4-31b
```

Or via the dashboard: **Upstream Providers** → **Create** → select `neuralwatt` provider key.

---

## 2. Response Metadata Capture: Cost, Energy, Service Tier

Neuralwatt returns cost, balance, and energy data in three forms:

### Non-Streaming Requests

Response headers:
- `X-Request-Cost-USD` — Request cost (e.g., `0.003400`)
- `X-Cache-Savings-USD` — Prompt-cache savings
- `X-Allowance-Remaining-USD` — Overall account balance
- `X-NW-Service-Tier` — Actual tier served (`standard` or `flex`)
- `X-Flex-Applied` — Whether flex was applied to this request

Response body includes `energy` object:
```json
{
  "energy": {
    "energy_joules": 42.5,
    "energy_kwh": 0.0118,
    "avg_power_watts": 1200,
    "duration_seconds": 0.0354,
    "measurement_available": true,
    "carbon_g_co2eq": 3.2,
    "grid_carbon_intensity_gco2perkwhr": 271,
    "grid_id": "us-west-2"
  }
}
```

If measurement is unavailable, `energy` collapses to `{"measurement_available": false}` (other fields omitted, not zeroed).

### Streaming Requests

SSE chunks end with `[DONE]`. **Before `[DONE]`**, a comment line carries cost:

```
data: {"id":"chatcmpl-...","choices":[...],"usage":{...}}
: cost {"request_cost_usd": 0.0034, "cache_savings_usd": 0.027, "allowance_remaining_usd": 47.66}
data: [DONE]
```

Cost comment fields: `request_cost_usd`, `cache_savings_usd`, `allowance_remaining_usd`.

### Implementation

**Non-streaming (`Execute`):**
1. After HTTP response, parse headers into a struct:
   ```go
   type NeuralwattMetadata struct {
     CostUSD              float64
     CacheSavingsUSD      float64
     AllowanceRemainingUSD float64
     ServiceTier          string // "standard" | "flex"
     FlexApplied          string
   }
   ```
2. Extract `energy` object from response JSON
3. Store both in `StreamResult.Metadata`

**Streaming (`ExecuteStream`):**
1. While buffering SSE chunks, watch for the cost comment line (`: cost {...}`)
2. Parse it immediately and cache the struct
3. On stream end, extract cost from cache
4. **Fallback:** If stream is interrupted (client disconnect, network hiccup), estimate cost from `usage.completion_tokens` using Neuralwatt's public per-token rate

**Reporter integration:**
- Reporter reads `StreamResult.Metadata.neuralwatt` after stream completes
- Extracts `CostUSD`, `EnergyJoules`, `ServiceTier`, carbon data
- Records to `usage_events` (see section 3)

---

## 3. Usage Storage: Cost, Energy, Service Tier

### Schema Extensions

Add two new columns to `usage_events` table (one migration):

```sql
ALTER TABLE usage_events
  ADD COLUMN IF NOT EXISTS energy_joules NUMERIC(12,6),
  ADD COLUMN IF NOT EXISTS provider_metadata JSONB DEFAULT '{}'::jsonb;
```

### Go Struct Extension

```go
type UsageEvent struct {
  // ... existing fields ...
  ResponseServiceTier string             `json:"response_service_tier,omitempty"` // already exists
  EnergyJoules        float64            `json:"energy_joules,omitempty"`         // NEW
  ProviderMetadata    map[string]any     `json:"provider_metadata,omitempty"`     // NEW
}
```

### Storage Strategy

Per the design requirements, use **standardized cost/energy fields + provider-specific JSON blob**:

- **`cost_usd`** — NixLLM's canonical price (from `model_prices_and_context_window.json`, enforced by policy layer). Neuralwatt's reported cost reconciles offline; not used for budget enforcement.
- **`response_service_tier`** — Neuralwatt's `X-NW-Service-Tier` header (`standard` or `flex`)
- **`energy_joules`** — `energy.energy_joules` from response (NULL if unavailable)
- **`provider_metadata`** — JSON object:
  ```json
  {
    "neuralwatt": {
      "carbon_g_co2eq": 3.2,
      "grid_id": "us-west-2",
      "grid_carbon_intensity_gco2perkwhr": 271,
      "attribution_method": "...",
      "measurement_available": true,
      "cache_savings_usd": 0.027
    }
  }
  ```

### Reporter Flow

1. After request completes (streaming or non-streaming), reporter reads Neuralwatt metadata
2. Maps to `UsageEvent` fields:
   - `CostUSD` → `usage_events.cost_usd` (from NixLLM pricing, not Neuralwatt's reported cost)
   - `ServiceTier` → `usage_events.response_service_tier`
   - `EnergyJoules` → `usage_events.energy_joules`
   - Carbon/grid data → `usage_events.provider_metadata["neuralwatt"]`
3. Flusher writes row to DB

This allows policy/alerts to query `cost_usd` and `energy_joules` across all providers without JSON extraction, while keeping Neuralwatt-specific metadata queryable via `provider_metadata @> '{"neuralwatt": {...}}'`.

---

## 4. Model Registry & Configuration

### Remote Model Metadata

Neuralwatt's `GET /v1/models` endpoint returns reasoning capabilities per model:

```json
{
  "data": [
    {
      "id": "deepseek-v4-pro",
      "metadata": {
        "reasoning": {
          "supported_efforts": ["none", "minimal", "low", "medium", "high", "xhigh", "max"],
          "default_effort": "medium",
          "effort_aliases": {"auto": "medium"}
        }
      }
    }
  ]
}
```

### Implementation: Hybrid Config + Optional Refresh

**1. Config baseline:**

Operators list known Neuralwatt models in `config.yaml` (or dashboard):

```yaml
providers:
  - name: neuralwatt
    type: openai-compat
    base_url: https://api.neuralwatt.com/v1
    models:
      - name: deepseek-v4-pro
        reasoning_support: [none, minimal, low, medium, high, xhigh, max]
      - name: deepseek-v4-flash
        reasoning_support: [none, minimal]
```

**2. Optional remote refresh:**

On startup (and periodically, e.g., every 15 minutes):
- If `NEURALWATT_API_KEY` is set AND `--local-model` is NOT set:
  - Fetch `https://api.neuralwatt.com/v1/models`
  - Extract `metadata.reasoning` per model
  - **Merge** into registry: new models from remote are added; config entries take precedence for overrides
- Gracefully fails (logs warning, continues) if:
  - API key is missing
  - Endpoint is down
  - Request times out

**3. Registry binding:**

- `internal/registry` maps model IDs → reasoning capabilities
- Dashboard reasoning-selector dropdown uses this to show available efforts per model
- Executor validates `reasoning_effort` against the model's supported list before sending upstream

**4. Dashboard integration:**

When editing a Neuralwatt upstream provider entry:
- **Models tab** shows:
  - ✓ Live list (from remote `/v1/models` if refresh succeeded)
  - Reasoning efforts per model (from metadata)
  - Fallback to config baseline if refresh fails
- Operators can manually add/remove models from the config

---

## 5. Error Handling, Flex Tier & Alerts

### Neuralwatt-Specific Error Codes

| Status | Code | Meaning | Handling |
|---|---|---|---|
| **402** | — | Out of credit / overage cap | Surface distinct error to client; trigger **alert** so operators top up before the whole provider dies. |
| **429** | `concurrent_budget_exceeded` | Concurrency slot busy | `retry_after: 1` means retry *immediately*, not sleep. Map to NixLLM's cooldown logic (brief parking, not long cool). |
| **429** | `tpm_uncached_exceeded` | Rate limit (uncached tokens) | Standard cooldown with `retry_after`. |
| **503** | — | Flex-tier capacity shedding | **If `service_tier: flex` was requested:** retry once in `default` tier. If already `default`: normal upstream error. |

### Parsing

The 429 error parser reads:
- `error.code` — error type
- `error.retry_after` — seconds to wait
- `error.retryable` — boolean flag
- `error.retry_strategy` — backoff metadata (type, base, jitter, etc.)

### Flex Tier Routing

**Per-entry config:**

```yaml
providers:
  - name: neuralwatt
    entries:
      - api_key: ${NEURALWATT_KEY_1}
        service_tier: default  # Standard pricing, highest reliability
      - api_key: ${NEURALWATT_KEY_2}
        service_tier: flex     # Discounted, may shed under load
```

**Flow:**
1. Executor includes `service_tier` in request body
2. Response echoes back `X-NW-Service-Tier` header (which tier actually served)
3. `response_service_tier` recorded to `usage_events` so dashboard can show which tier was used
4. If `service_tier: flex` gets a 503, retry with `service_tier: default` on the same auth

**Dashboard:**
- When viewing requests, shows `response_service_tier` so operators can see cost savings from flex requests
- Usage feed aggregates flex vs. standard requests separately

---

## 6. Testing Strategy

### Unit Tests

- **`neuralwatt_executor_test.go`** — Thin-wrapper delegation, credential resolution
- **`neuralwatt_executor_cost_test.go`** — Header parsing (non-streaming), SSE comment parsing (streaming), interrupted-stream cost estimation fallback
- **`neuralwatt_executor_energy_test.go`** — `measurement_available: false` omit behavior (fields omitted, not zeroed); NULL handling in the DB layer
- **`neuralwatt_executor_error_test.go`** — 402/429/503 mapping, flex→default retry logic
- **`neuralwatt_models_test.go`** — `/v1/models` refresh, config precedence, graceful fallback when refresh fails

### Integration Tests

- Full request-response cycle through the compat path with a fake Neuralwatt server (httptest)
- Streaming request with SSE cost comment parsing
- Multi-auth failover and cooldown handling

### Manual Smoke Testing

- Configure a test Neuralwatt upstream with a real API key
- Send requests through the dashboard
- Verify cost, energy, and service_tier are recorded to `usage_events`
- Check dashboard Usage feed shows Neuralwatt-specific metadata

---

## 7. Implementation Roadmap

**Phase 1: Executor & Auth**
- Create `neuralwatt_executor.go` and `neuralwatt_executor_auth.go`
- Register in executor factory
- Write basic unit tests

**Phase 2: Cost & Energy Capture**
- Implement header parsing (non-streaming)
- Implement SSE comment parsing + cost fallback (streaming)
- Write cost/energy tests

**Phase 3: Usage Storage & Reporter**
- Migrate `usage_events` schema (add `energy_joules`, `provider_metadata`)
- Update `UsageEvent` struct
- Reporter integration (reads metadata, writes to DB)

**Phase 4: Model Registry & Config**
- Implement `/v1/models` refresh with fallback
- Update config schema for Neuralwatt model entries
- Dashboard rendering (Models tab)

**Phase 5: Error Handling & Flex Tier**
- Implement 402/429/503 error mapping
- Flex-tier routing and retry logic
- Alert integration (402 triggers top-up alert)

**Phase 6: Testing & Docs**
- Full integration tests
- Manual smoke testing
- Update AGENTS.md with Neuralwatt examples

---

## 8. Configuration Examples

### YAML Config

```yaml
providers:
  - name: neuralwatt
    type: openai-compat
    base_url: https://api.neuralwatt.com/v1
    api_key: ${NEURALWATT_API_KEY}
    entries:
      - api_key: sk-standard-key
        service_tier: default
      - api_key: sk-flex-key
        service_tier: flex
    models:
      - deepseek-v4-pro
      - deepseek-v4-flash
      - gemma-4-31b
```

### Dashboard

1. **Create Upstream Provider:**
   - Name: `neuralwatt`
   - Type: `openai-compat`
   - Base URL: `https://api.neuralwatt.com/v1`

2. **Add API Key Entry:**
   - API Key: paste key
   - Service Tier: select `default` or `flex`

3. **Add Models:**
   - Auto-fetch from `/v1/models` if refresh enabled
   - Or manually list in config

---

## 9. Success Criteria

- ✓ Neuralwatt requests route through the executor and reach the API
- ✓ Cost, energy, and service_tier are captured and recorded to `usage_events`
- ✓ 402/429/503 errors are handled correctly
- ✓ Flex-tier requests retry gracefully on 503
- ✓ Dashboard displays Neuralwatt cost & energy metrics
- ✓ Model registry syncs from `/v1/models` with config precedence
- ✓ All unit + integration tests pass
- ✓ Smoke test with real Neuralwatt key succeeds
