#!/usr/bin/env bash
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/common.sh"

force=0
if [[ ${1:-} == --force ]]; then
  force=1
  shift
fi
if [[ $# -ne 0 ]]; then
  echo "usage: $0 [--force]" >&2
  exit 2
fi

gym_require_command tmux
run_dir="$(gym_current_run)"
session="$(gym_tmux_session "$run_dir")"
if ! tmux has-session -t "$session" 2>/dev/null; then
  echo "Gym tmux session is not running: $session" >&2
  exit 1
fi
if [[ -f $run_dir/driven && $force -ne 1 ]]; then
  echo "This run has already been driven. Use --force only if you intentionally want another turn." >&2
  exit 1
fi

wait_for_lane() {
  local lane=$1 target output attempt dead
  target="$(gym_lane_target "$session" "$lane")"
  for ((attempt = 1; attempt <= 30; attempt++)); do
    dead="$(tmux display-message -p -t "$target" '#{pane_dead}')"
    if [[ $dead == 1 ]]; then
      echo "Lane $lane exited before Claude Code became ready." >&2
      return 1
    fi
    output="$(tmux capture-pane -p -t "$target" -S -120 | gym_strip_ansi)"
    if grep -Eiq 'trust (this|the) (folder|project)|do you trust|yes, i trust' <<<"$output"; then
      echo "Lane $lane is waiting for a workspace-trust decision." >&2
      echo "Attach with: tmux attach -t $session" >&2
      echo "After reviewing and accepting it, rerun: $script_dir/drive.sh" >&2
      return 1
    fi
    if grep -Eiq 'Claude Code|What can I help|Try ["“]|Welcome back' <<<"$output"; then
      return 0
    fi
    sleep 1
  done
  echo "Lane $lane did not reach a recognizable Claude Code prompt within 30 seconds." >&2
  echo "Inspect it with: tmux attach -t $session" >&2
  return 1
}

for lane in a b; do
  wait_for_lane "$lane"
done

for lane in a b; do
  target="$(gym_lane_target "$session" "$lane")"
  tmux send-keys -t "$target" -l -- '/effort ultracode'
  tmux send-keys -t "$target" Enter
done

# Let the interactive slash command apply before submitting the test turn.
sleep 3

for lane in a b; do
  lane_upper=${lane^^}
  target="$(gym_lane_target "$session" "$lane")"
  prompt="This is Claude Master gym lane $lane_upper. Do not modify files and do not use the network. Fan out with the Agent tool to exactly two read-only subagents in parallel. Subagent 1 must read go.mod and report the module path and Go version. Subagent 2 must read AGENTS.md and report its first heading plus one repository command. Wait for both. Then reply with GYM_LANE_${lane_upper}_OK, one sentence per subagent result, and the model and effort level you believe this session is using. Do not spawn any additional subagents."
  tmux send-keys -t "$target" -l -- "$prompt"
  tmux send-keys -t "$target" Enter
done

date -u +'%Y-%m-%dT%H:%M:%SZ' >"$run_dir/driven"
chmod 0600 "$run_dir/driven"

echo "Submitted /effort ultracode and the bounded two-subagent prompt to both lanes."
echo "Attach:  tmux attach -t $session"
echo "Inspect: $script_dir/inspect.sh"
