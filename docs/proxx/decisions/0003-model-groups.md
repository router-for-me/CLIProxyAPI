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

## Implementation notes

Landed on branch `proxx/model-groups` (2026-11-16).

### What was built

- `internal/proxx/groups`: normalized group store (atomic snapshot), `Apply(cfg)` on startup and
  every config reload, round-robin cursor, thinking-suffix handling (`auto(high)` applies `(high)`
  to each member that has no suffix of its own), and registry publication.
- `sdk/cliproxy/auth/model_groups_proxx.go`: the failover loop. A request whose provider list is
  the synthetic provider `proxx-group` is expanded member by member:
  1. resolve the member's providers with `util.GetProviderName` (skip if none);
  2. skip it before execution when `earliestPoolCooldownForModelAt` reports no ready credential;
  3. run the normal `Execute` / `ExecuteStream` / `ExecuteCount` with `req.Model` and the
     requested-model metadata set to the member (`proxx_model_group` metadata keeps the group name);
  4. on a failover-class error, move on; otherwise return the error.
  If every member was skipped or failed and some were cooling, the member that recovers first is
  run once more with upstream's normal cooldown wait (bounded by `routing.retry.max-retry-interval`;
  a longer cooldown returns 429 with `Retry-After`, exactly like a plain model request).
- `GET /v8/management/proxx/groups`: groups with per-member `providers`, `available`,
  `cooldown_until` and `credentials` (`internal/api/handlers/management/model_groups_proxx.go`).
- Response header `X-Proxx-Served-Model: <member>`, set on the gin context when a member is
  committed (header passthrough is off by default, so executor headers would not reach clients).

### Hooks in upstream files

| File | Hook |
|---|---|
| `sdk/api/handlers/handlers_routing.go` | `getRequestDetailsWithOptions`: `proxxgroups.RequestProviders(model)` before `ResolveAutoModel` |
| `sdk/api/handlers/openai/openai_responses_websocket_session.go` | `responsesWebsocketResolvedModelName`: keep group names before `ResolveAutoModel` |
| `sdk/cliproxy/auth/conductor_execution.go` | top of `Execute` / `ExecuteCount` / `ExecuteStream`: `m.proxxExecute*Group(...)` |
| `sdk/cliproxy/auth/conductor_execution.go` | after `retrySettings()` in the same three: `maxWait = proxxGroupMaxWait(ctx, maxWait)` |
| `sdk/cliproxy/service_config.go` | `commitConfigUpdate`: `proxxgroups.Apply(newCfg)` (hot reload) |
| `sdk/cliproxy/service_lifecycle.go` | `Run`: `proxxgroups.Apply(s.currentConfig())` (startup) |

### Decisions

- **Dispatch in the auth manager, not the HTTP handlers.** The handler resolves a group to the
  synthetic provider `proxx-group` and passes the group name through; `Manager.Execute*` expands it.
  One loop covers non-stream, stream, count, every entry format (OpenAI, Claude, Gemini, Responses,
  websocket) and the handler's own stream bootstrap retries. Streaming needs no channel juggling:
  the manager already buffers the stream bootstrap, so an upstream failure before the first payload
  arrives either as the returned error or as the only chunk of the result. The group layer peeks
  that first chunk and only then commits the member.
- **No-wait member execution.** Pre-skipping cooled members is not enough: a member that is ready
  but answers 429 with `Retry-After: 20` would make `Execute` sleep 20 s between retry rounds
  (verified: removing the hook makes the e2e 429 case take 64 s with the default `request-retry: 3`).
  Member executions carry a context marker that sets `maxWait` to 0, so rounds that need no wait still
  run (other credentials of the same member) but a round that would sleep ends and the group fails over.
- **Failover classes.** Fail over on status 0 (transport, auth unavailable, model not found), 401,
  402, 403, 404, 408, 413, 429 and 5xx, and on 400/422 whose message is a context-length error.
  Stop on other 4xx, interceptor terminations (`RequestTerminatedError`), request-scoped "stop"
  rules, truncated streams and client cancellation.
- **Listing via the registry.** Visible groups are registered as models of a synthetic registry
  client (`proxx-model-groups`, provider `proxx-group`, `owned_by: proxx`, `created: 0` so upstream's
  newest-model `auto` resolution never picks a group). No list handler was touched. Hidden groups
  are not registered but still route.
- **`auto` precedence.** A group named `auto` wins in both interception points; without one,
  upstream's `ResolveAutoModel` behavior is unchanged.
- Groups are disabled in Home mode (the control plane owns routing there). Nested groups are
  rejected with a warning.

### Verified end to end

`PROXX_E2E=1 go test ./test/proxx/groups_e2e/ -run E2E -v -count=1` builds the real binary, starts
it with `--local-model` against an in-process mock OpenAI-compatible upstream (11 provider entries)
and checks: member order, failover on 500 / 429 / context-length 400 (and not on plain 400),
a cooled member skipped on the next request (~0.4 ms), all-members-cooling returning 429 with
`Retry-After` in ~1 ms, payload `override` rules for the member applied (and not leaked to other
members), stream failover before the first chunk, no failover after the first chunk, Claude
`/v1/messages` (stream and non-stream), `/v1/responses`, round-robin rotation, `/v1/models`
and `/v1beta/models` listings without hidden groups, hidden group routing, the management endpoint, hot reload, and
that plain model requests carry no proxx header.

### Limitations

- The response `model` field is whatever the member's upstream/translator reports (for an
  openai-compatibility alias that is the upstream model ID, e.g. `up-a`), the same as a direct alias
  request (verified in the e2e: a direct `model-c` request reports `up-c` in both the OpenAI and
  Claude formats). Use `X-Proxx-Served-Model` for the client-visible member name.
- `X-Proxx-Served-Model` is not set on Responses websocket sessions.
- Request interceptors, model routers and request-lifecycle plugin events see the group name;
  executors, payload rules and usage records see the member. A plugin model router that claims
  the group name runs first and bypasses the group.
- `SupportsApplyPatchModel` and `HasAlternativeExecutionTarget` report false for group names
  (the synthetic provider has no executor); websocket credential pinning does not apply to groups.
- Upstream marks a credential/model on 5xx and 429 with a cooldown; a member that just failed is
  therefore skipped by later requests until it recovers (desired for free-tier APIs).
- The round-robin cursor is in memory and per process.
- A group whose name equals a real model ID shadows that model (logged as a warning).
