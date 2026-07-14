#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
baseline="$repo_root/.github/vet-baseline.txt"
raw="$(mktemp)"
normalized="$(mktemp)"
unexpected="$(mktemp)"
trap 'rm -f "$raw" "$normalized" "$unexpected"' EXIT

cd "$repo_root"
status=0
if go vet ./... >"$raw" 2>&1; then
  :
else
  status=$?
fi

sed 's#\\#/#g' "$raw" | sort -u >"$normalized"
comm -23 "$normalized" "$baseline" >"$unexpected"
if [[ -s "$unexpected" ]]; then
  echo "unexpected go vet diagnostics:" >&2
  cat "$unexpected" >&2
  echo "fix new findings or review and update the upstream baseline" >&2
  exit 1
fi
if [[ "$status" -ne 0 && ! -s "$normalized" ]]; then
  echo "go vet failed without a diagnostic" >&2
  exit "$status"
fi

echo "go vet produced no diagnostics beyond the reviewed upstream baseline"
