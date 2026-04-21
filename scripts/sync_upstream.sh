#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
ORIGIN_REMOTE="${ORIGIN_REMOTE:-origin}"
BRANCH="${BRANCH:-main}"
PUSH="${PUSH:-1}"

if ! git remote get-url "$UPSTREAM_REMOTE" >/dev/null 2>&1; then
  echo "missing upstream remote: $UPSTREAM_REMOTE" >&2
  exit 1
fi

if ! git remote get-url "$ORIGIN_REMOTE" >/dev/null 2>&1; then
  echo "missing origin remote: $ORIGIN_REMOTE" >&2
  exit 1
fi

current_branch="$(git branch --show-current)"
if [[ "$current_branch" != "$BRANCH" ]]; then
  echo "switching to $BRANCH"
  git checkout "$BRANCH"
fi

echo "fetching remotes..."
git fetch "$UPSTREAM_REMOTE"
git fetch "$ORIGIN_REMOTE"

echo "merging $UPSTREAM_REMOTE/$BRANCH into $BRANCH ..."
git merge --no-edit "$UPSTREAM_REMOTE/$BRANCH"

if [[ "$PUSH" == "1" ]]; then
  echo "pushing to $ORIGIN_REMOTE/$BRANCH ..."
  git push "$ORIGIN_REMOTE" "$BRANCH"
else
  echo "PUSH=0 -> skipped push"
fi

echo
echo "done"
echo "HEAD: $(git rev-parse --short HEAD)"
echo "upstream/$BRANCH: $(git rev-parse --short "$UPSTREAM_REMOTE/$BRANCH")"
