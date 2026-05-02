#!/usr/bin/env bash
# Capture backend benchmark baseline for refactor regression checks.
#
# Output:
#   bench/baseline/backend-meta.json — capture metadata (branch, commit, etc.)
#   bench/baseline/backend.txt       — concatenated `go test -bench` output

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

mkdir -p bench/baseline

CAPTURED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
BRANCH=$(git rev-parse --abbrev-ref HEAD)
COMMIT=$(git rev-parse HEAD)
COMMIT_SHORT=$(git rev-parse --short HEAD)
GO_VERSION=$(go version)

PACKAGES=(
  "./internal/logging"
  "./internal/cache"
  "./internal/api/modules/amp"
)

OUT="bench/baseline/backend.txt"
META="bench/baseline/backend-meta.json"

{
  echo "# Backend benchmark baseline"
  echo "# Captured at:  $CAPTURED_AT"
  echo "# Branch:       $BRANCH"
  echo "# Commit:       $COMMIT_SHORT ($COMMIT)"
  echo "# $GO_VERSION"
  echo ""
} > "$OUT"

for pkg in "${PACKAGES[@]}"; do
  echo "Running benchmarks in $pkg..."
  {
    echo "## $pkg"
    echo ""
    go test -bench=. -benchmem -benchtime=1s -count=1 -run='^Benchmark$' "$pkg"
    echo ""
  } >> "$OUT"
done

cat > "$META" <<JSON
{
  "captured_at": "$CAPTURED_AT",
  "branch": "$BRANCH",
  "commit": "$COMMIT",
  "commit_short": "$COMMIT_SHORT",
  "go_version": "$GO_VERSION",
  "packages": [
    "./internal/logging",
    "./internal/cache",
    "./internal/api/modules/amp"
  ],
  "output_file": "bench/baseline/backend.txt"
}
JSON

echo ""
echo "Backend baseline written to bench/baseline/backend.txt"
echo "Metadata at bench/baseline/backend-meta.json"
