#!/usr/bin/env bash
#
# Verify that a release tag belongs to this fork before it is published.
#
# The release workflow triggers on any vX.Y.Z tag push, and upstream uses the
# same tag shape. Ancestry alone does not separate them: upstream tags that the
# fork already contains are ancestors of our main, so a stray `git push --tags`
# would satisfy the ancestry check and publish a spurious release.
#
# Name comparison is not enough either. This fork's releases are numbered
# independently and already collide with upstream history: our v1.0.0 and
# upstream v1.0.0 are different commits sharing one name. So this script
# compares the commit each tag points at. Matching target means the tag is
# upstream's and must not be published from here; a different target means the
# name is ours, even when upstream uses it too.
#
# Usage: ops/upstream-intake/guard-release-tag.sh <tag> [upstream-repo]

set -euo pipefail

UPSTREAM_REPO="${2:-${UPSTREAM_REPO:-router-for-me/CLIProxyAPI}}"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

[ "$#" -ge 1 ] || die "usage: guard-release-tag.sh <tag> [upstream-repo]"
tag="$1"

# Our release tags are vX.Y.Z-upstreamA.B.C, the same shape the release
# workflow triggers on. A bare vX.Y.Z is still accepted so the script can be
# used against an untagged upstream name, but it is not what we publish.
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-upstream(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))?$ ]] ||
  die "expected a stable vX.Y.Z or vX.Y.Z-upstreamA.B.C release tag, got '$tag'"

command -v gh >/dev/null 2>&1 || die "gh is required to query $UPSTREAM_REPO"
git rev-parse --verify --quiet "refs/tags/$tag^{commit}" >/dev/null ||
  die "tag '$tag' does not exist locally; fetch or create it before releasing"

local_sha="$(git rev-parse "refs/tags/$tag^{commit}")"

# gh prints the API error body on stdout for a 404, so probe with --silent and
# only trust a well-formed SHA afterwards.
if ! gh api "repos/$UPSTREAM_REPO/git/ref/tags/$tag" --silent >/dev/null 2>&1; then
  printf 'Tag %s does not exist in %s; it is a fork-owned release tag.\n' \
    "$tag" "$UPSTREAM_REPO"
  exit 0
fi

# An annotated tag resolves to a tag object on the API side, so comparing it
# against a local commit would never match. Follow the chain to the commit.
upstream_sha="$(gh api "repos/$UPSTREAM_REPO/git/ref/tags/$tag" \
  --jq '.object.sha' 2>/dev/null)"
object_type="$(gh api "repos/$UPSTREAM_REPO/git/ref/tags/$tag" \
  --jq '.object.type' 2>/dev/null)"
while [ "$object_type" = "tag" ] && [ -n "$upstream_sha" ]; do
  object_type="$(gh api "repos/$UPSTREAM_REPO/git/tags/$upstream_sha" \
    --jq '.object.type' 2>/dev/null)"
  upstream_sha="$(gh api "repos/$UPSTREAM_REPO/git/tags/$upstream_sha" \
    --jq '.object.sha' 2>/dev/null)"
done

if ! printf '%s' "$upstream_sha" | grep -Eq '^[0-9a-f]{40}$'; then
  die "could not resolve $tag in $UPSTREAM_REPO to a commit; refusing to publish"
fi

if [ "$upstream_sha" = "$local_sha" ]; then
  cat >&2 <<EOF
error: tag '$tag' points at $local_sha, which is upstream's own tag in $UPSTREAM_REPO.

Upstream tags must never be published from this fork. They are fetched into the
local-only refs/upstream/tags/* namespace and must not be pushed to origin.
If this tag was pushed by accident, delete it on the remote and re-push only
refs/tags/<our-version> explicitly, never with --tags or --mirror.
EOF
  exit 1
fi

printf 'Tag %s points at %s, distinct from upstream %s; it is a fork-owned release tag.\n' \
  "$tag" "${local_sha:0:8}" "${upstream_sha:0:8}"
