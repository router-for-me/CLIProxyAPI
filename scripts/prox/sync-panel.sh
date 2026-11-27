#!/usr/bin/env bash
# Applies upstream Management Center changes to web/panel.
#
#   scripts/prox/sync-panel.sh [ref]   # default: origin/main of the panel repo
#
# Diffs the panel repo from the commit in web/panel/UPSTREAM to <ref> and
# applies it with a three-way merge, so prox edits survive and real conflicts
# show up as conflict markers. Updates web/panel/UPSTREAM only on success.
set -euo pipefail

repo_url="https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
root="$(git rev-parse --show-toplevel)"
cd "$root"

old="$(awk '{print $1; exit}' web/panel/UPSTREAM)"
git fetch --quiet --no-tags "$repo_url" "+refs/heads/main:refs/prox-panel/main"
ref="${1:-refs/prox-panel/main}"
new="$(git rev-parse "$ref^{commit}")"

if [ "$(git rev-parse "$old^{commit}")" = "$new" ]; then
  echo "web/panel is already at ${new:0:7}"
  exit 0
fi

echo "Applying panel changes ${old:0:7}..${new:0:7}"
if ! git diff --binary "$old" "$new" | git apply --3way --directory=web/panel; then
  echo "Panel changes did not apply cleanly (see above). Fix conflict markers or apply by hand." >&2
  exit 1
fi
date="$(git log -1 --format=%cs "$new")"
echo "${new:0:7} (router-for-me/Cli-Proxy-API-Management-Center main, $date)" > web/panel/UPSTREAM
echo "Done. Next: cd web/panel && bun install && bun run build:prox, then test."
