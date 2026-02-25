#!/usr/bin/env bash
set -euo pipefail

report="${REPORT_PATH:-docs/reports/fragemented/OPEN_ITEMS_VALIDATION_2026-02-22.md}"
if [[ ! -f "$report" ]]; then
  echo "[FAIL] Missing report: $report"
  exit 1
fi

section="$(awk '
  BEGIN { in_issue=0 }
  /^- Issue #258/ { in_issue=1 }
  in_issue {
    if ($0 ~ /^- (Issue|PR) #[0-9]+/ && $0 !~ /^- Issue #258/) {
      exit
    }
    print
  }
' "$report")"

if [[ -z "$section" ]]; then
  echo "[FAIL] $report missing Issue #258 section."
  exit 1
fi

status_line="$(echo "$section" | awk 'BEGIN{IGNORECASE=1} /- (Status|State):/{print; exit}')"
if [[ -z "$status_line" ]]; then
  echo "[FAIL] $report missing explicit status line for #258 (expected '- Status:' or '- State:')."
  exit 1
fi

status_lower="$(echo "$status_line" | tr '[:upper:]' '[:lower:]')"

if echo "$status_lower" | rg -q "\b(partial|partially|not implemented|todo|to-do|pending|wip|in progress|open|blocked|backlog)\b"; then
  echo "[FAIL] $report has non-implemented status for #258: $status_line"
  exit 1
fi

if ! echo "$status_lower" | rg -q "\b(implemented|resolved|complete|completed|closed|done|fixed|landed|shipped)\b"; then
  echo "[FAIL] $report has unrecognized completion status for #258: $status_line"
  exit 1
fi

if ! rg -n "pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go" "$report" >/dev/null 2>&1; then
  echo "[FAIL] $report missing codex variant fallback evidence path."
  exit 1
fi

echo "[OK] fragmented open-items report parity checks passed"
