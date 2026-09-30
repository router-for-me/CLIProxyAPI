#!/usr/bin/env bash
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/common.sh"

if [[ $# -ne 2 ]]; then
  echo "usage: run-lane.sh RUN_DIR LANE" >&2
  exit 2
fi

run_dir=$1
lane=$2
if [[ $lane != a && $lane != b ]]; then
  echo "lane must be a or b" >&2
  exit 2
fi

profile_file="$run_dir/lane-$lane.profiles"
if [[ ! -f $profile_file ]]; then
  echo "Missing lane profile list: $profile_file" >&2
  exit 1
fi
mapfile -t profiles <"$profile_file"
if [[ ${#profiles[@]} -eq 0 ]]; then
  echo "Lane $lane has no profiles." >&2
  exit 1
fi

launcher_args=(run "${profiles[0]}")
for profile in "${profiles[@]:1}"; do
  launcher_args+=(--next-profile "$profile")
done
launcher_args+=(--diagnostics --)

native_args=(
  --model claude-sonnet-5-5
  --name "claude-master-gym-$lane"
  --ax-screen-reader
  --debug-file "$run_dir/lane-$lane.debug.log"
)
if [[ ${CLAUDE_MASTER_GYM_REMOTE_CONTROL:-0} == 1 ]]; then
  native_args+=("--remote-control=claude-master-gym-$lane")
fi

cd -- "$(gym_repo_root)"
exec "$run_dir/claude-master" "${launcher_args[@]}" "${native_args[@]}"
