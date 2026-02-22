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
5. [Planning Boards](/planning/)
6. [Board Workflow](/planning/board-workflow)

## Design Guidelines

- Keep client contracts stable (`/v1/*`) and evolve provider config behind the proxy.
- Use explicit model aliases/prefixes so client behavior is deterministic.
- Add integration tests for `401`, `429`, and model-not-found paths.

## Change Awareness

- [Feature Change Reference](../../../FEATURE_CHANGES_PLUSPLUS.md)
- [2000-Item Execution Board](../../../planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.md)
- [GitHub Project Import CSV](../../../planning/GITHUB_PROJECT_IMPORT_CLIPROXYAPI_2000_2026-02-22.csv)
