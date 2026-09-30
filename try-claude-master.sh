#!/usr/bin/env bash
set -euo pipefail
umask 077

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

native_version="$(sed -n 's/^const NativeClaudeVersion = "\([0-9.]*\)"$/\1/p' internal/claudemaster/launcher.go)"
if [[ ! "$native_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Could not determine the reviewed Claude Code version." >&2
  exit 1
fi

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) native_platform="linux-x64" ;;
  Linux-aarch64) native_platform="linux-arm64" ;;
  Darwin-arm64) native_platform="darwin-arm64" ;;
  Darwin-x86_64) native_platform="darwin-x64" ;;
  *)
    echo "This host does not have a reviewed Claude Code download." >&2
    exit 1
    ;;
esac

cache_home="${XDG_CACHE_HOME:-$HOME/.cache}"
if [[ "$cache_home" != /* ]]; then
  echo "XDG_CACHE_HOME must be an absolute path." >&2
  exit 1
fi
native_dir="$cache_home/claude-master/native/$native_version/$native_platform"
native_path="$native_dir/claude"

native_is_reviewed() {
  local candidate="$1"
  local reported

  [[ -x "$candidate" ]] || return 1
  reported="$(DISABLE_AUTOUPDATER=1 "$candidate" --version 2>/dev/null)" || return 1
  [[ "$reported" == "$native_version" || "$reported" == "$native_version (Claude Code)" ]]
}

install_reviewed_native() {
  local command_name
  for command_name in curl jq; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
      echo "$command_name is required to install reviewed Claude Code $native_version." >&2
      exit 1
    fi
  done
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    echo "sha256sum or shasum is required to verify reviewed Claude Code." >&2
    exit 1
  fi

  local download_dir="$build_dir/native-download"
  local native_base="https://downloads.claude.ai/claude-code-releases/$native_version"
  local expected_checksum
  local actual_checksum
  local reported
  mkdir -p "$download_dir"

  echo "Caching reviewed Claude Code $native_version for claude-master."
  echo "Your normal claude command will not be changed."
  curl -fsSL --proto '=https' --proto-redir '=https' \
    -o "$download_dir/manifest.json" "$native_base/manifest.json"
  expected_checksum="$(jq -er --arg version "$native_version" --arg platform "$native_platform" \
    'select(.version == $version) | .platforms[$platform].checksum | select(test("^[a-f0-9]{64}$"))' \
    "$download_dir/manifest.json")"
  curl -fL --progress-bar --proto '=https' --proto-redir '=https' \
    -o "$download_dir/claude" "$native_base/$native_platform/claude"

  if command -v sha256sum >/dev/null 2>&1; then
    actual_checksum="$(sha256sum "$download_dir/claude" | awk '{print $1}')"
  else
    actual_checksum="$(shasum -a 256 "$download_dir/claude" | awk '{print $1}')"
  fi
  if [[ "$actual_checksum" != "$expected_checksum" ]]; then
    echo "The reviewed Claude Code download failed checksum verification." >&2
    exit 1
  fi

  chmod 0700 "$download_dir/claude"
  reported="$(DISABLE_AUTOUPDATER=1 "$download_dir/claude" --version)"
  if [[ "$reported" != "$native_version" && "$reported" != "$native_version (Claude Code)" ]]; then
    echo "The reviewed Claude Code download reported an unexpected version." >&2
    exit 1
  fi

  mkdir -p "$native_dir"
  install -m 0700 "$download_dir/claude" "$native_path"
}

build_dir="$(mktemp -d)"
readonly build_dir
cleanup() {
  rm -rf -- "$build_dir"
}
trap cleanup EXIT

binary="$build_dir/claude-master"
go build -o "$binary" ./cmd/claude-master

if ! native_is_reviewed "$native_path"; then
  install_reviewed_native
fi
export PATH="$native_dir:$PATH"
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
