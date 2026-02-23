#!/usr/bin/env bash
set -euo pipefail

report="${REPORT_PATH:-docs/reports/fragemented/OPEN_ITEMS_VALIDATION_2026-02-22.md}"
issue_id="${ISSUE_ID:-258}"
if [[ ! -f "$report" ]]; then
  echo "[FAIL] Missing report: $report"
  exit 1
fi

section="$(
  awk -v issue_id="$issue_id" '
    BEGIN {
      in_target = 0
      target = "^- (Issue|PR) #" issue_id "([[:space:]]|$)"
      boundary = "^- (Issue|PR) #[0-9]+([[:space:]]|$)"
    }
    $0 ~ target {
      in_target = 1
      print
      next
    }
    in_target && $0 ~ boundary {
      exit
    }
    in_target {
      print
    }
  ' "$report"
)"
if [[ -z "$section" ]]; then
  echo "[FAIL] $report missing Issue #$issue_id section."
  exit 1
fi

status_line="$(
  printf '%s\n' "$section" \
    | rg -i -m1 '^\s*-\s*(#status|status)\s*:\s*.+$' \
    || true
)"
if [[ -z "$status_line" ]]; then
  echo "[FAIL] $report missing explicit status mapping for #$issue_id (expected '- Status:' or '- #status:')."
  exit 1
fi

status_value="$(printf '%s\n' "$status_line" \
  | sed -E 's/^\s*-\s*(#status|status)\s*:\s*//I' \
  | tr '[:upper:]' '[:lower:]')"
if printf '%s\n' "$status_value" | rg -q '\b(partial|partially|blocked|pending|todo|not implemented)\b'; then
  echo "[FAIL] $report status for #$issue_id is not implemented: $status_value"
  exit 1
fi

if ! printf '%s\n' "$status_value" | rg -q '\b(implemented|done|fixed|resolved|complete|completed)\b'; then
  echo "[FAIL] $report status for #$issue_id is not recognized as implemented: $status_value"
  exit 1
fi

if ! rg -n "pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go" "$report" >/dev/null 2>&1; then
  echo "[FAIL] $report missing codex variant fallback evidence path."
  exit 1
fi

echo "[OK] fragmented open-items report parity checks passed"
