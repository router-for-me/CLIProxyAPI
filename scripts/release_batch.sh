#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/release_batch.sh [--hotfix] [--target <branch>] [--dry-run]

Creates and publishes a GitHub release using the repo's existing tag pattern:
  v<major>.<minor>.<patch>-<batch>

Rules:
  - Default mode (no --hotfix): bump patch, reset batch to 0.
  - --hotfix mode: keep patch, increment batch suffix.

Examples:
  scripts/release_batch.sh
  scripts/release_batch.sh --hotfix
  scripts/release_batch.sh --target main --dry-run
EOF
}

hotfix=0
target_branch="main"
dry_run=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --hotfix)
      hotfix=1
      shift
      ;;
    --target)
      target_branch="${2:-}"
      if [[ -z "$target_branch" ]]; then
        echo "error: --target requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -n "$(git status --porcelain)" ]]; then
  echo "error: working tree is not clean; commit/stash before release" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh CLI is required" >&2
  exit 1
fi

git fetch origin "$target_branch" --quiet
git fetch --tags origin --quiet

if ! git show-ref --verify --quiet "refs/remotes/origin/${target_branch}"; then
  echo "error: target branch origin/${target_branch} not found" >&2
  exit 1
fi

latest_tag="$(git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$' | sort -V | tail -n 1)"
if [[ -z "$latest_tag" ]]; then
  echo "error: no existing release tags matching v<semver>-<batch>" >&2
  exit 1
fi

version="${latest_tag#v}"
base="${version%-*}"
batch="${version##*-}"
major="${base%%.*}"
minor_patch="${base#*.}"
minor="${minor_patch%%.*}"
patch="${base##*.}"

if [[ "$hotfix" -eq 1 ]]; then
  next_patch="$patch"
  next_batch="$((batch + 1))"
else
  next_patch="$((patch + 1))"
  next_batch=0
fi

next_tag="v${major}.${minor}.${next_patch}-${next_batch}"

range="${latest_tag}..origin/${target_branch}"
mapfile -t commits < <(git log --pretty='%H %s' "$range")
if [[ "${#commits[@]}" -eq 0 ]]; then
  echo "error: no commits found in ${range}" >&2
  exit 1
fi

notes_file="$(mktemp)"
{
  echo "## Changelog"
  for line in "${commits[@]}"; do
    echo "* ${line}"
  done
  echo
} > "$notes_file"

echo "latest tag : $latest_tag"
echo "next tag   : $next_tag"
echo "target     : origin/${target_branch}"
echo "commits    : ${#commits[@]}"

if [[ "$dry_run" -eq 1 ]]; then
  echo
  echo "--- release notes preview ---"
  cat "$notes_file"
  rm -f "$notes_file"
  exit 0
fi

git tag -a "$next_tag" "origin/${target_branch}" -m "$next_tag"
git push origin "$next_tag"
gh release create "$next_tag" \
  --title "$next_tag" \
  --target "$target_branch" \
  --notes-file "$notes_file"

rm -f "$notes_file"
echo "release published: $next_tag"
