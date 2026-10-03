#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/common.sh"

gym_require_command tmux
run_dir="$(gym_current_run)"
session="$(gym_tmux_session "$run_dir")"

echo "Run: $run_dir"
echo "tmux: $session"
echo "Model: $(<"$run_dir/model")"
if [[ -f $run_dir/driven ]]; then
  echo "Prompt submitted: $(<"$run_dir/driven")"
else
  echo "Prompt submitted: no"
fi
echo
cat "$run_dir/routing.tsv"
echo
if ! tmux has-session -t "$session" 2>/dev/null; then
  echo "Session state: stopped"
  exit 1
fi
echo "Session state: running"
for lane in a b; do
  tmux display-message -p -t "$(gym_lane_target "$session" "$lane")" \
    'lane=#{window_name} dead=#{pane_dead} pid=#{pane_pid} command=#{pane_current_command}'
done
