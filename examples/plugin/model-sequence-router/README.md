# Model Sequence Router Plugin

`model-sequence-router` exposes client-visible model aliases and routes each conversation through an ordered sequence of built-in providers and models. Clients request the configured alias; provider selection and credentials remain server-side.

The plugin selects one target per logical request. It does not combine model outputs or send one model's output to another model.

## Build

From the repository root, use the dedicated verification script:

```bash
./scripts/build-model-sequence-router.sh --plugin-only
./scripts/build-model-sequence-router.sh --focused
./scripts/build-model-sequence-router.sh --full
```

The script is the single build and verification entry point. It detects all available host CPUs, runs Go and Make work with that parallelism, selects the platform artifact extension (`.so` on Linux, `.dylib` on macOS, or `.dll` on Windows under Git Bash/MSYS2), and removes its temporary build and Go cache data. Use `--plugin-only` to format, race-test, and build only this plugin, `--focused` while developing its core integration, and `--full` for repository-wide race and ordinary test suites.

The unversioned deployment artifact is `examples/plugin/bin/model-sequence-router.so` on Linux, `model-sequence-router.dylib` on macOS, or `model-sequence-router.dll` on Windows. The script also reads the plugin's declared version and stages the same fresh binary as `plugins/model-sequence-router-v<version>.<extension>` for live upgrades. Copy that versioned artifact into the production plugin directory and then pin its version in configuration as described in [the deployment guide](../../../plugins/model-sequence-router.md#safe-upgrades-and-hot-reload).

For the repository's local `luna-haiku` test configuration, build and start a CGO-enabled server with:

```bash
./scripts/start-model-sequence-router.sh
```

The launcher builds when the runnable binary is missing or source files changed, installs the deployment artifact into `plugins/`, and starts the isolated test server configured on port `8318`. Later launches reuse the existing build and perform no Git operations. Pass `--rebuild` to force verification and rebuilding; any other flags are forwarded to the server.

## Codex and Claude example

```yaml
routing:
  strategy: round-robin
  session-affinity: true
  session-affinity-ttl: 1h

plugins:
  enabled: true
  dir: plugins
  configs:
    model-sequence-router:
      enabled: true
      priority: 100
      session_ttl: 1h
      aliases:
        - alias: iterative-model
          display_name: Iterative Model
          random_start: true
          targets:
            - provider: codex
              model: terra
              repeat: 3
            - provider: claude
              model: claude-opus-4-6
              repeat: 1
```

For every independently identified conversation, requests dispatch as:

```text
codex/terra → codex/terra → codex/terra → claude/claude-opus-4-6 → repeat
```

Two simultaneous conversations maintain separate positions. By default, each new or expired conversation chooses a uniformly random effective sequence position and then follows the configured order. Set `random_start: false` on an alias for deterministic position-zero starts. Concurrent generations in one conversation reserve sequence positions atomically in dispatch order; response completion order may differ. A failed upstream request still consumes its reserved position.

## Arbitrary multi-provider example

Targets are expanded in configured order, so repeated blocks can express patterns that are not equivalent to weights:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    model-sequence-router:
      enabled: true
      priority: 100
      session_ttl: 2h
      aliases:
        - alias: research-cycle
          display_name: Research Cycle
          targets:
            - provider: codex
              model: terra
              repeat: 2
            - provider: claude
              model: claude-opus-4-6
            - provider: codex
              model: terra
            - provider: antigravity
              model: gemini-3.1-pro
```

This produces:

```text
Terra → Terra → Opus → Terra → Gemini Pro → repeat
```

Any number of aliases, targets, and providers is supported. `repeat` defaults to `1`; an alias's expanded sequence is limited to 65,536 positions.

## Worked example: four aliases

Nothing in this section is a default. The plugin ships no aliases, no models, and no effort tiers; every value below is an illustration of the mechanism, and the model names are placeholders for whatever a deployment actually has credentials for. Alias names, sequences, scores, and tiers are all deployment choices.

The four aliases below span a capability range. Two state no tiers and two retune their GPT slots.

### Illustrative intelligence scores

Effort remapping is worth configuring when two models in one rotation answer the same effort label at different levels. The figures below are an example measurement set used to derive the tiers that follow; substitute current measurements for the models in use.

| Score | Model | Effort | Cost |
|---:|---|---|---:|
| 24 | Claude Haiku 4.5 | non-reasoning | not published |
| 33 | Luna | low | $68.80 |
| 37 | Claude Haiku 4.5 | reasoning | $619.69 |
| 38 | Luna | medium | $105.84 |
| 40 | Terra | low | $160.65 |
| 46 | Terra | medium | $240.23 |
| 49 | Sol | low | $353.49 |
| 53 | Claude Sonnet 5 | max | $4010.12 |
| 54 | Sol | medium | $593.04 |
| 55 | Terra | max | $2060.40 |
| 56 | Sol | high | $955.55 |
| 56 | Claude Opus 4.8 | max | $3752.55 |
| 58 | Sol | xhigh | $1542.52 |
| 59 | Sol | max | $2824.18 |
| 60 | Claude Fable 5 | max, with fallback | $5630.52 |

### Entry tier, no remapping

```yaml
- alias: mist
  display_name: Mist
  targets:
    - provider: codex
      model: gpt-5.6-luna
      repeat: 1
    - provider: claude
      model: claude-haiku-4-5-20251001
      repeat: 1
```

Haiku 4.5 carries no effort ladder in the sample data: one non-reasoning point and one reasoning point, with nothing between. Tiers derived from a two-point curve would collapse every sub-`max` request onto a single Luna setting and discard the requested level, so omitting `efforts` preserves caller intent.

### Balanced tier, no remapping

```yaml
- alias: jade
  display_name: Jade
  targets:
    - provider: codex
      model: gpt-5.6-terra
      repeat: 1
    - provider: claude
      model: claude-sonnet-5
      repeat: 1
```

Terra scores at or above Sonnet 5 at the only level both publish, so no tier would raise parity and every tier would raise cost. When two slots already track each other, write no `efforts` map.

### Frontier tier, effort retuned in place

```yaml
- alias: opal
  display_name: Opal
  targets:
    - provider: codex
      model: gpt-5.6-sol
      repeat: 1
      efforts:
        medium: high
        high: xhigh
    - provider: claude
      model: claude-opus-5
      repeat: 1
```

| Requested | Position 0 emitted | Position 1 emitted | Rule |
|---|---|---|---|
| `low` | `gpt-5.6-sol(low)` | `claude-opus-5(low)` | passthrough |
| `medium` | `gpt-5.6-sol(high)` | `claude-opus-5(medium)` | tier |
| `high` | `gpt-5.6-sol(xhigh)` | `claude-opus-5(high)` | tier |
| `xhigh` | `gpt-5.6-sol(xhigh)` | `claude-opus-5(xhigh)` | passthrough |
| `max` | `gpt-5.6-sol(max)` | `claude-opus-5(max)` | reserved |
| `8000` | `gpt-5.6-sol(8000)` | `claude-opus-5(8000)` | outside the level lattice |
| none | `gpt-5.6-sol` | `claude-opus-5` | absent suffix |

In the sample data the Sol ladder rises steeply at the bottom and flattens at the top, so its middle labels sit below the Claude model answering the same label in the same rotation. Shifting `medium` and `high` one rung up closes that band. The ends stay unwritten deliberately: `low` is the cheapest entry, `max` is reserved by rule, and `xhigh` already resolves to the rung `high` was raised to.

### Replacing the model for one effort

A tier may name a different model of the same provider instead of, or in addition to, an effort. This suits a model whose strength sits at one point of the ladder rather than across it.

```yaml
- alias: opal-substituted
  display_name: Opal Substituted
  targets:
    - provider: codex
      model: gpt-5.6-sol
      repeat: 1
      efforts:
        medium: high
        high: xhigh
    - provider: claude
      model: claude-opus-5
      repeat: 1
      efforts:
        medium:
          model: claude-opus-4-8
          effort: medium
        high:
          model: claude-opus-4-8
```

| Requested | Position 1 emitted | Tier form |
|---|---|---|
| `low` | `claude-opus-5(low)` | no tier, passthrough |
| `medium` | `claude-opus-4-8(medium)` | `model` and `effort` |
| `high` | `claude-opus-4-8(high)` | `model` only, caller effort carried across |
| `max` | `claude-opus-5(max)` | no tier, reserved |

Both substituted levels stay within the `claude` provider, because `provider` is the root of a target definition and a tier refines only a model that same provider serves.

### Maximum tier, repeated slots

```yaml
- alias: ruby
  display_name: Ruby
  targets:
    - provider: codex
      model: gpt-5.6-sol
      repeat: 1
      efforts:
        medium: high
        high: xhigh
    - provider: claude
      model: claude-opus-5
      repeat: 1
    - provider: codex
      model: gpt-5.6-sol
      repeat: 1
      efforts:
        medium: high
        high: xhigh
    - provider: claude
      model: claude-opus-5
      repeat: 1
    - provider: claude
      model: claude-fable-5
      repeat: 1
```

Five positions alternate providers four times before reaching the most expensive model, spreading cheaper turns ahead of it. Both Sol slots repeat the same tiers literally rather than sharing a YAML anchor, because a management-panel rewrite of the configuration does not preserve anchors.

## Routing behavior

- Alias matching is case-insensitive and ignores a supported effort suffix. The configured alias spelling is used in model catalogs.
- The plugin selects a provider and target model. Response model presentation stays with the host and the upstream provider.
- A client effort suffix is preserved on the selected target unless that target already has its own suffix or the selected slot's effort tier states a different effort for the requested level.
- If the next provider is not currently registered, the router scans forward to the next available provider. Every matched stateful route consumes the positions it skips.
- If no configured provider is available, the route is declined so normal host routing can continue.
- Requests without an identifiable conversation always use the first available target and do not create or advance state.
- `random_start` defaults to `true` per alias. It randomizes the initial effective slot for identified conversations; truly stateless requests remain first-target selections.
- Cursor state is in memory with a sliding TTL. Proxy restart, plugin reload, disable/enable, and successful reconfiguration reset all positions.
- Successful reconfiguration atomically replaces the alias catalog and configuration. Invalid reconfiguration leaves the prior configuration and cursor state active.

### Per-slot effort tiers

A rotation slot may state an `efforts` map deciding how that slot answers one requested effort. A tier refines the slot beneath its `provider`: `model` names another model of the same provider, and `effort` names the level that model receives.

```yaml
targets:
  - provider: codex
    model: gpt-5.6-sol
    repeat: 1
    efforts:
      medium: high
      high: {effort: xhigh}
  - provider: claude
    model: claude-opus-5
    efforts:
      medium:
        model: claude-opus-4-8
        effort: medium
```

| Tier entry | Model used | Emitted effort |
|---|---|---|
| level absent | the slot's | the caller's, unchanged |
| bare level, or `effort` only | the slot's | the stated effort |
| `model` only | the tier's | the caller's, unchanged |
| `model` and `effort` | the tier's | the stated effort |

- Keys are the discrete levels `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`, spelled in lower case. A bare level is shorthand for `{effort: <level>}`.
- Omission is passthrough. A level with no tier forwards the caller's suffix to the slot's own model, so `max` reaches each model's native ceiling without an entry.
- `max` is reserved. A tier emits `max` under the `max` key and under no other, and the `max` key must emit `max`. The check reads the suffix the tier actually emits, so a tier model carrying its own `(max)` is rejected under a lower key.
- Numeric budgets, `none`, `auto`, and an absent suffix carry no discrete level. They never match a tier and pass through unchanged on the slot's own model.
- A tier cannot name a provider. `provider` is the root of a target definition, and a tier refines only a model that same provider serves.
- Across tiers that only retune the slot's own model, the mapping must not decrease, so a higher request never receives a lower effort. Tiers naming another model are exempt, because no ordering relates two distinct models.
- A suffix written on the effective `model:` value outranks both the caller suffix and the tier's `effort`.
- `repeat` shares one compiled tier map across every sequence position the slot expands into.
- An invalid `efforts` map fails reconfiguration, leaving the prior configuration and cursor state active.

## Verifying routing in logs

Set the top-level `debug: true` in `config.yaml`. Route decisions are emitted through the host logger as `model-sequence-router: selected target alias=<alias> position=<index> requested=<effort>`, where `requested=unset` marks a caller that sent no suffix. Those three values ride in the message because a host decides for itself which structured fields it prints. The same decision also carries these structured fields:

- `alias`, `sequence_index`, the caller's `requested_effort`, `provider`, and the effective target `model`
- `advanced`, `true` when a conversation cursor moved and `false` for stateless routing
- `random_start`, showing the alias policy used for a new or expired cursor
- `session_hash`, an eight-character hash used to correlate one conversation without logging its identifier

At startup or successful reconfiguration, the info log `model-sequence-router: configuration loaded and state reset` includes `alias_count`, `generation`, and `sequence_lengths`, and its JSONL record carries an explicit `event=config` discriminator. Stateless routing is identified at debug level. If none of an alias's configured providers is registered, a warning lists only the alias and its provider names. Request bodies, authorization data, credentials, prompt-cache keys, and complete session identifiers are never logged.

For the four-slot example above, filter for `model-sequence-router: selected target`. With the default `random_start: true`, a conversation begins at any one index and then follows cyclic order—for example, `2, 3, 0, 1, 2`. Set `random_start: false` when testing if you want the exact trace `0, 1, 2, 3, 0`. Host logs carry a supplementary subset of these fields; the JSONL diagnostic file is the authoritative record.

### Cache diagnostics and inspection

Enable the plugin-owned, bounded JSONL diagnostics sink without enabling raw request logging:

```yaml
diagnostics:
  enabled: true
  path: /var/tmp/cliproxy-model-sequence-router/diagnostics.jsonl
  max_size_mb: 25
  max_backups: 2
```

Inspect the current cache and routing state or follow it live:

```bash
./scripts/inspect-model-sequence-router.py
./scripts/inspect-model-sequence-router.py --follow --verbose
./scripts/inspect-model-sequence-router.py --alias iterative-model --summary-only
./scripts/inspect-model-sequence-router.py --list-reloads --since-last-reload --summary-only
./scripts/inspect-model-sequence-router.py --generation 2 --summary-only
./scripts/inspect-model-sequence-router.py \
  --since '2026-08-01T21:09:47-07:00' \
  --until '2026-08-01T22:00:00-07:00' \
  --cache-low-percent 80
```

The inspector separates two kinds of evidence. `READ`, `CREATE`, and `HITS` are actual upstream usage counters. `WARM`, `SETTINGS`, and `PREFIX` compare content-free system, tool, and history fingerprints within the same alias/session/provider/model lane. A prefix warning is a request-shape warning, not proof of a cache miss; the upstream counters are authoritative. Fingerprints, session IDs, callback IDs, and credential IDs are hashed. Request bodies, prompt text, responses, tokens, and authorization headers are not written.

Use `--since-last-reload` to avoid combining observations made under different sequence configurations. `--list-reloads` shows the UTC and local timestamp, generation, and effective sequence lengths for every recorded reload. `--generation` selects one interval between reloads. `--since` and `--until` accept ISO-8601 timestamps; a timestamp without an offset uses the host's local timezone. For window boundaries, usage events are classified by `requested_at` rather than completion time, so a request started under the previous configuration is not attributed to the new one merely because it completed after reload. The cache table reports both token-weighted `READ%` and per-request `MEDIAN`; `--cache-low-percent` controls the threshold for the `LOW` request count.

For automation, `--fail-on-risk` exits `3` for failed usage, changed settings, a history-prefix mismatch, or an opaque continuation. `--require-cache-read` exits `4` when a successful generation with input tokens reports no cache read. A lane's first upstream request is normally cold, so use the latter only when a cache read is mandatory for the observed window.

## Credentials, affinity, and caches

The recommended `routing.session-affinity` settings keep a conversation on the same credential for repeated requests to a provider when that credential remains available. Existing affinity keys include provider, session, and model, so different providers and models retain separate scopes.

The plugin preserves the client body, prompt-cache keys, headers, system prompts, and message order. Codex and Claude still maintain independent upstream caches; switching providers does not share cache contents. Cache TTLs, prefix rules, and read/write costs remain controlled by each upstream. With affinity disabled and multiple credentials configured, additional cache warmups are expected.

## v1 boundaries

- Advancement happens at dispatch reservation, not successful completion.
- Parallel completion order is not guaranteed.
- State is local to one proxy process and is not shared across replicas.
- Upstream errors may mention the selected provider or model.
- The plugin routes one model per request and does not judge, merge, or chain outputs.
- Exact provider model IDs are configuration values and must match the models available to the configured credentials.
