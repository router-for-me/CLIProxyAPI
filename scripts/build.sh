#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LDFLAGS="$("${ROOT_DIR}/scripts/version.sh" --ldflags)"

OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/bin}"
mkdir -p "$OUTPUT_DIR"

go build -ldflags "$LDFLAGS" -o "${OUTPUT_DIR}/cliproxyapi" "${ROOT_DIR}/cmd/server"
echo "Built ${OUTPUT_DIR}/cliproxyapi"
