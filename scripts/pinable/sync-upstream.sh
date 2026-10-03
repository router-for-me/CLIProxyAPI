#!/usr/bin/env bash
# Prepare a verified upstream merge without resetting history or pushing any branch.
set -euo pipefail

die() { printf 'sync-upstream: %s\n' "$*" >&2; exit 1; }
[ "$#" -le 1 ] || die 'Usage: bash scripts/pinable/sync-upstream.sh [ref|--continue|--abort]'
mode=${1:-main}
root=$(git rev-parse --show-toplevel) || die 'Run inside the repository.'
cd "$root"
[ "$(git rev-parse --is-shallow-repository)" = false ] || die 'Fetch full history before syncing.'
branch=$(git symbolic-ref --quiet --short HEAD) || die 'Check out a branch first.'
state=$(git rev-parse --git-path pinable-upstream-sync)
merge_head=$(git rev-parse --git-path MERGE_HEAD)

check_worktree() {
  git diff --quiet || die 'Stage all reviewed resolutions before continuing.'
  [ -z "$(git ls-files --others --exclude-standard)" ] || die 'Commit, move, or remove untracked files first.'
}

if [ "$mode" = --continue ] || [ "$mode" = --abort ]; then
  [ -f "$state" ] && [ -f "$merge_head" ] || die 'No pending merge started by this script.'
  target=$(sed -n '1p' "$state")
  original=$(sed -n '2p' "$state")
  expected_branch=$(sed -n '3p' "$state")
  [ "$branch" = "$expected_branch" ] || die 'The synchronization branch has changed.'
  [ "$(git rev-parse HEAD)" = "$original" ] || die 'HEAD changed during synchronization.'
  [ "$(cat "$merge_head")" = "$target" ] || die 'The merge target does not match the recorded upstream revision.'
  if [ "$mode" = --abort ]; then
    git merge --abort
    rm -- "$state"
    exit 0
  fi
else
  case "$mode" in -*) die 'Unknown option.';; esac
  git check-ref-format "refs/heads/$mode" >/dev/null || die 'Use a branch, tag, or full commit SHA without a refspec.'
  [ ! -f "$merge_head" ] || die 'Finish or abort the existing merge first.'
  [ -z "$(git status --porcelain)" ] || die 'Start with a clean worktree and index.'
  git var GIT_AUTHOR_IDENT >/dev/null
  if ! git remote get-url upstream >/dev/null 2>&1; then
    git remote add upstream https://github.com/router-for-me/CLIProxyAPI.git
  fi
  printf 'Fetching from %s\n' "$(git remote get-url upstream)"
  git fetch --no-tags upstream "$mode"
  target=$(git rev-parse 'FETCH_HEAD^{commit}')
  if git merge-base --is-ancestor "$target" HEAD; then
    printf 'Already contains upstream %s; nothing to merge.\n' "$target"
    exit 0
  fi
  git merge-base HEAD "$target" >/dev/null || die 'Refusing unrelated histories.'
  git config --local rerere.enabled true
  git config --local rerere.autoupdate false
  original=$(git rev-parse HEAD)
  branch="sync/upstream-$(date -u +%Y%m%d)-${target:0:12}"
  git show-ref --verify --quiet "refs/heads/$branch" && die "Branch $branch already exists; inspect it instead of overwriting it."
  git switch -c "$branch"
  printf '%s\n%s\n%s\n' "$target" "$original" "$branch" > "$state"
  if ! git merge --no-ff --no-commit "$target"; then
    printf 'Merge stopped. Review every conflict (including rerere suggestions), stage resolutions, then run --continue.\n' >&2
    git status --short
    exit 1
  fi
fi

[ -z "$(git ls-files -u)" ] || die 'Unresolved conflicts remain; no automatic ours/theirs selection is used.'
check_worktree
git diff --cached --check
build_dir=$(mktemp -d)
trap 'rm -rf -- "$build_dir"' EXIT
go build -o "$build_dir/cli-proxy-api" ./cmd/server
go test ./...
check_worktree
git diff --cached --check
git commit -m "Merge upstream ${target:0:12} with Pinable host integration preserved"
git merge-base --is-ancestor "$target" HEAD
rm -- "$state"
printf '\nVerified merge on %s. Review and push this branch, then merge its PR with a merge commit (not squash/rebase).\n' "$branch"
