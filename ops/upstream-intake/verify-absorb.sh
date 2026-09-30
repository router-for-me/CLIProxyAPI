#!/usr/bin/env bash
#
# Regression gate for absorbing an upstream release.
#
# Exits 0 only when every stage passes. Any failure means the port is not
# absorbed. Run this from the repository root.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

# Packages whose concurrency behaviour matters for this fork: the responses
# tools pipeline, the auth conductor and its scheduling, the executors
# (including the Codex websocket path) and the cross-module integration tests.
RACE_PACKAGES=(
  ./internal/responsestools
  ./internal/runtime/executor
  ./internal/runtime/executor/helps
  ./sdk/cliproxy/auth
  ./internal/cache
  ./internal/wsrelay
  ./test
)

failures=()

stage() {
  name="$1"
  shift
  printf '\n=== %s ===\n' "$name"
  if "$@"; then
    printf 'PASS: %s\n' "$name"
  else
    printf 'FAIL: %s\n' "$name"
    failures+=("$name")
  fi
}

check_gofmt() {
  # Inspect tracked source only; propagate formatter failures.
  unformatted="$(git ls-files -z '*.go' | xargs -0 gofmt -l)" || return 1
  if [ -n "$unformatted" ]; then
    printf 'these files need gofmt:\n%s\n' "$unformatted"
    return 1
  fi
  return 0
}

check_build() {
  local build_dir
  build_dir="$(mktemp -d)" || return 1
  (
    trap 'rm -rf "$build_dir"' EXIT
    go build -o "$build_dir/cli-proxy-api" ./cmd/server
  )
}

check_invariants() {
  # Focused run over the namespaces this fork exists to maintain. A general
  # failure is already covered by the full suite; this fails fast and names
  # the subsystem so the log is readable.
  go test ./internal/responsestools/... -count=1
}

stage "maintenance tooling" python3 ops/tests/test_maintenance.py
stage "gofmt" check_gofmt
stage "go vet" go vet ./...
stage "responses-tools invariants" check_invariants
stage "full test suite" go test ./... -count=1
stage "race-sensitive packages" go test -race -count=1 "${RACE_PACKAGES[@]}"
stage "server build" check_build

printf '\n========================================\n'
if [ "${#failures[@]}" -eq 0 ]; then
  printf 'REGRESSION GATE: PASS\n'
  printf 'This port may be absorbed.\n'
  exit 0
fi

printf 'REGRESSION GATE: FAIL\n'
printf 'Failed stages:\n'
for f in "${failures[@]}"; do
  printf '  - %s\n' "$f"
done
printf '\nDo NOT absorb this port. Fix the failures on our side or drop the\n'
printf 'commit and record the reason in absorbed.md.\n'
exit 1
