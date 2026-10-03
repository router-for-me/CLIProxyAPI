#!/usr/bin/env bash
set -euo pipefail

update=0
if [[ ${1:-} == --update ]]; then
  update=1
  shift
fi
if [[ $# -ne 0 ]]; then
  echo "usage: $0 [--update]" >&2
  exit 2
fi

if ! command -v claude >/dev/null 2>&1; then
  echo "Claude Code is not installed or not on PATH." >&2
  exit 1
fi
if [[ $update -eq 1 ]]; then
  echo "Explicit update requested; invoking the native Claude Code updater."
  claude update
fi

reported=$(claude --version)
installed=${reported%% *}
if [[ ! $installed =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Could not parse installed Claude Code version: $reported" >&2
  exit 1
fi
echo "Installed: $reported"

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required to check the official latest-version endpoint." >&2
  exit 1
fi
latest=$(curl -fsSL --proto '=https' --proto-redir '=https' https://downloads.claude.ai/claude-code-releases/latest)
if [[ ! $latest =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Official latest-version response was not a semantic version." >&2
  exit 1
fi
echo "Official latest: $latest"
if [[ $installed == "$latest" ]]; then
  echo "Claude Code is current."
  exit 0
fi
echo "Claude Code is not current. Review the change, then run: $0 --update" >&2
exit 1

