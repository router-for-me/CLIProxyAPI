# 0002: Reuse existing routing before adding new routing

Date: 2026-11-16. Status: accepted.

## Context

Research on upstream (Nov 2026) found that much of "route to any available model" already exists:

- The same alias declared under several provider sections (`gemini-api-key`, `openai-compatibility`,
  `claude-api-key`, ...) is pooled across all of them: `registry.GetModelProviders` returns every
  provider, and `Manager.Execute` picks across them and fails over on 403/408/429/5xx.
- Per-credential `priority` gives strict tiers: lower tiers are used only when no higher-tier
  credential is ready.
- 429 handling: cooldown for `Retry-After` (min 10s) or exponential backoff (1s·2^n, cap 30m);
  cooled credentials are skipped by the scheduler.
- `openai-compatibility` can repeat one alias over several upstream models inside **one** provider
  entry (model pool with per-model cooldown).
- Adding API keys and models is already supported by config and the panel's providers page.

Gaps:

- No named group of **different** models across providers with an explicit order.
  Most API-key sections map an alias to only one upstream model.
- The literal model name `auto` is hijacked upstream by `util.ResolveAutoModel` (newest registered
  model, no cooldown awareness, no failover), in `handlers_routing.go` and the Responses websocket session.

## Decision

Build a thin "model groups" layer (ADR 0003) on top of `Manager.Execute` instead of a new router.
It reuses upstream cooldowns, retries and credential selection for each member.
