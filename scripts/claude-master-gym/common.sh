#!/usr/bin/env bash

# Shared helpers for the manual Claude Master tmux gym.

gym_script_dir() {
  cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd
}

gym_repo_root() {
  cd -- "$(gym_script_dir)/../.." && pwd
}

gym_state_root() {
  if [[ -n ${CLAUDE_MASTER_GYM_STATE_DIR:-} ]]; then
    printf '%s\n' "$CLAUDE_MASTER_GYM_STATE_DIR"
    return
  fi
  printf '%s/claude-master-gym\n' "${XDG_STATE_HOME:-$HOME/.local/state}"
}

gym_current_run() {
  local state_root current_file run_dir
  state_root="$(gym_state_root)"
  current_file="$state_root/current"
  if [[ ! -f $current_file ]]; then
    echo "No Claude Master gym run has been recorded." >&2
    return 1
  fi
  IFS= read -r run_dir <"$current_file"
  case "$run_dir" in
    "$state_root"/run.*) ;;
    *)
      echo "The Claude Master gym state pointer is invalid." >&2
      return 1
      ;;
  esac
  if [[ ! -d $run_dir ]]; then
    echo "The recorded Claude Master gym run no longer exists: $run_dir" >&2
    return 1
  fi
  printf '%s\n' "$run_dir"
}

gym_tmux_session() {
  local run_dir=$1 session_file session
  session_file="$run_dir/tmux-session"
  if [[ ! -f $session_file ]]; then
    echo "The gym run has no tmux session record." >&2
    return 1
  fi
  IFS= read -r session <"$session_file"
  if [[ ! $session =~ ^[A-Za-z0-9_.-]+$ ]]; then
    echo "The recorded tmux session name is invalid." >&2
    return 1
  fi
  printf '%s\n' "$session"
}

gym_require_command() {
  local command_name=$1
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "Required command is not installed or not on PATH: $command_name" >&2
    return 1
  fi
}

gym_lane_target() {
  local session=$1 lane=$2
  printf '%s:lane-%s.0\n' "$session" "$lane"
}

gym_lane_upper() {
  case "$1" in
    a) printf 'A\n' ;;
    b) printf 'B\n' ;;
    *) return 1 ;;
  esac
}

gym_new_session_id() {
  # Session correlation only, not a credential or security capability.
  printf '%04x%04x-%04x-4%03x-%04x-%04x%04x%04x\n' \
    "$RANDOM" "$RANDOM" "$RANDOM" "$((RANDOM & 4095))" \
    "$(((RANDOM & 16383) | 32768))" "$RANDOM" "$RANDOM" "$RANDOM"
}

gym_lane_session_id() {
  local run_dir=$1 lane=$2 session_id
  if [[ ! -f $run_dir/lane-$lane.session-id ]]; then
    echo "The gym lane has no recorded native session ID." >&2
    return 1
  fi
  IFS= read -r session_id <"$run_dir/lane-$lane.session-id"
  if [[ ! $session_id =~ ^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$ ]]; then
    echo "The gym lane's native session ID is invalid." >&2
    return 1
  fi
  printf '%s\n' "$session_id"
}

gym_lane_evidence() {
  local run_dir=$1 lane=$2 session_id transcript bytes
  local -a transcripts=()
  if ! session_id="$(gym_lane_session_id "$run_dir" "$lane")"; then
    printf '%s\n' '{"completion":"unverified","reason":"session-id-unavailable"}'
    return
  fi
  if [[ -d $HOME/.claude/projects ]]; then
    while IFS= read -r transcript; do
      transcripts+=("$transcript")
    done < <(find "$HOME/.claude/projects" -mindepth 2 -maxdepth 2 -type f -name "$session_id.jsonl" -print)
  fi
  if [[ ${#transcripts[@]} -ne 1 ]]; then
    printf '%s\n' '{"completion":"unverified","reason":"transcript-unavailable-or-ambiguous"}'
    return
  fi
  transcript=${transcripts[0]}
  bytes=$(wc -c <"$transcript")
  if [[ $bytes -gt 8388608 ]]; then
    printf '%s\n' '{"completion":"unverified","reason":"transcript-exceeds-inspection-bound"}'
    return
  fi
  # Print aggregate evidence only, never raw transcript records or tool output.
  if ! jq -cs --arg session "$session_id" --arg lane "$(gym_lane_upper "$lane")" \
    -f "$(gym_script_dir)/evidence.jq" "$transcript" 2>/dev/null; then
    printf '%s\n' '{"completion":"unverified","reason":"transcript-incomplete-or-unknown"}'
  fi
}

gym_strip_ansi() {
  if command -v perl >/dev/null 2>&1; then
    perl -pe 's/\e\[[0-?]*[ -\/]*[@-~]//g; s/\e\][^\a]*(?:\a|\e\\)//g'
  else
    sed $'s/\033\\[[0-9;?]*[[:alpha:]]//g'
  fi
}

gym_redact_text() {
  sed -E \
    -e 's/(Authorization:?[[:space:]]*(Bearer|Basic))[[:space:]]+[^[:space:]]+/\1 [REDACTED]/g' \
    -e 's/sk-ant-[A-Za-z0-9_-]+/[REDACTED]/g' \
    -e 's/(access_token|refresh_token|sessionKey|oauth_token)(["=:[:space:]]+)[^,}"[:space:]]+/\1\2[REDACTED]/g'
}
