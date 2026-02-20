# Agent Operating Model

## Execution Loop

1. Route request into OpenAI-compatible API surface.
2. Resolve provider/model translation and auth context.
3. Execute request with quotas, cooldown, and resilience controls.
4. Emit structured logs and monitoring signals.
