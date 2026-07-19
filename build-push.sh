#!/usr/bin/env bash

set -euo pipefail

REMOTE_NAME="${CPA_BUILD_REMOTE:-austin}"
WORKFLOW_FILE="${CPA_BUILD_WORKFLOW:-austin-ci-ghcr.yml}"
REPOSITORY="${CPA_BUILD_REPOSITORY:-austinhmh/CLIProxyAPI}"
IMAGE_NAME="${CPA_BUILD_IMAGE:-ghcr.io/austinhmh/cli-proxy-api-plus}"
RUN_DISCOVERY_TIMEOUT_SECONDS="${CPA_BUILD_RUN_DISCOVERY_TIMEOUT_SECONDS:-300}"

fail() {
  printf 'Error: %s\n' "$1" >&2
  exit 1
}

command -v git >/dev/null 2>&1 || fail "git is required."
command -v gh >/dev/null 2>&1 || fail "GitHub CLI is required."

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "not inside a Git repository."

BRANCH_NAME="$(git branch --show-current)"
[[ -n "${BRANCH_NAME}" ]] || fail "detached HEAD is not supported."
if [[ "${BRANCH_NAME}" != "main" && "${BRANCH_NAME}" != feat/* ]]; then
  fail "branch ${BRANCH_NAME} does not trigger ${WORKFLOW_FILE}; use main or feat/**."
fi

git remote get-url "${REMOTE_NAME}" >/dev/null 2>&1 || fail "Git remote ${REMOTE_NAME} does not exist."

if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  fail "tracked files are not clean; commit the intended changes before building."
fi

if [[ "${CPA_BUILD_ALLOW_UNTRACKED_SOURCE:-0}" != "1" ]]; then
  UNTRACKED_SOURCE_FILES="$(
    git ls-files \
      --others \
      --exclude-standard \
      -- '*.go' '*.sh' '*.yml' '*.yaml' 'Dockerfile*'
  )"
  if [[ -n "${UNTRACKED_SOURCE_FILES}" ]]; then
    printf 'Error: untracked source files could be missing from the CI commit:\n%s\n' "${UNTRACKED_SOURCE_FILES}" >&2
    printf 'Commit, remove, or explicitly review them before setting CPA_BUILD_ALLOW_UNTRACKED_SOURCE=1.\n' >&2
    exit 1
  fi
fi

COMMIT_SHA="$(git rev-parse HEAD)"
[[ "${COMMIT_SHA}" =~ ^[0-9a-f]{40}$ ]] || fail "HEAD is not a full 40-character commit SHA."

gh auth status >/dev/null 2>&1 || fail "GitHub CLI is not authenticated; run gh auth login or provide GH_TOKEN."

git push "${REMOTE_NAME}" "HEAD:refs/heads/${BRANCH_NAME}" >&2

RUN_ID=""
DISCOVERY_DEADLINE=$((SECONDS + RUN_DISCOVERY_TIMEOUT_SECONDS))
while (( SECONDS < DISCOVERY_DEADLINE )); do
  RUN_ID="$(
    gh run list \
      --repo "${REPOSITORY}" \
      --workflow "${WORKFLOW_FILE}" \
      --branch "${BRANCH_NAME}" \
      --event push \
      --limit 30 \
      --json databaseId,headSha,createdAt \
      --jq "map(select(.headSha == \"${COMMIT_SHA}\"))[0].databaseId // empty"
  )"
  if [[ -n "${RUN_ID}" ]]; then
    break
  fi
  sleep 5
done

[[ -n "${RUN_ID}" ]] || fail "timed out waiting for ${WORKFLOW_FILE} at ${COMMIT_SHA}."

if ! gh run watch "${RUN_ID}" --repo "${REPOSITORY}" --exit-status >&2; then
  fail "GitHub Actions run ${RUN_ID} failed."
fi

RUN_RESULT="$(
  gh run view "${RUN_ID}" \
    --repo "${REPOSITORY}" \
    --json headSha,event,conclusion \
    --jq '[.headSha, .event, .conclusion] | @tsv'
)"
IFS=$'\t' read -r RUN_SHA RUN_EVENT RUN_CONCLUSION <<< "${RUN_RESULT}"

[[ "${RUN_SHA}" == "${COMMIT_SHA}" ]] || fail "workflow SHA ${RUN_SHA} does not match ${COMMIT_SHA}."
[[ "${RUN_EVENT}" == "push" ]] || fail "workflow event ${RUN_EVENT} is not push."
[[ "${RUN_CONCLUSION}" == "success" ]] || fail "workflow conclusion ${RUN_CONCLUSION} is not success."

printf '%s:sha-%s\n' "${IMAGE_NAME}" "${COMMIT_SHA}"
