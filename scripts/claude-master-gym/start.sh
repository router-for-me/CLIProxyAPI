#!/usr/bin/env bash
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/common.sh"

usage() {
  cat >&2 <<EOF
usage: $0 [--config FILE] [LANE_A_PROFILE LANE_B_PROFILE]

Configure one disjoint profile list per lane either positionally, in the
environment, or in FILE:
  CLAUDE_MASTER_GYM_LANE_A_PROFILES=claude-a[,fallback-a]
  CLAUDE_MASTER_GYM_LANE_B_PROFILES=claude-b[,fallback-b]
EOF
}

config_file=
if [[ ${1:-} == --config ]]; then
  if [[ $# -lt 2 ]]; then
    usage
    exit 2
  fi
  config_file=$2
  shift 2
fi
if [[ -n $config_file ]]; then
  if [[ ! -f $config_file ]]; then
    echo "Gym config does not exist: $config_file" >&2
    exit 1
  fi
  # The config is an explicit user-selected shell environment file. It must not
  # contain credentials; only profile names and the optional remote-control flag.
  # shellcheck disable=SC1090
  source "$config_file"
fi

if [[ $# -eq 2 ]]; then
  if [[ -n ${CLAUDE_MASTER_GYM_LANE_A_PROFILES:-} || -n ${CLAUDE_MASTER_GYM_LANE_B_PROFILES:-} ]]; then
    echo "Choose positional profiles or lane profile environment variables, not both." >&2
    exit 2
  fi
  CLAUDE_MASTER_GYM_LANE_A_PROFILES=$1
  CLAUDE_MASTER_GYM_LANE_B_PROFILES=$2
  shift 2
elif [[ $# -ne 0 ]]; then
  usage
  exit 2
fi

gym_require_command go
gym_require_command claude
gym_require_command tmux

parse_profiles() {
  local value=$1 destination=$2 item
  local -a parsed=()
  IFS=',' read -r -a parsed <<<"$value"
  if [[ ${#parsed[@]} -eq 0 || -z ${parsed[0]} ]]; then
    return 1
  fi
  for item in "${parsed[@]}"; do
    if [[ ! $item =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$ ]]; then
      echo "Invalid profile name in lane list: $item" >&2
      return 1
    fi
  done
  local -n result=$destination
  # shellcheck disable=SC2034 # The nameref writes the caller's requested array.
  result=("${parsed[@]}")
}

lane_a_value=${CLAUDE_MASTER_GYM_LANE_A_PROFILES:-}
lane_b_value=${CLAUDE_MASTER_GYM_LANE_B_PROFILES:-}
if [[ -z $lane_a_value || -z $lane_b_value ]]; then
  profile_root="$HOME/.local/share/claude-master/profiles"
  echo "Two disjoint, normally logged-in Claude Master profiles are required." >&2
  echo "Only profile names are inspected; no credential files are read." >&2
  echo "Available profiles:" >&2
  found=0
  if [[ -d $profile_root ]]; then
    while IFS= read -r profile; do
      printf '  %s\n' "$profile" >&2
      found=1
    done < <(find "$profile_root" -mindepth 1 -maxdepth 1 -type d -exec basename {} \; | LC_ALL=C sort)
  fi
  if [[ $found -eq 0 ]]; then
    echo "  (none)" >&2
  fi
  echo >&2
  echo "Create a second normal subscription login, for example:" >&2
  echo "  go run ./cmd/claude-master login claude-gym-b" >&2
  echo "Then run: $0 PROFILE_A PROFILE_B" >&2
  exit 2
fi

declare -a lane_a_profiles lane_b_profiles
parse_profiles "$lane_a_value" lane_a_profiles
parse_profiles "$lane_b_value" lane_b_profiles

declare -A lane_a_set=()
for profile in "${lane_a_profiles[@]}"; do
  lane_a_set[$profile]=1
done
for profile in "${lane_b_profiles[@]}"; do
  if [[ -n ${lane_a_set[$profile]:-} ]]; then
    echo "Profile '$profile' appears in both lanes. Concurrent launchers require disjoint profile lists because each profile is exclusively locked." >&2
    exit 2
  fi
done

profile_root="$HOME/.local/share/claude-master/profiles"
for profile in "${lane_a_profiles[@]}" "${lane_b_profiles[@]}"; do
  if [[ ! -f $profile_root/$profile/current/profile.json || ! -d $profile_root/$profile/current/auth ]]; then
    echo "Profile '$profile' is not logged in under $profile_root." >&2
    echo "Create it with: go run ./cmd/claude-master login $profile" >&2
    exit 1
  fi
done

# The gym is specifically a bleeding-edge compatibility exercise. This check is
# read-only; updating remains an explicit claude-version.sh --update action.
"$script_dir/claude-version.sh"

session=${CLAUDE_MASTER_GYM_TMUX_SESSION:-claude-master-gym}
if [[ ! $session =~ ^[A-Za-z0-9_.-]+$ ]]; then
  echo "CLAUDE_MASTER_GYM_TMUX_SESSION contains unsupported characters." >&2
  exit 2
fi
if tmux has-session -t "$session" 2>/dev/null; then
  echo "tmux session already exists: $session" >&2
  echo "Inspect or stop it before starting another gym run." >&2
  exit 1
fi

repo_root="$(gym_repo_root)"
state_root="$(gym_state_root)"
mkdir -p -- "$state_root"
chmod 0700 "$state_root"
run_dir="$(mktemp -d "$state_root/run.XXXXXXXX")"
chmod 0700 "$run_dir"

printf '%s\n' "${lane_a_profiles[@]}" >"$run_dir/lane-a.profiles"
printf '%s\n' "${lane_b_profiles[@]}" >"$run_dir/lane-b.profiles"
printf '%s\n' "$session" >"$run_dir/tmux-session"
printf '%s\n' 'claude-sonnet-5-5' >"$run_dir/model"
printf 'lane\tprofiles\tselection-evidence\n' >"$run_dir/routing.tsv"
printf 'A\t%s\t%s\n' "$(IFS=,; echo "${lane_a_profiles[*]}")" "$([[ ${#lane_a_profiles[@]} -eq 1 ]] && echo exact || echo quota-selected-within-lane)" >>"$run_dir/routing.tsv"
printf 'B\t%s\t%s\n' "$(IFS=,; echo "${lane_b_profiles[*]}")" "$([[ ${#lane_b_profiles[@]} -eq 1 ]] && echo exact || echo quota-selected-within-lane)" >>"$run_dir/routing.tsv"
: >"$run_dir/lane-a.pane.log"
: >"$run_dir/lane-b.pane.log"
: >"$run_dir/lane-a.debug.log"
: >"$run_dir/lane-b.debug.log"
chmod 0600 "$run_dir"/*

echo "Building the repository's claude-master launcher..."
(cd -- "$repo_root" && go build -o "$run_dir/claude-master" ./cmd/claude-master)
chmod 0700 "$run_dir/claude-master"
"$run_dir/claude-master" check

shell_quote() {
  local value=${1//\'/\'\\\'\'}
  printf "'%s'" "$value"
}
runner_command="$(shell_quote "$script_dir/run-lane.sh") $(shell_quote "$run_dir")"

tmux new-session -d -s "$session" -n lane-a -c "$repo_root" "$runner_command a"
tmux new-window -d -t "$session" -n lane-b -c "$repo_root" "$runner_command b"
tmux pipe-pane -o -t "$(gym_lane_target "$session" a)" "cat >> $(shell_quote "$run_dir/lane-a.pane.log")"
tmux pipe-pane -o -t "$(gym_lane_target "$session" b)" "cat >> $(shell_quote "$run_dir/lane-b.pane.log")"

printf '%s\n' "$run_dir" >"$state_root/current"
chmod 0600 "$state_root/current"

echo "Started two independent Claude Code sessions in tmux session '$session'."
echo "Native version: $(claude --version)"
echo "Model: claude-sonnet-5-5"
echo "State: $run_dir"

if ! "$script_dir/drive.sh"; then
  echo >&2
  echo "Both lanes remain running so you can resolve the prompt and rerun drive.sh." >&2
  exit 1
fi
