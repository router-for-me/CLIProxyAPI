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

## Verified behavior (e2e, 2026-11-16)

The first subtest of `test/proxx/groups_e2e` checks the existing behavior on the real binary:
the alias `shared` declared under two `openai-compatibility` entries, `pa` (`priority: 10`) and
`pb` (`priority: 0`), each with one key.

- With both healthy, every request goes to `pa` (3/3 requests). Higher `priority` is a strict tier,
  not a weight.
- When `pa` returns 500, the **same** request fails over to `pb` (`up-shared-a` then `up-shared-b`,
  HTTP 200 to the client).
- The next request goes straight to `pb`: the 500 put `pa`'s credential for that model into a short
  cooldown, and the scheduler skips cooling credentials.

So "one alias, many providers" already gives failover for a single model. Model groups (ADR 0003)
are still needed for ordered fallback across **different** models and for the `auto` name.
