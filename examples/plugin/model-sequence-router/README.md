# Model Sequence Router Plugin

`model-sequence-router` exposes client-visible model aliases and routes each conversation through an ordered sequence of built-in providers and models. Clients request and receive only the configured alias; provider selection and credentials remain server-side.

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

Token-count requests peek at the current position without advancing it. Repeated count requests therefore use the same target, and the following generation uses that target when provider availability has not changed.

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

## Routing behavior

- Alias matching is case-insensitive and ignores a supported thinking suffix. The configured alias spelling is used in model catalogs and successful response model fields.
- A client thinking suffix is preserved on the selected target unless that target already has its own suffix.
- If the next provider is not currently registered, the router scans forward to the next available provider. Stateful generations consume skipped positions; token counts do not change the cursor.
- If no configured provider is available, the route is declined so normal host routing can continue.
- Requests without an identifiable conversation always use the first available target and do not create or advance state.
- `random_start` defaults to `true` per alias. It randomizes the initial effective slot for identified conversations; truly stateless requests remain first-target selections.
- Cursor state is in memory with a sliding TTL. Proxy restart, plugin reload, disable/enable, and successful reconfiguration reset all positions.
- Successful reconfiguration atomically replaces the alias catalog and configuration. Invalid reconfiguration leaves the prior configuration and cursor state active.

## Verifying routing in logs

Set the top-level `debug: true` in `config.yaml`. Route decisions are emitted through the host logger as `model-sequence-router: selected target` with these structured fields:

- `alias`, `sequence_index`, `provider`, and the effective target `model`
- `operation` (`generate` or `count_tokens`) and `advanced` (`true` only when the cursor moved)
- `random_start`, showing the alias policy used for a new or expired cursor
- `session_hash`, an eight-character hash used to correlate one conversation without logging its identifier

At startup or successful reconfiguration, the info log `model-sequence-router: configuration loaded and state reset` includes `alias_count`, `generation`, and `sequence_lengths`. Stateless routing is identified at debug level. If none of an alias's configured providers is registered, a warning lists only the alias, provider names, and operation. Request bodies, authorization data, credentials, prompt-cache keys, and complete session identifiers are never logged.

For the four-slot example above, filter for `model-sequence-router: selected target`. With the default `random_start: true`, a conversation begins at any one index and then follows cyclic order—for example, `2, 3, 0, 1, 2`. Set `random_start: false` when testing if you want the exact trace `0, 1, 2, 3, 0`. A token count and the immediately following generation should show the same index; the count has `advanced=false` and the generation has `advanced=true`.

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
