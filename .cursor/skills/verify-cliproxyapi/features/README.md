# CLIProxyAPI verification map

Maintained source for verifying CLIProxyAPI's user-facing behavior. Read this index, then the matching feature file.

## Baseline preconditions

- `scripts/cpa-verify.sh launch` succeeded and `scripts/cpa-verify.sh doctor` prints `OK`.
- Scratch config: 127.0.0.1, random port, one client API key, a management secret key, empty auth-dir, `-local-model`.
- Never drive an instance not started by this harness (the user's `:8317` server, Docker compose stacks).

## Driving conventions

- Harness: `scripts/cpa-verify.sh drive <feature>`; manual steps use curl against `http://127.0.0.1:$(cat $CPA_RUN/port)`.
- Read keys from `$CPA_RUN/apikey` / `$CPA_RUN/mgmtkey`; never echo them.
- Prefer route paths and JSON fields over log text.

## Proof and skip reporting

- Record request, status, headers and body for both the rejected and accepted path.
- Mutations need a second read (API GET and on-disk config.yaml).
- Paths needing real provider OAuth (actual completions, `-*-login`) are SKIPPED with the reason, never PASS.

## Features

- [Health](./health.md) — `/healthz` liveness.
- [Model listing & client auth](./models.md) — `/v1/models` (OpenAI/Claude shapes), `/v1beta/models`, API-key enforcement.
- [Management API](./management.md) — `/v0/management/*` key enforcement, config read, setting writes persisted to config.yaml.
- [Chat completions](./chat-completions.md) — `/v1/chat/completions` auth and no-credential error shape.
- [CLI flags](./cli.md) — `cli-proxy-api -h` and flag surface.
