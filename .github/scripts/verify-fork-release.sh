#!/usr/bin/env bash
set -euo pipefail
tag="${1:?release tag is required}"
commit="${2:?release commit is required}"

# shellcheck source=/dev/null
source ./.fork-upstream.env
if ! [[ "$tag" =~ ^(v[0-9]+\.[0-9]+\.[0-9]+)-dc[1-9][0-9]*$ &&
        "${BASH_REMATCH[1]}" == "$UPSTREAM_TAG" &&
        "$commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Invalid fork tag or commit: $tag at $commit" >&2
  exit 1
fi
git fetch --no-tags origin refs/heads/dc-use:refs/remotes/origin/dc-use
git fetch --no-tags https://github.com/router-for-me/CLIProxyAPI.git \
  "refs/tags/$UPSTREAM_TAG:refs/upstream-tags/$UPSTREAM_TAG"
test "$(git rev-parse "refs/upstream-tags/$UPSTREAM_TAG^{commit}")" = "$UPSTREAM_COMMIT"
test "$(git rev-parse "refs/tags/$tag^{commit}")" = "$commit"
test "$(git rev-parse HEAD)" = "$commit"
git merge-base --is-ancestor "$UPSTREAM_COMMIT" "$commit"
git merge-base --is-ancestor "$commit" refs/remotes/origin/dc-use
