#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LDFLAGS="$("${ROOT_DIR}/scripts/version.sh" --ldflags)"

# Install primary binary (name derives from cmd directory: cmd/server -> server)
go install -ldflags "$LDFLAGS" "${ROOT_DIR}/cmd/server"

# Resolve Go bin directory where the binary was installed
BIN_DIR="$(go env GOBIN)"
if [[ -z "${BIN_DIR}" ]]; then
  BIN_DIR="$(go env GOPATH)/bin"
fi

PRIMARY_BIN="${BIN_DIR}/server"
ALIAS1="${BIN_DIR}/CLIProxyAPI"
ALIAS2="${BIN_DIR}/cliproxyapi"

# Create/refresh canonical aliases that users expect
ln -sf "${PRIMARY_BIN}" "${ALIAS1}"
ln -sf "${PRIMARY_BIN}" "${ALIAS2}"

echo "Installed CLIProxyAPI to ${PRIMARY_BIN}"
echo "Aliased CLIProxyAPI -> ${PRIMARY_BIN}"
echo "Aliased cliproxyapi -> ${PRIMARY_BIN}"

# Quick version sanity check
if [[ -x "${PRIMARY_BIN}" ]]; then
  echo "server -version:      $(${PRIMARY_BIN} -version)"
fi
if [[ -x "${ALIAS1}" ]]; then
  echo "CLIProxyAPI -version: $(${ALIAS1} -version)"
fi
if [[ -x "${ALIAS2}" ]]; then
  echo "cliproxyapi -version: $(${ALIAS2} -version)"
fi
