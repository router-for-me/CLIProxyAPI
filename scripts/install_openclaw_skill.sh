#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SRC_DIR="$ROOT_DIR/skills/cliproxyapi-autonomy"

if [[ ! -d "$SRC_DIR" ]]; then
  echo "skill source not found: $SRC_DIR" >&2
  exit 1
fi

TARGET=""
if [[ "${1:-}" == "--shared" ]]; then
  TARGET="$HOME/.openclaw/skills/cliproxyapi-autonomy"
elif [[ "${1:-}" == "--workspace" && -n "${2:-}" ]]; then
  TARGET="$2/skills/cliproxyapi-autonomy"
else
  echo "usage:" >&2
  echo "  $0 --shared" >&2
  echo "  $0 --workspace /path/to/openclaw/workspace" >&2
  exit 1
fi

mkdir -p "$(dirname "$TARGET")"
rm -rf "$TARGET"
cp -R "$SRC_DIR" "$TARGET"

echo "installed -> $TARGET"
