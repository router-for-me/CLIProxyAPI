# Provider-specific session affinity

Local built-in routing can use a different fallback strategy and affinity precedence for an individual provider. Omitted overrides preserve the current global strategy and session-first affinity. This is a routing policy; CLIProxyAPI does not infer subscription types or fetch OAuth usage to assign credential priorities.

```yaml
routing:
  strategy: fill-first
  session-affinity: true
  session-affinity-overrides:
    claude:
      strategy: round-robin
      precedence: priority
```

`strategy` accepts the existing `round-robin`, `fill-first` and `weighted-round-robin` strategies; omission inherits the global strategy. `precedence` accepts `session` (default) or `priority`. Overrides are ignored when session affinity is disabled. The same fields work in legacy and v8 configuration layouts.

With priority precedence, new conversations use the highest eligible credential priority tier. An established conversation keeps its credential while that credential remains in the highest eligible tier. If a higher tier recovers or a credential's priority changes, the next request rebinds through the provider's fallback strategy. Responses already in progress are not interrupted. Moving between credentials may lose prompt-cache reuse. Equal-priority sessions remain sticky.

Explicit sessions, parent/subagent and guardian inheritance, and prefix-matched conversations follow the same precedence. Model support, cooldown, retry exclusions and explicit credential pins retain their existing authority. Temporary failures retain the existing `session-affinity-rebind-on` behavior within a priority tier, but cannot retain a lower-tier binding when a higher eligible tier exists.

The provider override also applies when execution uses the mixed entrypoint for a homogeneous provider pool. Heterogeneous mixed pools retain the global policy because they do not have one unambiguous provider override.

Authenticated `GET /v8/management/routing/capabilities` reports `provider_affinity_overrides: true` and whether `local_builtin_selector` currently owns selection. Consumers should require both values before activating this policy. Home, plugin schedulers and custom selectors can select before this policy and are not supported by this capability. Configuration text or a base version string alone does not establish that a running binary supports it.

## Conditional priority updates

`PATCH /v8/management/credentials/priority` accepts `{ "name": "credential.json", "priority": 4, "expected_priority": null }`. The expected value is compared atomically with the latest credential under the manager mutation gate. A mismatch returns HTTP 409 without changing the credential. Both priority fields accept safe integers or null; null means absence/removal, while zero and negative values are preserved exactly. Other credential metadata, OAuth refresh state and cooldowns are retained. `credential_priority_cas: true` in the capability response advertises this operation. Controllers should journal ownership, check the precondition on every change and verify runtime readback before claiming success.
