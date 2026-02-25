#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
REPORT="${ROOT_DIR}/docs/planning/reports/issue-wave-cpb-0546-0555-lane-f-implementation-2026-02-23.md"
QUICKSTARTS="${ROOT_DIR}/docs/provider-quickstarts.md"
OPERATIONS="${ROOT_DIR}/docs/provider-operations.md"
BOARD1000="${ROOT_DIR}/docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv"

test -f "${REPORT}"
test -f "${QUICKSTARTS}"
test -f "${OPERATIONS}"
test -f "${BOARD1000}"

for id in 0546 0547 0548 0549 0550 0551 0552 0553 0554 0555; do
  rg -n "^CPB-${id}," "${BOARD1000}" >/dev/null
  rg -n "CPB-${id}" "${REPORT}" >/dev/null
done

rg -n "Homebrew install" "${QUICKSTARTS}" >/dev/null
rg -n "embeddings.*OpenAI-compatible path" "${QUICKSTARTS}" >/dev/null
rg -n "Gemini model-list parity" "${QUICKSTARTS}" >/dev/null
rg -n "Codex.*triage.*provider-agnostic" "${QUICKSTARTS}" >/dev/null

rg -n "Windows duplicate auth-file display safeguards" "${OPERATIONS}" >/dev/null
rg -n "Metadata naming conventions for provider quota/refresh commands" "${OPERATIONS}" >/dev/null
rg -n "TrueNAS Apprise notification DX checks" "${OPERATIONS}" >/dev/null

echo "lane-f-cpb-0546-0555: PASS"
