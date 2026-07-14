#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
baseline="$repo_root/.github/vet-baseline.txt"
raw="$(mktemp)"
normalized="$(mktemp)"
trap 'rm -f "$raw" "$normalized"' EXIT

cd "$repo_root"
if go vet ./... >"$raw" 2>&1; then
  :
fi

sed 's#\\#/#g' "$raw" | sort -u >"$normalized"
if ! diff -u "$baseline" "$normalized"; then
  echo "go vet diagnostics changed; fix new findings or review and update the upstream baseline" >&2
  exit 1
fi

echo "go vet produced no diagnostics beyond the reviewed upstream baseline"
