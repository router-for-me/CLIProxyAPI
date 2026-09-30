#!/usr/bin/env bash
set -euo pipefail

# Profiles are used in this order. Add or remove names as needed.
profiles=(
  "claude-primary"
  "claude-secondary"
  # "claude-tertiary"
)

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$repo_dir"

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required to build claude-master." >&2
  exit 1
fi

build_dir="$(mktemp -d)"
readonly build_dir
cleanup() {
  rm -rf -- "$build_dir"
}
trap cleanup EXIT

binary="$build_dir/claude-master"
go build -o "$binary" ./cmd/claude-master
"$binary" check

profile_root="$HOME/.local/share/claude-master/profiles"
for profile in "${profiles[@]}"; do
  if [[ -d "$profile_root/$profile/current" ]]; then
    echo "Using existing profile: $profile"
    continue
  fi

  echo
  echo "Logging into profile: $profile"
  echo "Choose that subscription in the Anthropic browser login."
  read -r -p "Press Enter to begin..."
  "$binary" login "$profile"
done

run_args=("${profiles[0]}")
for profile in "${profiles[@]:1}"; do
  run_args+=(--next-profile "$profile")
done

claude_args=("$@")
if [[ ${#claude_args[@]} -eq 0 ]]; then
  claude_args=(--remote-control)
fi

"$binary" run "${run_args[@]}" -- "${claude_args[@]}"
