#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

echo "==> release-lint: config example and compatibility tests"
go test ./pkg/llmproxy/config -run 'TestLoadConfig|TestMigrateOAuthModelAlias|TestConfig_Validate'

if ! command -v python3 >/dev/null 2>&1; then
  echo "[SKIP] python3 not available for markdown snippet parsing"
  exit 0
fi

echo "==> release-lint: markdown yaml/json snippet parse"
python3 - "$@" <<'PY'
import re
import sys
from pathlib import Path

import json
import yaml


repo_root = Path.cwd()
docs_root = repo_root / "docs"
md_roots = [repo_root / "README.md", repo_root / "README_CN.md", docs_root]
skip_markers = [
    "${",
    "{{",
    "<YOUR_",
    "your_",
    "your-",
    "[REDACTED",
]

fence_pattern = re.compile(r"```([\\w-]+)\s*\n(.*?)\n```", re.S)
supported_languages = {
    "json": "json",
    "jsonc": "json",
    "yaml": "yaml",
    "yml": "yaml",
}


def gather_files() -> list[Path]:
    files: list[Path] = []
    for path in md_roots:
        if path.is_file():
            files.append(path)
    if docs_root.is_dir():
        files.extend(sorted(p for p in docs_root.rglob("*.md") if p.is_file()))
    return files


def should_skip(text: str) -> bool:
    return any(marker in text for marker in skip_markers) or "${" in text


def is_parseable_json(block: str) -> bool:
    stripped = []
    for line in block.splitlines():
        line = line.strip()
        if not line or line.startswith("//"):
            continue
        stripped.append(line)
    payload = "\n".join(stripped)
    payload = re.sub(r",\s*([}\]])", r"\1", payload)
    json.loads(payload)
    return True


def is_parseable_yaml(block: str) -> bool:
    yaml.safe_load(block)
    return True


failed: list[str] = []
for file in gather_files():
    text = file.read_text(encoding="utf-8", errors="replace")
    for match in fence_pattern.finditer(text):
        lang = match.group(1).lower()
        snippet = match.group(2).strip()
        if not snippet:
            continue
        parser = supported_languages.get(lang)
        if not parser:
            continue
        if should_skip(snippet):
            continue
        try:
            if parser == "json":
                is_parseable_json(snippet)
            else:
                is_parseable_yaml(snippet)
        except Exception as error:
            failed.append(f"{file}:{match.start(0)}::{lang}::{error}")

if failed:
    print("release-lint: markdown snippet parse failed:")
    for item in failed:
        print(f"- {item}")
    sys.exit(1)

print("release-lint: markdown snippet parse passed")
PY
