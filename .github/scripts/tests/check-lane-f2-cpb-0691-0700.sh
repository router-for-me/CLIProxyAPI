#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
REPORT="${ROOT_DIR}/docs/planning/reports/issue-wave-cpb-0691-0700-lane-f2-implementation-2026-02-23.md"
QUICKSTARTS="${ROOT_DIR}/docs/provider-quickstarts.md"

# Files exist
[ -f "${REPORT}" ]
[ -f "${QUICKSTARTS}" ]

# Tracker coverage for all 10 items
for id in 0691 0692 0693 0694 0695 0696 0697 0698 0699 0700; do
  rg -n "CPB-${id}" "${REPORT}" >/dev/null
  rg -n "CPB-${id}" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv >/dev/null
done

# Docs coverage anchors
rg -n "Copilot Unlimited Mode Compatibility" "${QUICKSTARTS}" >/dev/null
rg -n "OpenAI->Anthropic Event Ordering Guard" "${QUICKSTARTS}" >/dev/null
rg -n "Gemini Long-Output 429 Observability" "${QUICKSTARTS}" >/dev/null
rg -n "Global Alias \+ Model Capability Safety" "${QUICKSTARTS}" >/dev/null
rg -n "Load-Balance Naming \+ Distribution Check" "${QUICKSTARTS}" >/dev/null

# Focused regression signal
( cd "${ROOT_DIR}" && go test ./pkg/llmproxy/translator/openai/claude -run 'TestEnsureMessageStartBeforeContentBlocks' -count=1 )

echo "lane-f2-cpb-0691-0700: PASS"
