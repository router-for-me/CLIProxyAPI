#!/usr/bin/env bash
set -euo pipefail
umask 077

# Profiles are used in this order. Add or remove names as needed.
# Optional: export CLAW_MASTER_BACKUP_API_KEY for final paid API-key backup.
# The launcher consumes it without changing these normal subscription logins.
profiles=(
  "claude-primary"
  "claude-secondary"
  # "claude-tertiary"
)

# Extract only launcher options; all other arguments retain native Claude semantics.
launcher_args=()
claude_args=()
backup_source=""
backup_requested=0
backup_index=-1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --backup-api-key|--map)
      if [[ $# -lt 2 ]]; then
        echo "Launcher option requires a value." >&2
        exit 2
      fi
      if [[ "$1" == --backup-api-key ]]; then
        backup_index=${#launcher_args[@]}
        backup_source="$2"
        backup_requested=1
      fi
      launcher_args+=("$1" "$2")
      shift 2
      ;;
    --backup-api-key=*)
      backup_source="${1#--backup-api-key=}"
      backup_requested=1
      backup_index=${#launcher_args[@]}
      launcher_args+=(--backup-api-key "$backup_source")
      shift
      ;;
    --map=*)
      launcher_args+=("$1")
      shift
      ;;
    --)
      shift
      claude_args+=("$@")
      break
      ;;
    *)
      claude_args+=("$1")
      shift
      ;;
  esac
done

# Checks and normal OAuth login helpers have no need for the backup credential.
preflight_env=(env -u CLAW_MASTER_BACKUP_API_KEY)
if [[ "$backup_requested" == 1 && -z "$backup_source" ]]; then
  echo "Backup API key requires a file path or env:VARIABLE." >&2
  exit 2
fi
if [[ "$backup_source" == env:* ]]; then
  backup_env="${backup_source#env:}"
  if [[ ! "$backup_env" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || [[ -z "${!backup_env:-}" ]]; then
    echo "Backup API key environment variable must be valid, set, and nonempty." >&2
    exit 2
  fi
  preflight_env+=(-u "$backup_env")
elif [[ -n "$backup_source" ]]; then
  backup_path="${backup_source#file:}"
  if [[ ! -f "$backup_path" || ! -r "$backup_path" || ! -s "$backup_path" ]]; then
    echo "Backup API key file must be readable and nonempty." >&2
    exit 2
  fi
  # Validation and consumption must refer to the same file after cd below.
  if [[ "$backup_path" != /* ]]; then
    backup_path="$PWD/$backup_path"
  fi
  backup_source="file:$backup_path"
fi
if [[ "$backup_requested" == 1 ]]; then
  launcher_args[backup_index + 1]="$backup_source"
fi

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$repo_dir"

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required to build claw-master." >&2
  exit 1
fi
if ! command -v claude >/dev/null 2>&1; then
  echo "Claude Code is required and must be available as 'claude' on PATH." >&2
  exit 1
fi

build_dir="$(mktemp -d)"
readonly build_dir
cleanup() {
  rm -rf -- "$build_dir"
}
trap cleanup EXIT

binary="$build_dir/claw-master"
go build -o "$binary" ./cmd/claw-master
"${preflight_env[@]}" "$binary" check

profile_root="$HOME/.local/share/claw-master/profiles"
# Before the rename the profiles lived under claude-master; claw-master keeps using that directory
# while the new one does not exist, and so does this check.
if [[ ! -e $HOME/.local/share/claw-master && -d $HOME/.local/share/claude-master ]]; then
  profile_root="$HOME/.local/share/claude-master/profiles"
fi
for profile in "${profiles[@]}"; do
  if [[ -d "$profile_root/$profile/current" ]]; then
    echo "Using existing profile: $profile"
    continue
  fi

  echo
  echo "Logging into profile: $profile"
  echo "Choose that subscription in the Anthropic browser login."
  read -r -p "Press Enter to begin..."
  "${preflight_env[@]}" "$binary" login "$profile"
done

run_args=("${profiles[0]}")
for profile in "${profiles[@]:1}"; do
  run_args+=(--next-profile "$profile")
done

if [[ ${#claude_args[@]} -eq 0 ]]; then
  claude_args=(--remote-control)
fi

# Bash 3 treats an empty array expansion as unset under nounset.
if [[ ${#launcher_args[@]} -gt 0 ]]; then
  run_args+=("${launcher_args[@]}")
fi
"$binary" run "${run_args[@]}" -- "${claude_args[@]}"
