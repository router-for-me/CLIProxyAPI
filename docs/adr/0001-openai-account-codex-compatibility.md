# Expose OpenAI accounts through the Codex provider

The proxy exposes `openai` as the external provider name for OpenAI account OAuth while retaining `codex` as the canonical provider for stored credentials, account scheduling, and execution. This preserves existing auth files and runtime behavior while allowing clients to discover an OpenAI account login surface and select model, reasoning effort, and Fast mode capabilities.

## Considered Options

- Rename the stored provider from `codex` to `openai`. Rejected because existing credentials and Codex-specific executor dispatch depend on the canonical identity.
- Treat OpenAI accounts as OpenAI API-key compatibility providers. Rejected because ChatGPT subscription OAuth credentials use the Codex account and Responses flow.

## Consequences

Clients can use `/v0/management/openai-auth-url`, `/openai/callback`, `-openai-login`, and `-openai-device-login`. Existing Codex routes and account files remain valid. The Codex request adapter accepts `service_tier: "fast"` and normalizes it to the upstream `priority` tier.
