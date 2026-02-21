# External Developer Docset

For engineers integrating `cliproxyapi++` into external services or products.

## Audience

- Teams with existing OpenAI-compatible clients.
- Platform developers adding proxy-based multi-provider routing.

## Integration Path

1. [Integration Quickstart](./integration-quickstart.md)
2. [OpenAI-Compatible API](/api/openai-compatible)
3. [Provider Usage](/provider-usage)
4. [Routing and Models Reference](/routing-reference)

## Design Guidelines

- Keep client contracts stable (`/v1/*`) and evolve provider config behind the proxy.
- Use explicit model aliases/prefixes so client behavior is deterministic.
- Add integration tests for `401`, `429`, and model-not-found paths.

## Change Awareness

- [Feature Change Reference](../../../FEATURE_CHANGES_PLUSPLUS.md)
