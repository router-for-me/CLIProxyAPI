#!/usr/bin/env bash
set -euo pipefail

report="docs/reports/fragemented/OPEN_ITEMS_VALIDATION_2026-02-22.md"
if [[ ! -f "$report" ]]; then
  echo "[FAIL] Missing report: $report"
  exit 1
fi

section="$(awk '/Issue #258/{flag=1} flag{print} /^- (Issue|PR) #[0-9]+/{if(flag && $0 !~ /Issue #258/) exit}' "$report")"
if [[ -z "$section" ]]; then
  echo "[FAIL] $report missing Issue #258 section."
  exit 1
fi

if echo "$section" | rg -q "Partial:"; then
  echo "[FAIL] $report still marks #258 as Partial; update to implemented status with current evidence."
  exit 1
fi

if ! echo "$section" | rg -qi "implemented"; then
  echo "[FAIL] $report missing implemented status text for #258."
  exit 1
fi

if ! rg -n "pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go" "$report" >/dev/null 2>&1; then
  echo "[FAIL] $report missing codex variant fallback evidence path."
  exit 1
fi

echo "[OK] fragmented open-items report parity checks passed"
