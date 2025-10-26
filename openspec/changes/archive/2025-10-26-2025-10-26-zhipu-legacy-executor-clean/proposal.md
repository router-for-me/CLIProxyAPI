# Proposal: zhipu-legacy-executor-clean

## Summary
- Make Python Agent Bridge the only execution path for provider="zhipu".
- Remove legacy fallback (OpenAICompatExecutor) for zhipu, including streaming.
- Enforce local-only Bridge URL by default; allow remote with CLAUDE_AGENT_SDK_ALLOW_REMOTE=true.

## Why
- Reduce complexity and maintenance overhead from dual paths.
- Improve observability and security with a single, validated upstream path.
- Align with product direction: Python Agent Bridge as the canonical integration for GLM.

## What Changes
1) Routing: GLM (glm-*) strictly maps to provider "zhipu"; remove iFlow GLM entries.
2) Execution: ZhipuExecutor requires Python Agent Bridge; legacy fallback removed for both streaming and non-streaming.
3) Security: Validate Bridge URL scheme/host; default local-only, remote allowed via env gate.
4) Docs: Update examples and migration guidance for breaking changes.

## Scope
- Update executors and provider resolution to route GLM models to zhipu only.
- Add Bridge URL validation and diagnostics.
- Update examples and migration docs.

## Out of Scope
- Changes to other providers.
- Introduction of official zhipu/bigmodel SDKs.

## Acceptance Criteria
- GLM (glm-*) always resolves to zhipu.
- Disabling Python Agent returns a diagnostic error.
- Bridge URL defaults to localhost and rejects remote unless opt-in env is set.
- Docs and examples reflect breaking changes and migration steps.
