#!/usr/bin/env bash
set -euo pipefail

REPORT="docs/planning/reports/issue-wave-cpb-0556-0610-lane-d-implementation-2026-02-23.md"
BOARD1000="docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv"
BOARD2000="docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv"

if [[ ! -f "$REPORT" ]]; then
  echo "[FAIL] missing report: $REPORT"
  exit 1
fi

for id in 0556 0557 0558 0559 0560 0606 0607 0608 0609 0610; do
  if ! rg -n "CPB-${id}" "$REPORT" >/dev/null; then
    echo "[FAIL] missing CPB-${id} section in report"
    exit 1
  fi
  if ! rg -n "^CPB-${id},.*implemented-wave80-lane-j" "$BOARD1000" >/dev/null; then
    echo "[FAIL] CPB-${id} missing implemented marker in 1000-board"
    exit 1
  fi
  if ! rg -n "CP2K-${id}.*implemented-wave80-lane-j" "$BOARD2000" >/dev/null; then
    echo "[FAIL] CP2K-${id} missing implemented marker in 2000-board"
    exit 1
  fi
done

implemented_count="$(rg -n 'Status: `implemented`' "$REPORT" | wc -l | tr -d ' ')"
if [[ "$implemented_count" -lt 10 ]]; then
  echo "[FAIL] expected at least 10 implemented statuses, got $implemented_count"
  exit 1
fi

if ! rg -n 'Lane-D Validation Checklist \(Implemented\)' "$REPORT" >/dev/null; then
  echo "[FAIL] missing lane validation checklist"
  exit 1
fi

echo "[OK] wave80 lane-d CPB-0556..0560 + CPB-0606..0610 report validation passed"
