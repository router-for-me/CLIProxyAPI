#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

# Guard against unresolved generator placeholders in planning reports.
# Allow natural-language "undefined" mentions; block explicit malformed token patterns.
PATTERN='undefinedBKM-[A-Za-z0-9_-]+|undefined[A-Z0-9_-]+undefined'

if rg -n --pcre2 "$PATTERN" docs/planning/reports -g '*.md'; then
  echo "[FAIL] unresolved placeholder-like tokens detected in docs/planning/reports"
  exit 1
fi

echo "[OK] no unresolved placeholder-like tokens in docs/planning/reports"
