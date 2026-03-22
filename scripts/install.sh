#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LDFLAGS="$("${ROOT_DIR}/scripts/version.sh" --ldflags)"

go install -ldflags "$LDFLAGS" "${ROOT_DIR}/cmd/server"
echo "Installed CLIProxyAPI server to GOPATH/bin or GOBIN"
