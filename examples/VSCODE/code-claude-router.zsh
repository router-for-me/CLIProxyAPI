#!/usr/bin/env zsh
set -euo pipefail

# Launch VS Code / VS Code Insiders with an isolated user-data-dir whose
# Claude Code extension uses CLIProxyAPI. Normal VS Code settings and normal
# Claude subscription login remain untouched.

script_dir="${0:A:h}"
repo_root="${CLI_PROXY_API_REPO:-${script_dir:h:h}}"

router_config="${CLAUDE_CODE_ROUTER_CONFIG:-$repo_root/config.yaml}"
router_url="${CLAUDE_CODE_ROUTER_URL:-http://127.0.0.1:8317}"
profile_name="${CLAUDE_ROUTER_VSCODE_PROFILE:-Claude Router}"

# Select which VS Code CLI to launch. Defaults to Insiders when available,
# otherwise stable Code.
if [[ -n "${CLAUDE_ROUTER_VSCODE_CLI:-}" ]]; then
  vscode_cli="$CLAUDE_ROUTER_VSCODE_CLI"
elif command -v code-insiders >/dev/null 2>&1; then
  vscode_cli="code-insiders"
elif command -v code >/dev/null 2>&1; then
  vscode_cli="code"
else
  print -u2 "Could not find 'code-insiders' or 'code' on PATH."
  print -u2 "Install the VS Code shell command, or set CLAUDE_ROUTER_VSCODE_CLI=/path/to/code."
  exit 1
fi

case "${vscode_cli:t}" in
  code-insiders)
    default_user_data_dir="$HOME/.vscode-claude-router-insiders-user-data"
    default_extensions_dir="$HOME/.vscode-insiders/extensions"
    ;;
  *)
    default_user_data_dir="$HOME/.vscode-claude-router-user-data"
    default_extensions_dir="$HOME/.vscode/extensions"
    ;;
esac

user_data_dir="${CLAUDE_ROUTER_VSCODE_USER_DATA_DIR:-$default_user_data_dir}"
extensions_dir="${CLAUDE_ROUTER_VSCODE_EXTENSIONS_DIR:-$default_extensions_dir}"

if [[ ! -r "$router_config" ]]; then
  print -u2 "Router config not readable: $router_config"
  print -u2 "Set CLAUDE_CODE_ROUTER_CONFIG=/absolute/path/to/config.yaml if needed."
  exit 1
fi

router_token="$(awk '
  /^api-keys:/ { found=1; next }
  found && /^[[:space:]]*-[[:space:]]*/ {
    sub(/^[[:space:]]*-[[:space:]]*/, "")
    gsub(/^[\047\"]|[\047\"]$/, "")
    print
    exit
  }
' "$router_config")"

if [[ -z "$router_token" ]]; then
  print -u2 "Could not read the first api-keys entry from $router_config"
  print -u2 "Alternatively set CLAUDE_CODE_ROUTER_CONFIG to a config with api-keys."
  exit 1
fi

mkdir -p "$user_data_dir/User"

python3 - "$user_data_dir/User/settings.json" "$router_url" "$router_token" <<'PY'
import json
import os
import sys

path, router_url, router_token = sys.argv[1:4]

def env(name, default):
    return os.environ.get(name, default)

settings = {
    "claudeCode.disableLoginPrompt": True,
    "claudeCode.preferredLocation": env("CLAUDE_ROUTER_VSCODE_LOCATION", "panel"),
    "claudeCode.environmentVariables": [
        {"name": "ANTHROPIC_BASE_URL", "value": router_url},
        {"name": "ANTHROPIC_AUTH_TOKEN", "value": router_token},
        {"name": "ANTHROPIC_MODEL", "value": env("CLAUDE_ROUTER_MAIN_MODEL", "claude-opus-5")},
        {"name": "ANTHROPIC_DEFAULT_OPUS_MODEL", "value": env("CLAUDE_ROUTER_OPUS_MODEL", "claude-opus-5")},
        {"name": "ANTHROPIC_DEFAULT_SONNET_MODEL", "value": env("CLAUDE_ROUTER_SONNET_MODEL", "claude-sonnet-5")},
        {"name": "ANTHROPIC_DEFAULT_HAIKU_MODEL", "value": env("CLAUDE_ROUTER_HAIKU_MODEL", "claude-sonnet-5")},
        {"name": "ANTHROPIC_DEFAULT_FABLE_MODEL", "value": env("CLAUDE_ROUTER_FABLE_MODEL", "claude-fable-5")},
        {"name": "ANTHROPIC_SMALL_FAST_MODEL", "value": env("CLAUDE_ROUTER_SMALL_FAST_MODEL", "claude-sonnet-5")},
        {"name": "CLAUDE_CODE_SUBAGENT_MODEL", "value": env("CLAUDE_ROUTER_SUBAGENT_MODEL", "claude-opus-5")},
        {"name": "CLAUDE_CODE_AUTO_COMPACT_WINDOW", "value": env("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "262144")},
        {"name": "CLAUDE_CODE_EFFORT_LEVEL", "value": env("CLAUDE_CODE_EFFORT_LEVEL", "max")},
    ],
}

with open(path, "w", encoding="utf-8") as f:
    json.dump(settings, f, indent=2)
    f.write("\n")
PY

if command -v curl >/dev/null 2>&1; then
  if ! curl -fsS -m 2 -H "Authorization: Bearer $router_token" "$router_url/v1/models" >/dev/null; then
    print -u2 "Warning: CLIProxyAPI did not answer at $router_url/v1/models."
    print -u2 "Start CLIProxyAPI before using Claude Code in this VS Code window."
  fi
fi

exec "$vscode_cli" \
  --user-data-dir "$user_data_dir" \
  --extensions-dir "$extensions_dir" \
  --profile "$profile_name" \
  "$@"
