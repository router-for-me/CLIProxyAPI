#!/usr/bin/env bash
#
# Fetch an upstream release tag locally and classify the commits since the
# last absorbed release. Read-only with respect to the working tree and to
# `origin`; the only ref it writes is refs/upstream/tags/*.
#
# Usage: ops/upstream-intake/assess-release.sh v8.0.6 [previous-tag]

set -euo pipefail

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_REPO="${UPSTREAM_REPO:-router-for-me/CLIProxyAPI}"
TAG_PREFIX="refs/upstream/tags"
# A release must have settled before we port from it, so regressions and
# hurried follow-up fixes have had time to surface upstream.
MIN_AGE_HOURS="${MIN_AGE_HOURS:-72}"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
Usage: assess-release.sh <new-tag> [previous-tag]

  <new-tag>       upstream release tag to assess, e.g. v8.0.6
  [previous-tag]  previous release tag; defaults to the most recent tag that
                  is already present under refs/upstream/tags/
EOF
  exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage

new_tag="$1"
case "$new_tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) die "expected a vX.Y.Z release tag, got '$new_tag'" ;;
esac

git rev-parse --git-dir >/dev/null 2>&1 || die "not inside a git repository"
git remote get-url "$UPSTREAM_REMOTE" >/dev/null 2>&1 ||
  die "remote '$UPSTREAM_REMOTE' not found; it must point at router-for-me/CLIProxyAPI"

# Age gate, checked against the upstream release publish time rather than the
# commit date: a release can be cut from commits that are days old, and what
# matters is how long the published artifact has been in use.
require_aged_release() {
  command -v gh >/dev/null 2>&1 ||
    die "gh is required to read the upstream release date"

  published_at="$(gh release view "$1" --repo "$UPSTREAM_REPO" --json publishedAt \
    --jq '.publishedAt' 2>/dev/null)" ||
    die "upstream release '$1' not found in $UPSTREAM_REPO"

  [ -n "$published_at" ] && [ "$published_at" != "null" ] ||
    die "upstream release '$1' has no publish date; refusing to assess it"

  now="$(date -u +%s)"
  published="$(date -u -d "$published_at" +%s 2>/dev/null)" ||
    published="$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$published_at" +%s)" ||
    die "could not parse upstream publish date '$published_at'"

  age_hours=$(( (now - published) / 3600 ))
  if [ "$age_hours" -lt "$MIN_AGE_HOURS" ]; then
    printf 'error: upstream %s was published %dh ago; we require at least %dh\n' \
      "$1" "$age_hours" "$MIN_AGE_HOURS" >&2
    printf 'Re-run after it has settled to decide whether it introduced a regression.\n' >&2
    exit 3
  fi

  printf 'Upstream %s published %s (%dh old, minimum %dh)\n' \
    "$1" "$published_at" "$age_hours" "$MIN_AGE_HOURS"
}

require_aged_release "$new_tag"

# Never let a stray upstream tag reach origin and publish a release.
if git ls-remote --exit-code --tags origin "refs/tags/$new_tag" >/dev/null 2>&1; then
  die "origin already has tag '$new_tag'; upstream tags must stay local"
fi

printf 'Fetching %s into %s/%s (local only)\n' "$new_tag" "$TAG_PREFIX" "$new_tag"
git fetch --quiet "$UPSTREAM_REMOTE" \
  "refs/tags/$new_tag:$TAG_PREFIX/$new_tag"

new_ref="$TAG_PREFIX/$new_tag"

if [ "$#" -eq 2 ]; then
  prev_ref="$TAG_PREFIX/$2"
  if ! git rev-parse --verify --quiet "$prev_ref" >/dev/null; then
    printf 'Fetching previous tag %s\n' "$2"
    git fetch --quiet "$UPSTREAM_REMOTE" "refs/tags/$2:$prev_ref" ||
      die "previous tag '$2' does not exist upstream"
  fi
else
  # grep exits 1 when it filters out every line, which under `pipefail` would
  # abort the script before the diagnostic below can explain the situation.
  prev_ref="$(git for-each-ref --sort=-creatordate --format='%(refname)' \
    "$TAG_PREFIX/" | { grep -v "^${new_ref}$" || true; } | head -n 1)"
  [ -n "$prev_ref" ] ||
    die "no previous upstream tag under $TAG_PREFIX/ to compare against; pass one explicitly, e.g. $0 $new_tag v8.0.4"
  printf 'Comparing against %s\n' "$prev_ref"
fi

printf '\n== %s..%s ==\n' "${prev_ref##*/}" "${new_tag}"
git log --oneline --no-decorate "$prev_ref..$new_ref"

count="$(git rev-list --count "$prev_ref..$new_ref")"
printf '\n%d commit(s) between the two releases.\n' "$count"

printf '\n== Candidate commits, grouped ==\n'
git log --format='%H' "$prev_ref..$new_ref" | while read -r sha; do
  subject="$(git log -1 --format='%s' "$sha")"
  case "$subject" in
    fix*|Fix*|hotfix*|HOTFIX*|bugfix*|BUGFIX*)
      printf '  candidate  %s  %s\n' "${sha:0:8}" "$subject"
      ;;
    *)
      printf '  review     %s  %s\n' "${sha:0:8}" "$subject"
      ;;
  esac
done

printf '\n== Files touched ==\n'
git diff --stat "$prev_ref..$new_ref"

cat <<EOF

Next: decide what to port, record it in absorbed.md, then run
  ops/upstream-intake/verify-absorb.sh
Do not absorb until the gate exits 0.
EOF
