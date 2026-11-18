#!/usr/bin/env bash
# Build the management console and copy it where the Go binary embeds it.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/console"
bun install --frozen-lockfile
bun run build
cp dist/index.html "$root/internal/managementasset/panel/management.html"
echo "embedded console updated: internal/managementasset/panel/management.html"
