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

