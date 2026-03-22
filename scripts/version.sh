#!/usr/bin/env bash
set -euo pipefail

resolve_version() {
  local desc dirty=0
  if desc=$(git describe --tags --dirty --match "v*" 2>/dev/null); then
    if [[ "$desc" == *-dirty ]]; then
      dirty=1
      desc="${desc%-dirty}"
    fi
    if [[ $dirty -eq 1 ]]; then
      desc="${desc}+dirty"
    fi
    echo "$desc"
    return 0
  fi

  local sha
  sha=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
  local fallback="v0.0.0-dev.${sha}"
  if ! git diff --quiet --ignore-submodules -- 2>/dev/null; then
    fallback="${fallback}+dirty"
  fi
  echo "$fallback"
}

VERSION="$(resolve_version)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo "none")"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

case "${1:-}" in
  --version)
    echo "$VERSION"
    ;;
  --ldflags)
    echo "-X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'"
    ;;
  --json)
    printf '{"version":"%s","commit":"%s","buildDate":"%s"}\n' "$VERSION" "$COMMIT" "$BUILD_DATE"
    ;;
  *)
    echo "VERSION=${VERSION}"
    echo "COMMIT=${COMMIT}"
    echo "BUILD_DATE=${BUILD_DATE}"
    ;;
esac
