#!/usr/bin/env bash
# Builds the bundled Antigravity guard plugin for the current GOOS/GOARCH into
# <output-dir>. The release archives and the Docker image both call this, so
# the pinned plugin source is defined only here. Cross builds pass GOOS, GOARCH,
# CC and CGO_LDFLAGS through the environment.
set -euo pipefail

repo="${GUARD_PLUGIN_REPO:-https://github.com/zhyx111999/cpa-plugin-antigravity-coding-filter}"
ref="${GUARD_PLUGIN_REF:-7b229dddf09a8f3244fb86ac26da4ee70fb1d926}"
out_dir="${1:?usage: build-guard-plugin.sh <output-dir>}"

case "$(go env GOOS)" in
  darwin) extension=dylib ;;
  windows) extension=dll ;;
  *) extension=so ;;
esac

mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
src_dir="$(mktemp -d)"
trap 'rm -rf "$src_dir"' EXIT

curl -fsSL --retry 3 "${repo}/archive/${ref}.tar.gz" | tar -xz -C "$src_dir" --strip-components=1
(
  cd "$src_dir"
  CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -ldflags="-s -w" \
    -o "$out_dir/antigravity-coding-filter.$extension" .
)
rm -f "$out_dir/antigravity-coding-filter.h"
