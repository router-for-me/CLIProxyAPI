#!/usr/bin/env bash
# Build the management control panel and stage it for the backend.
# The backend serves ./static/management.html (internal/managementasset) in
# preference to the GitHub-downloaded asset, so staging the freshly built
# single-file bundle here makes the server use the in-repo panel.
set -euo pipefail
cd "$(dirname "$0")"

bun install --frozen-lockfile
bun run build

mkdir -p ../static
cp dist/index.html ../static/management.html
echo "Staged $(wc -c <../static/management.html) bytes into ../static/management.html"
