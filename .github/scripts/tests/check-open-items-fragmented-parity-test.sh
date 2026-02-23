#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
script_path="$repo_root/.github/scripts/check-open-items-fragmented-parity.sh"
fixtures_dir="$repo_root/.github/scripts/tests/fixtures/open-items-parity"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

run_case() {
  local name="$1"
  local fixture="$2"
  local should_pass="$3"

  local out_file="$tmp_dir/$name.out"
  set +e
  REPORT_PATH="$fixtures_dir/$fixture" ISSUE_ID=258 "$script_path" >"$out_file" 2>&1
  local status=$?
  set -e

  if [[ "$should_pass" == "pass" && $status -ne 0 ]]; then
    echo "[FAIL] $name: expected pass"
    cat "$out_file"
    exit 1
  fi
  if [[ "$should_pass" == "fail" && $status -eq 0 ]]; then
    echo "[FAIL] $name: expected fail"
    cat "$out_file"
    exit 1
  fi
  echo "[OK] $name"
}

run_case "passes_with_status_implemented" "pass-status-implemented.md" "pass"
run_case "passes_with_hash_status_done" "pass-hash-status-done.md" "pass"
run_case "fails_with_partial_status" "fail-status-partial.md" "fail"
run_case "fails_without_status_mapping" "fail-missing-status.md" "fail"

