#!/bin/bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "==> disk before"
df -h / | tail -1

echo
echo "==> [1/4] clean request logs (keep last 50 files)"
if [[ -d logs ]]; then
  mapfile -t old_logs < <(ls -1t logs/*.log 2>/dev/null | tail -n +51 || true)
  if ((${#old_logs[@]})); then
    rm -f "${old_logs[@]}"
    echo "removed ${#old_logs[@]} old log files"
  else
    echo "nothing to trim in logs/"
  fi
fi

echo
echo "==> [2/4] remove temp artifacts"
rm -rf tmp-applypatch-test tmp-import-progress.txt tmp-import-results.json logs-cache-fail 2>/dev/null || true
echo "temp artifacts removed"

echo
echo "==> [3/4] kill cursor-server and remove install dir"
pkill -9 -f '/root/.cursor-server/' 2>/dev/null || true
sleep 2
rm -rf /root/.cursor-server /run/user/0/cursor-remote-code.token.* 2>/dev/null || true
remaining=$(ps -eo args 2>/dev/null | awk '/\/root\/\.cursor-server\// && !/awk/' | wc -l)
if [[ -d /root/.cursor-server ]]; then
  echo "WARNING: /root/.cursor-server still exists"
else
  echo "cursor-server removed (remaining procs=$remaining)"
fi

echo
echo "==> [4/4] optional: clear cursor crepe index cache"
rm -rf "$REPO_ROOT/.git/cursor" 2>/dev/null || true
echo "crepe index cache cleared"

echo
echo "==> disk after"
df -h / | tail -1
echo
echo "Done. Reconnect Cursor Remote only after this script finishes."
