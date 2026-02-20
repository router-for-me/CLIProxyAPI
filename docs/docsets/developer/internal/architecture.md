# Internal Architecture

## Core Boundaries

1. API entrypoint and command bootstrap (`cmd/`)
2. Proxy core and reusable translation runtime (`pkg/llmproxy`)
3. Authentication and provider adapters
4. Operational surfaces (config, auth state, logs)

## Maintainer Rules

- Keep translation logic deterministic.
- Preserve OpenAI-compatible API behavior.
- Enforce path and security governance gates.
