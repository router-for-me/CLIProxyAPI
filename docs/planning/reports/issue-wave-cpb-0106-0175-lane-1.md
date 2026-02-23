# Wave V3 Lane 1 Report (CPB-0106..CPB-0115)

Worktree: `cliproxyapi-plusplus-wave-cpb3-1`  
Branch: `workstream-cpbv3-1`  
Date: 2026-02-22

## Implemented quick wins

- Streaming troubleshooting and reproducible curl checks:
  - `docs/troubleshooting.md`
  - Covers CPB-0106 and supports CPB-0111 diagnostics.
- Qwen model visibility troubleshooting flow:
  - `docs/provider-quickstarts.md`
  - Supports CPB-0110 and CPB-0113 operator path.

## Item disposition

| Item | Disposition | Notes |
| --- | --- | --- |
| CPB-0106 | implemented | Added copy-paste stream diagnosis flow and expected behavior checks. |
| CPB-0107 | planned | Requires test-matrix expansion for hybrid routing scenarios. |
| CPB-0108 | deferred | JetBrains support requires product-surface decision outside this lane. |
| CPB-0109 | planned | Rollout safety needs auth-flow feature flag design. |
| CPB-0110 | implemented | Added Qwen model visibility verification path and remediation steps. |
| CPB-0111 | planned | Translator parity tests should be added in code-focused wave. |
| CPB-0112 | planned | Token-accounting regression fixtures needed for Minimax/Kimi. |
| CPB-0113 | implemented | Added operational checks to validate qwen3.5 exposure to clients. |
| CPB-0114 | planned | CLI extraction requires explicit command/API contract first. |
| CPB-0115 | planned | Integration surface design (Go bindings + HTTP fallback) still pending. |

## Validation

- `rg -n 'Claude Code Appears Non-Streaming|Qwen Model Visibility Check' docs/troubleshooting.md docs/provider-quickstarts.md`

## Next actions

1. Add translator tests for CPB-0111 (`response.function_call_arguments.done`) in next code lane.
2. Define a single auth rollout flag contract for CPB-0109 before implementing flow changes.
