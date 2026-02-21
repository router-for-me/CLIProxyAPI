# Internal Developer Docset

For maintainers extending or operating `cliproxyapi++` internals.

## Audience

- Contributors working in `pkg/` and `cmd/`.
- Maintainers shipping changes to API compatibility, routing, or auth subsystems.

## Read First

1. [Internal Architecture](./architecture.md)
2. [Feature Changes in ++](../../../FEATURE_CHANGES_PLUSPLUS.md)
3. [Feature Guides](/features/)
4. [API Index](/api/)

## Maintainer Priorities

- Preserve OpenAI-compatible external behavior.
- Keep translation and routing behavior deterministic.
- Avoid breaking management and operational workflows.
- Include docs updates with any surface/API behavior change.
