#!/usr/bin/env bash
#
# Detect upstream release tags that leaked into the local refs/tags/* namespace.
#
# Fork policy keeps upstream tags in the local-only refs/upstream/tags/* namespace
# so they can never be pushed to origin. Nothing enforces that by itself: a plain
# `git fetch upstream --tags` copies them into refs/tags/*, and a later
# `git push --tags` would then publish every one of them from this fork.
#
# This script never proposes deleting a tag that origin also carries. This fork
# numbers its releases independently, so a name such as v1.0.0 can exist on both
# sides pointing at different commits; only the upstream copy is a leak.
#
# Detection is offline by default and uses refs/upstream/tags/* as the reference
# set. Pass --remote to compare against the upstream repository over the network.
#
# Usage: ops/upstream-intake/check-tag-namespace.sh [--remote] [upstream-repo]

set -euo pipefail

UPSTREAM_REPO="router-for-me/CLIProxyAPI"
check_remote=0
MAX_LISTED=20

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

for arg in "$@"; do
  case "$arg" in
    --remote) check_remote=1 ;;
    -*) die "unknown option: $arg" ;;
    *) UPSTREAM_REPO="$arg" ;;
  esac
done

git rev-parse --git-dir >/dev/null 2>&1 || die "not inside a git repository"

# The durable fix is a fetch-level one. Without it, any plain `git fetch upstream`
# re-imports every upstream tag into refs/tags/* and the cleanup has to be redone.
if git remote get-url upstream >/dev/null 2>&1; then
  tag_opt="$(git config --get remote.upstream.tagOpt || echo "")"
  if [ "$tag_opt" != "--no-tags" ]; then
    cat >&2 <<'WARN'
warning: remote.upstream.tagOpt is not set to --no-tags.

  A plain `git fetch upstream` will copy upstream tags into refs/tags/* again.
  Prevent it once with:

    git config remote.upstream.tagOpt --no-tags

WARN
  fi
fi

# Our own published tags as "commit tag" pairs. origin is authoritative here:
# a tag we published is never a leak, even when upstream reuses the same name.
# For an annotated tag ls-remote emits the tag object plus a peeled "^{}" line
# carrying the commit; the peeled entry is the one comparable with a local
# "refs/tags/<t>^{commit}" lookup, so it wins.
ours="$(git ls-remote --tags origin 2>/dev/null |
  awk '{
      ref = $2
      sub(/^.*refs\/tags\//, "", ref)
      peeled = (ref ~ /\^\{\}$/)
      sub(/\^\{\}$/, "", ref)
      if (ref == "") next
      if (peeled) { print $1 " " ref; seen[ref] = 1 }
      else if (!(ref in seen)) pending[ref] = $1
    }
    END { for (t in pending) if (!(t in seen)) print pending[t] " " t }' || true)"

local_tags="$(git tag --list 'v[0-9]*' 2>/dev/null | sort -V)"
[ -n "$local_tags" ] || {
  printf 'No version tags in refs/tags/*; nothing to check.\n'
  exit 0
}

if [ "$check_remote" -eq 1 ]; then
  command -v gh >/dev/null 2>&1 || die "gh is required for --remote"
  reference="$(gh api "repos/$UPSTREAM_REPO/releases?per_page=100" \
    --paginate --jq '.[].tag_name' 2>/dev/null || true)"
  source_desc="$UPSTREAM_REPO (remote)"
else
  reference="$(git for-each-ref --format='%(refname:short)' refs/upstream/tags/ 2>/dev/null |
    sed 's|^upstream/tags/||')"
  source_desc="refs/upstream/tags/* (local; pass --remote to cover all upstream releases)"
fi

[ -n "$reference" ] || {
  printf 'No upstream reference tags available from %s; nothing to compare.\n' "$source_desc"
  exit 0
}

misplaced=""
leak_count=0
skipped_own=0
for tag in $local_tags; do
  printf '%s\n' "$reference" | grep -qxF "$tag" || continue

  local_sha="$(git rev-parse "refs/tags/$tag^{commit}" 2>/dev/null || echo "")"
  if [ -n "$ours" ] && printf '%s\n' "$ours" | grep -Fxq "$local_sha $tag"; then
    skipped_own=$((skipped_own + 1))
    continue
  fi

  misplaced="$misplaced$tag
"
  leak_count=$((leak_count + 1))
done

if [ "$skipped_own" -gt 0 ]; then
  printf 'Left %d tag(s) alone because origin publishes them too; a shared name is not a leak.\n' \
    "$skipped_own"
fi

if [ "$leak_count" -eq 0 ]; then
  printf 'refs/tags/* holds no upstream tags (checked against %s).\n' "$source_desc"
  exit 0
fi

{
  printf 'error: %d upstream tag(s) leaked into refs/tags/* (checked against %s):\n' \
    "$leak_count" "$source_desc"
  printf '%s' "$misplaced" | head -n "$MAX_LISTED" | sed 's/^/  /'
  if [ "$leak_count" -gt "$MAX_LISTED" ]; then
    printf '  ... and %d more\n' "$((leak_count - MAX_LISTED))"
  fi
} >&2

cat >&2 <<'EOF'

These arrived through the shared fork history or a fetch that wrote to
refs/tags/*. Move each one into the local-only namespace:

  for tag in v1.2.3 v1.2.4; do
    git tag -d "$tag" >/dev/null
    git fetch --no-tags upstream "refs/tags/$tag:refs/upstream/tags/$tag"
  done

Fill in the real tag names from the list above and skip any tag this fork also
publishes. Never push upstream tags with --tags or --mirror; release-time
protection lives in ops/upstream-intake/guard-release-tag.sh.
EOF
exit 1
