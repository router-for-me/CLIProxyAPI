# 0003: Model groups (auto model)

Date: 2026-11-16. Status: accepted (implementation notes are appended when it lands).

## Decision

```yaml
proxx:
  model-groups:
    - name: auto            # what clients send
      strategy: ordered     # ordered (default) | round-robin
      models:               # client-visible model names / aliases, in priority order
        - gemini-2.5-flash
        - or-kimi           # an openai-compatibility alias
        - llama-3.3-70b-versatile
```

- A request for a group name is executed member by member through the normal
  `Manager.Execute` / `ExecuteStream` / `ExecuteCount` path with `req.Model` set to the member,
  so payload rules, translation and usage accounting see the real model.
- A member is **skipped before execution** when every credential that serves it is cooling down,
  so a cooled member never blocks on upstream's `waitForCooldown`.
- Fail over to the next member on retryable errors (429, 5xx, 403/408, auth unavailable,
  model not found). Do not fail over on 400-class request errors, except context-length errors.
- Streaming fails over only before the first chunk reaches the client.
- A group named `auto` takes precedence over upstream's `ResolveAutoModel` in both places that intercept it.
- Groups are listed in `/v1/models` unless `hidden: true`.
- Hook points in upstream files are one-liners, listed in `docs/proxx/README.md`.

Design inspiration: LiteLLM Router (model groups, order tiers, cooldowns), OpenRouter `models`
fallback arrays, Portkey fallback targets.
