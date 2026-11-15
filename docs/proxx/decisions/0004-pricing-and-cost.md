# 0004: Pricing catalog and per-request cost

Date: 2026-11-16. Status: accepted (implementation notes are appended when it lands).

## Price source

- **Do not fork `router-for-me/models`.** It has no prices and is shared with upstream refreshes.
- Default remote catalog: **models.dev** `https://models.dev/api.json` (MIT, provider → model,
  USD per 1M tokens: `cost.input/output/cache_read/cache_write`). Refreshed every 3h like the
  other catalogs. An embedded snapshot (only models proxx knows about) is used with `--local-model`
  and as a fallback. `--local-model` never fetches remote prices.
- Config overrides in `proxx.pricing.models` (USD per 1M tokens) beat the catalog.
- Considered: LiteLLM `model_prices_and_context_window.json` (MIT, wider but inconsistent keys);
  OpenRouter `/api/v1/models` (live, not redistributable). models.dev was picked for its clean
  provider/model structure and units.

## Cost resolution per request (priority order)

1. If the matching price entry has `override: true`, or `proxx.pricing.override-upstream` is true,
   use the configured/catalog price.
2. Upstream-reported cost: body `usage.cost` (OpenRouter), `usage.cost_in_usd_ticks`
   (xAI, 1 USD = 1e10 ticks), or a configured cost header (`proxx.pricing.cost-headers`).
3. Configured price (`proxx.pricing.models`).
4. Catalog price.
5. Otherwise **unknown**. That is kept separate from 0; `:free` models are an explicit 0.

The model priced is the model that actually served the request, never the group/alias name.
Token formula must not double count: cached input and reasoning output are priced from the
normalized token breakdown, so check `sdk/cliproxy/usage/accounting.go` before changing it.

Result is attached to the usage record (`cost_details.final_cost`, `cost_details.cost_source`)
in a hook at `usage.Manager.Publish`, so every consumer (queue, plugins, proxx aggregator) sees it.

## Model listings

`/v1/models` (OpenAI, Claude, Gemini formats) gets a `pricing` object per model (USD per 1M tokens
plus `source`). Pricing is injected in the handlers, not in the registry's cached conversion, so
price reloads show immediately.

## Implementation notes (2026-11-16)

What was built:

- `internal/proxx/pricing`: models.dev parser (`catalog.go`), lookup (`lookup.go`), cost
  resolution (`cost.go`), listing/management helpers (`listing.go`), runtime state (`runtime.go`).
- Embedded snapshot `internal/proxx/pricing/snapshot.json` (~43 KB, 17 providers, ~560 models):
  every model of anthropic/openai/google/xai/deepseek/mistral/moonshotai/groq/openrouter plus
  registry `models.json` models found under other providers. Regenerate with
  `go run ./cmd/proxx_gen_pricing [-in api.json]` (default input is the live models.dev URL).
- `internal/registry/price_catalog_proxx.go`: `PriceCatalogUpdater` wraps the existing
  `catalogUpdater` (generation tracking, 3h refresh, `embed`/`disabled`, keep last good catalog).
  It has its own reader with a 64 MiB cap (models.dev is ~6.6 MB, close to the 8 MiB model catalog
  cap) and never reports changed providers, so price refreshes never trigger model refreshes.
  No hook into `configureCatalogs` was needed: the proxx facade drives it from service start and
  config reload. `--local-model` (via `registry.LocalModelCatalogs()`) maps the default source to
  `embed`, so it never fetches remotely. The embedded snapshot serves lookups until a remote refresh
  succeeds.
- Hook: `sdk/cliproxy/usage.SetCostHook` (new file `cost_hook_proxx.go`), called once in
  `Manager.Publish` after identity enrichment and before enqueue. The pricing package installs it,
  so the SDK package does not import `internal/proxx`. A panic in the hook is recovered.
- `CostDetails.FinalCost` / `CostSource` (`upstream|header|config|catalog|unknown`). The hook writes
  a cloned `CostDetails`; upstream `cost` / `cost_in_usd_ticks` stay untouched. The redisqueue JSON
  embeds `CostDetails`, so `cost_details.final_cost` and `cost_source` appear there automatically.

Lookup order (`lookup`): config entries (exact before `*` prefix; provider-restricted before
unrestricted; longest prefix wins; matched against the served model, then the client alias) →
catalog under the mapped provider (`claude`→`anthropic`; `gemini`/`vertex`/`aistudio`/`gemini-cli`/
`antigravity`→`google`; `codex*`/`openai`→`openai`; `kimi*`→`moonshotai`; `xai`; compat names such as
`openrouter`/`groq` map to the same models.dev ID) → `vendor/model` under the vendor → the same ID
under any provider (first-party order: anthropic, openai, google, xai, deepseek, mistral, moonshotai,
groq, openrouter, then alphabetical) → `:free` = explicit 0 → normalized IDs (strip `models/`,
`(thinking)`, `:tag`, date suffixes, `-thinking/-high/-xhigh/-medium/-low/-minimal/-preview/-latest`)
matched against the catalog and a normalized catalog index. Config fields left unset fall back to the
catalog price of the same model. The record's `Model` is priced first, then `ResponseModel`.

Cost formula: built on `usage.TokenBreakdown` (calling `EnsureTokenBreakdownForProvider` when the
record has none), whose buckets are mutually exclusive after per-provider normalization:
`uncached×input + cache_read×cache_read + cache_write×cache_write + non_reasoning_output×output +
reasoning×(reasoning price, else output)`. Missing cache prices fall back to the input price. Unit
tests cover a Claude-shaped record (input excludes cache) and an OpenAI-shaped record (prompt
includes cached, completion includes reasoning); the e2e checks both through the real binary.

Decisions:

- Override applies only when a price was actually found; otherwise upstream cost is still used
  (better than unknown).
- OpenRouter BYOK (`cost: 0`, `is_byok: true`) uses `upstream_inference_cost` when present.
- Listings inject `pricing` in the handlers (`AttachPricing`), not in the registry's cached
  conversion, so reloads show immediately. The provider for a listed model is
  `registry.GetModelProviders(id)[0]`, the same key usage records carry. Unknown prices get no
  `pricing` object.

Limitations:

- Context tiers (`tiers`, `context_over_200k`) are ignored; the base tier is used.
- Anthropic 1h cache writes are priced at `cache_write` (the 5m price).
- Image/video/audio and per-call tool pricing are not modeled (`input_audio`, web search, etc.).
- Records whose token breakdown is unclassified/inconsistent (unknown provider semantics) get
  `unknown` unless the upstream reports a cost.
- Alias pools that rename models (compat `alias` ≠ `name`) are priced by the served upstream name;
  listings price the client-visible ID, so add a config entry for the alias if it differs.
- Retries/failovers publish one record per upstream attempt; each attempt is priced.
