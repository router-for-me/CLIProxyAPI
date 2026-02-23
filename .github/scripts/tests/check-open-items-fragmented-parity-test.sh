#!/usr/bin/env bash
set -euo pipefail

script_under_test="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/check-open-items-fragmented-parity.sh"

run_case() {
  local label="$1"
  local expect_exit="$2"
  local expected_text="$3"
  local report_file="$4"

  local output status
  output=""
  status=0

  set +e
  output="$(REPORT_PATH="$report_file" "$script_under_test" 2>&1)"
  status=$?
  set -e

  printf '===== %s =====\n' "$label"
  echo "$output"

  if [[ "$status" -ne "$expect_exit" ]]; then
    echo "[FAIL] $label: expected exit $expect_exit, got $status"
    exit 1
  fi

  if ! echo "$output" | rg -q "$expected_text"; then
    echo "[FAIL] $label: expected output to contain '$expected_text'"
    exit 1
  fi
}

make_report() {
  local file="$1"
  local status_line="$2"

  cat >"$file" <<EOF_REPORT
# Open Items Validation

## Already Implemented
- Issue #258 \`Support variant fallback for reasoning_effort in codex models\`
  - $status_line
  - Current translators map top-level \`variant\` to Codex reasoning effort when \`reasoning.effort\` is absent.

## Evidence (commit/file refs)
- Issue #258 implemented:
  - Chat-completions translator maps \`variant\` fallback: pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go:56.
EOF_REPORT
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

pass_report="$tmpdir/pass.md"
make_report "$pass_report" "Status: Resolved and shipped on current main."
run_case "pass on resolved/shipped status" 0 "\[OK\] fragmented open-items report parity checks passed" "$pass_report"

fail_partial_report="$tmpdir/fail-partial.md"
make_report "$fail_partial_report" "Status: Partial pending final verification."
run_case "fail on partial/pending status" 1 "non-implemented status" "$fail_partial_report"

fail_unknown_report="$tmpdir/fail-unknown.md"
make_report "$fail_unknown_report" "Status: Investigating in QA."
run_case "fail on unknown status mapping" 1 "unrecognized completion status" "$fail_unknown_report"

echo "[OK] check-open-items-fragmented-parity script test suite passed"
