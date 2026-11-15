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
