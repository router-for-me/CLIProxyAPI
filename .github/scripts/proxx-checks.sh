#!/usr/bin/env bash
# proxx: small CI checks folded into one job. Every check runs; the job lists every failure.
# Usage: proxx-checks.sh [base-ref]   (base-ref enables the PR-only translator path guard)
set -uo pipefail

base="${1:-}"
failures=()
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"

check() {
	local name="$1"
	shift
	local start out status
	start=$(python3 -c "import time;print(time.time())")
	out="$("$@" 2>&1)"
	status=$?
	printf '%-28s %5.1fs %s\n' "$name" "$(python3 -c "import time;print(time.time()-$start)")" "$([ $status -eq 0 ] && echo ok || echo FAIL)"
	if [ $status -ne 0 ]; then
		failures+=("$name")
		printf '%s\n' "$out" | sed 's/^/    /'
	fi
}

gofmt_clean() {
	local files
	files="$(git ls-files -z '*.go' | xargs -0 gofmt -l)"
	[ -z "$files" ] || {
		echo "gofmt -w these files:"
		echo "$files"
		return 1
	}
}

translator_guard() {
	# Upstream rule (AGENTS.md): PRs must not change internal/translator/ on their own.
	local changed
	changed="$(git diff --name-only "$base"...HEAD -- internal/translator/)"
	[ -z "$changed" ] || {
		echo "Changes under internal/translator are not allowed in pull requests:"
		echo "$changed"
		return 1
	}
}

check gofmt gofmt_clean
check "go vet" go vet ./...
check "go mod tidy" go mod tidy -diff
check "codex catalog valid" go run ./cmd/validate_codex_models --file internal/registry/models/codex_client_models.json
check "test inputs inventory" python3 .github/scripts/proxx_ci.py inventory
if [ -n "$base" ]; then
	check "translator path guard" translator_guard
fi

if [ ${#failures[@]} -ne 0 ]; then
	{
		echo "### checks: ${#failures[@]} failed"
		printf -- '- %s\n' "${failures[@]}"
	} | tee -a "$summary"
	exit 1
fi
echo "### checks: all passed" | tee -a "$summary"
