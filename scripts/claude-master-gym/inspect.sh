#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/common.sh"

gym_require_command tmux
gym_require_command jq
run_dir="$(gym_current_run)"
session="$(gym_tmux_session "$run_dir")"

echo "Routing boundary (profile names only; credential files are never read):"
column -t -s $'\t' "$run_dir/routing.tsv" 2>/dev/null || cat "$run_dir/routing.tsv"

for lane in a b; do
  echo
  echo "===== lane $lane ====="
  debug_file="$run_dir/lane-$lane.debug.log"
  pane_log="$run_dir/lane-$lane.pane.log"
  debug_bytes=$(wc -c <"$debug_file" | tr -d ' ')
  debug_lines=$(wc -l <"$debug_file" | tr -d ' ')
  stream_markers=$(grep -Eic 'stream|content_block_delta|message_delta' "$debug_file" 2>/dev/null || true)
  agent_markers=$(grep -Eic 'subagent|agent tool|parent_tool_use|agent_id' "$debug_file" 2>/dev/null || true)
  compact_markers=$(grep -Eic 'compact|context window' "$debug_file" 2>/dev/null || true)
  printf 'debug evidence: bytes=%s lines=%s stream-markers=%s agent-markers=%s compaction-markers=%s\n' \
    "$debug_bytes" "$debug_lines" "$stream_markers" "$agent_markers" "$compact_markers"
  if tmux has-session -t "$session" 2>/dev/null; then
    terminal_text="$(tmux capture-pane -p -t "$(gym_lane_target "$session" "$lane")" -S -200 | gym_strip_ansi | gym_redact_text)"
  else
    terminal_text="$(tail -n 200 "$pane_log" | gym_strip_ansi | gym_redact_text)"
  fi
  diagnostic_markers=$(grep -Eic '"InferenceRequests"[[:space:]]*:[[:space:]]*[1-9][0-9]*' <<<"$terminal_text" || true)
  echo "Native transcript evidence (assistant output and matched agent results only):"
  gym_lane_evidence "$run_dir" "$lane"
  printf 'terminal diagnostics (advisory only): positive-inference-snapshots=%s\n' "$diagnostic_markers"
  echo "recent terminal evidence (sanitized):"
  tail -n 120 <<<"$terminal_text"
done

echo
echo "Raw native debug logs are intentionally not printed because they may contain conversation data."
echo "Effort needs manual verification of Claude's /effort acknowledgement; assistant claims are not evidence."
echo "Private run artifacts: $run_dir"
