#!/usr/bin/env bash

set -euo pipefail

REMOTE_NAME="${CPA_BUILD_REMOTE:-austin}"
WORKFLOW_FILE="${CPA_BUILD_WORKFLOW:-austin-ci-ghcr.yml}"
REPOSITORY="${CPA_BUILD_REPOSITORY:-austinhmh/CLIProxyAPI}"
IMAGE_NAME="${CPA_BUILD_IMAGE:-ghcr.io/austinhmh/cli-proxy-api-plus}"
RUN_DISCOVERY_TIMEOUT_SECONDS="${CPA_BUILD_RUN_DISCOVERY_TIMEOUT_SECONDS:-300}"
RUN_DISCOVERY_POLL_SECONDS="${CPA_BUILD_RUN_DISCOVERY_POLL_SECONDS:-30}"
RUN_STATUS_TIMEOUT_SECONDS="${CPA_BUILD_RUN_STATUS_TIMEOUT_SECONDS:-7200}"
RUN_STATUS_POLL_SECONDS="${CPA_BUILD_RUN_STATUS_POLL_SECONDS:-120}"
GITHUB_API_BASE="${CPA_BUILD_GITHUB_API_BASE:-https://api.github.com}"

fail() {
  printf 'Error: %s\n' "$1" >&2
  exit 1
}

validate_positive_integer() {
  local variable_name="$1"
  local variable_value="$2"
  if [[ ! "${variable_value}" =~ ^[1-9][0-9]*$ ]]; then
    fail "${variable_name} must be a positive integer, got ${variable_value}."
  fi
}

validate_positive_integer "CPA_BUILD_RUN_DISCOVERY_TIMEOUT_SECONDS" "${RUN_DISCOVERY_TIMEOUT_SECONDS}"
validate_positive_integer "CPA_BUILD_RUN_DISCOVERY_POLL_SECONDS" "${RUN_DISCOVERY_POLL_SECONDS}"
validate_positive_integer "CPA_BUILD_RUN_STATUS_TIMEOUT_SECONDS" "${RUN_STATUS_TIMEOUT_SECONDS}"
validate_positive_integer "CPA_BUILD_RUN_STATUS_POLL_SECONDS" "${RUN_STATUS_POLL_SECONDS}"

command -v git >/dev/null 2>&1 || fail "git is required."

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

USE_AUTHENTICATED_GH=false
if command -v gh >/dev/null 2>&1 && gh auth token --hostname github.com >/dev/null 2>&1; then
  USE_AUTHENTICATED_GH=true
fi
if [[ "${USE_AUTHENTICATED_GH}" != "true" ]]; then
  command -v curl >/dev/null 2>&1 || fail "curl is required when GitHub CLI is not authenticated."
  command -v jq >/dev/null 2>&1 || fail "jq is required when GitHub CLI is not authenticated."
fi

github_api_get() {
  local request_url="$1"
  shift

  local curl_arguments=(
    --connect-timeout 10
    --fail
    --location
    --max-time 60
    --silent
    --show-error
    --header "Accept: application/vnd.github+json"
    --header "X-GitHub-Api-Version: 2022-11-28"
  )
  local github_api_token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
  if [[ -n "${github_api_token}" ]]; then
    curl_arguments+=(--header "Authorization: Bearer ${github_api_token}")
  fi

  curl "${curl_arguments[@]}" "$@" "${request_url}"
}

find_workflow_run_id() {
  if [[ "${USE_AUTHENTICATED_GH}" == "true" ]]; then
    gh run list \
      --repo "${REPOSITORY}" \
      --workflow "${WORKFLOW_FILE}" \
      --branch "${BRANCH_NAME}" \
      --event push \
      --limit 30 \
      --json databaseId,headSha,createdAt \
      --jq "map(select(.headSha == \"${COMMIT_SHA}\"))[0].databaseId // empty"
    return
  fi

  github_api_get \
    "${GITHUB_API_BASE}/repos/${REPOSITORY}/actions/workflows/${WORKFLOW_FILE}/runs" \
    --get \
    --data-urlencode "branch=${BRANCH_NAME}" \
    --data-urlencode "event=push" \
    --data-urlencode "head_sha=${COMMIT_SHA}" \
    --data-urlencode "per_page=30" \
    | jq --raw-output --arg commit_sha "${COMMIT_SHA}" \
      '[.workflow_runs[] | select(.head_sha == $commit_sha)][0].id // empty'
}

wait_for_public_workflow_run() {
  local run_id="$1"
  local run_json=""
  local run_status=""
  local status_deadline=$((SECONDS + RUN_STATUS_TIMEOUT_SECONDS))

  while (( SECONDS < status_deadline )); do
    if ! run_json="$(github_api_get "${GITHUB_API_BASE}/repos/${REPOSITORY}/actions/runs/${run_id}")"; then
      printf 'Failed to query GitHub Actions run %s.\n' "${run_id}" >&2
      return 1
    fi
    if ! run_status="$(jq --exit-status --raw-output '.status | select(type == "string" and length > 0)' <<< "${run_json}")"; then
      printf 'GitHub Actions run %s returned an invalid status payload.\n' "${run_id}" >&2
      return 1
    fi
    printf 'GitHub Actions run %s status: %s\n' "${run_id}" "${run_status:-unknown}" >&2
    if [[ "${run_status}" == "completed" ]]; then
      printf '%s\n' "${run_json}"
      return
    fi
    sleep "${RUN_STATUS_POLL_SECONDS}"
  done

  printf 'Timed out waiting for GitHub Actions run %s after %s seconds.\n' "${run_id}" "${RUN_STATUS_TIMEOUT_SECONDS}" >&2
  return 1
}

git push "${REMOTE_NAME}" "HEAD:refs/heads/${BRANCH_NAME}" >&2

RUN_ID=""
DISCOVERY_DEADLINE=$((SECONDS + RUN_DISCOVERY_TIMEOUT_SECONDS))
while (( SECONDS < DISCOVERY_DEADLINE )); do
  if ! RUN_ID="$(find_workflow_run_id)"; then
    printf 'Failed to query workflow runs; retrying in %s seconds.\n' "${RUN_DISCOVERY_POLL_SECONDS}" >&2
    sleep "${RUN_DISCOVERY_POLL_SECONDS}"
    continue
  fi
  if [[ -n "${RUN_ID}" ]]; then
    break
  fi
  sleep "${RUN_DISCOVERY_POLL_SECONDS}"
done

[[ -n "${RUN_ID}" ]] || fail "timed out waiting for ${WORKFLOW_FILE} at ${COMMIT_SHA}."

if [[ "${USE_AUTHENTICATED_GH}" == "true" ]]; then
  if ! gh run watch "${RUN_ID}" --repo "${REPOSITORY}" --exit-status >&2; then
    fail "GitHub Actions run ${RUN_ID} failed."
  fi
  RUN_RESULT="$(
    gh run view "${RUN_ID}" \
      --repo "${REPOSITORY}" \
      --json headSha,event,conclusion \
      --jq '[.headSha, .event, .conclusion] | @tsv'
  )"
else
  printf 'GitHub CLI is not authenticated; monitoring the public workflow through the GitHub API.\n' >&2
  if ! RUN_JSON="$(wait_for_public_workflow_run "${RUN_ID}")"; then
    fail "failed to monitor GitHub Actions run ${RUN_ID}."
  fi
  RUN_RESULT="$(jq --raw-output '[.head_sha, .event, .conclusion] | @tsv' <<< "${RUN_JSON}")"
fi
IFS=$'\t' read -r RUN_SHA RUN_EVENT RUN_CONCLUSION <<< "${RUN_RESULT}"

[[ "${RUN_SHA}" == "${COMMIT_SHA}" ]] || fail "workflow SHA ${RUN_SHA} does not match ${COMMIT_SHA}."
[[ "${RUN_EVENT}" == "push" ]] || fail "workflow event ${RUN_EVENT} is not push."
[[ "${RUN_CONCLUSION}" == "success" ]] || fail "workflow conclusion ${RUN_CONCLUSION} is not success."

printf '%s:sha-%s\n' "${IMAGE_NAME}" "${COMMIT_SHA}"
