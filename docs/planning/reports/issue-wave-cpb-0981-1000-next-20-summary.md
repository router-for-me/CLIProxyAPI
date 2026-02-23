# Issue Wave CPB-0981-1000 Next-20 Summary

## Scope

- Window: `CPB-0981` to `CPB-1000` (20 items)
- Mode: direct implementation + docs/runbook coverage
- Date: `2026-02-23`

## Queue Snapshot

- `proposed` in board snapshot: 20/20
- `implemented in this pass`: 20/20 - WAVE COMPLETE

## IDs Implemented

### Batch 1 (P1 items)
- `CPB-0981`: Copilot thinking support (thinking-and-reasoning)
- `CPB-0982`: Copilot Claude tools forwarding (responses-and-chat-compat)
- `CPB-0983`: Kiro deleted aliases preserved (provider-model-registry)
- `CPB-0986`: Kiro web search quickstart (docs-quickstarts)
- `CPB-0988`: Kiro placeholder user message CLI (go-cli-extraction)
- `CPB-0989`: Kiro placeholder integration path (integration-api-bindings)
- `CPB-0993`: Copilot strip model suffix (thinking-and-reasoning)
- `CPB-0994`: Kiro orphaned tool_results (responses-and-chat-compat)
- `CPB-0995`: Kiro web search MCP (responses-and-chat-compat)
- `CPB-0996`: Kiro default aliases (provider-model-registry)
- `CPB-0998`: Nullable type arrays (responses-and-chat-compat)

### Batch 2 (P2 items)
- `CPB-0984`: Antigravity warn-level logging (thinking-and-reasoning)
- `CPB-0985`: v6.8.15 DX polish (general-polish)
- `CPB-0987`: v6.8.13 QA scenarios (general-polish)
- `CPB-0990`: Kiro CBOR handling (general-polish)
- `CPB-0991`: Assistant tool_calls merging (responses-and-chat-compat)
- `CPB-0992`: Kiro new models thinking (thinking-and-reasoning)
- `CPB-0997`: v6.8.9 QA scenarios (general-polish)
- `CPB-0999`: v6.8.7 rollout safety (general-polish)
- `CPB-1000`: Copilot premium count inflation (responses-and-chat-compat)

## Implemented Surfaces

- Wave Batch 12 quick probes in provider-quickstarts.md
- Runbook entries for all P1 items in provider-error-runbook.md
- CHANGELOG.md updated with all 20 IDs
- Wave summary report

## Validation Commands

```bash
rg -n "CPB-098[1-9]|CPB-099[0-9]|CPB-1000|Wave Batch 12" docs/provider-quickstarts.md docs/operations/provider-error-runbook.md CHANGELOG.md
```
