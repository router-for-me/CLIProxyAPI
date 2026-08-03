#!/usr/bin/env zsh
set -euo pipefail

script_dir="${0:A:h}"
install_dir="${CLAUDE_ROUTER_VSCODE_INSTALL_DIR:-$HOME/bin}"
install_name="${CLAUDE_ROUTER_VSCODE_INSTALL_NAME:-code-claude-router}"

mkdir -p "$install_dir"
cp "$script_dir/code-claude-router.zsh" "$install_dir/$install_name"
chmod +x "$install_dir/$install_name"

print "Installed: $install_dir/$install_name"
print "Run: $install_name ."
print "If $install_dir is not on PATH, run: export PATH=\"$install_dir:\$PATH\""
