#!/usr/bin/env bash
# ruflo - Claude Code task execution tool for Shadow-Claw
# Reads prompt from stdin or first argument
# Uses claude CLI to execute tasks
set -euo pipefail

PROMPT="${1:-$(cat)}"
if [ -z "$PROMPT" ]; then
    echo "Error: no task description provided" >&2
    exit 1
fi

# Use claude CLI in non-interactive mode
claude -p "$PROMPT" --output-format text 2>/dev/null
