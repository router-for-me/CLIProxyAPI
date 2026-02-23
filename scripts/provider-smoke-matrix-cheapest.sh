#!/usr/bin/env bash
set -euo pipefail

# Convenience matrix for cheap/lowest-cost aliases used in provider smoke checks.
#
# This keeps CI and local smoke commands reproducible while still allowing callers
# to override cases/URLs in advanced workflows.

readonly default_cheapest_cases="openai:gpt-5-codex-mini,claude:claude-3-5-haiku-20241022,gemini:gemini-2.5-flash,kimi:kimi-k2,qwen:qwen3-coder-flash,deepseek:deepseek-v3"
readonly cheapest_mode="${CLIPROXY_PROVIDER_SMOKE_CHEAP_MODE:-default}"
readonly explicit_all_cases="${CLIPROXY_PROVIDER_SMOKE_ALL_CASES:-}"

if [ "${cheapest_mode}" = "all" ]; then
  if [ -z "${explicit_all_cases}" ]; then
    echo "[WARN] CLIPROXY_PROVIDER_SMOKE_ALL_CASES is empty; falling back to default cheapest aliases."
    export CLIPROXY_PROVIDER_SMOKE_CASES="${CLIPROXY_PROVIDER_SMOKE_CASES:-$default_cheapest_cases}"
  else
    export CLIPROXY_PROVIDER_SMOKE_CASES="${explicit_all_cases}"
  fi
else
  export CLIPROXY_PROVIDER_SMOKE_CASES="${CLIPROXY_PROVIDER_SMOKE_CASES:-$default_cheapest_cases}"
fi

if [ -z "${CLIPROXY_PROVIDER_SMOKE_CASES}" ]; then
  echo "[WARN] provider smoke cases are empty; script will skip."
  exit 0
fi

export CLIPROXY_SMOKE_EXPECT_SUCCESS="${CLIPROXY_SMOKE_EXPECT_SUCCESS:-0}"

if [ -n "${explicit_all_cases}" ] && [ "${cheapest_mode}" = "all" ]; then
  echo "[INFO] provider-smoke-matrix-cheapest running all-cheapest mode with ${CLIPROXY_PROVIDER_SMOKE_CASES}"
else
  echo "[INFO] provider-smoke-matrix-cheapest running default mode with ${CLIPROXY_PROVIDER_SMOKE_CASES}"
fi

"$(dirname "$0")/provider-smoke-matrix.sh"
