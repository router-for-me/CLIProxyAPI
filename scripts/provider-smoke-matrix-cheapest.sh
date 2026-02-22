#!/usr/bin/env bash
set -euo pipefail

# Convenience matrix for cheap/lowest-cost aliases used in provider smoke checks.
#
# This keeps CI and local smoke commands reproducible while still allowing callers
# to override cases/URLs in advanced workflows.
export CLIPROXY_PROVIDER_SMOKE_CASES="${CLIPROXY_PROVIDER_SMOKE_CASES:-openai:gpt-5-codex-mini,claude:claude-3-5-haiku-20241022,gemini:gemini-2.5-flash,kimi:kimi-k2,qwen:qwen3-coder-flash,deepseek:deepseek-v3}"
export CLIPROXY_SMOKE_EXPECT_SUCCESS="${CLIPROXY_SMOKE_EXPECT_SUCCESS:-0}"

"$(dirname "$0")/provider-smoke-matrix.sh"
