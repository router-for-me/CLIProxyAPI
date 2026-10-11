#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/common.sh"

gym_require_command tmux
run_dir="$(gym_current_run)"
session="$(gym_tmux_session "$run_dir")"

if tmux has-session -t "$session" 2>/dev/null; then
  tmux kill-session -t "$session"
  echo "Stopped tmux session: $session"
else
  echo "tmux session is already stopped: $session"
fi
echo "Logs were retained at: $run_dir"
