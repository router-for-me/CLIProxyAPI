#!/usr/bin/env bash
set -Eeuo pipefail

BASE_URL="${CLIPROXY_BASE_URL:-http://127.0.0.1:8317}"
REQUEST_TIMEOUT="${CLIPROXY_SMOKE_TIMEOUT_SECONDS:-5}"
CASES="${CLIPROXY_PROVIDER_SMOKE_CASES:-}"
EXPECT_SUCCESS="${CLIPROXY_SMOKE_EXPECT_SUCCESS:-0}"
WAIT_FOR_READY="${CLIPROXY_SMOKE_WAIT_FOR_READY:-0}"
READY_ATTEMPTS="${CLIPROXY_SMOKE_READY_ATTEMPTS:-60}"
READY_SLEEP_SECONDS="${CLIPROXY_SMOKE_READY_SLEEP_SECONDS:-1}"

if [ -z "${CASES}" ]; then
  echo "[SKIP] CLIPROXY_PROVIDER_SMOKE_CASES is empty. Set it to comma-separated cases like 'openai:gpt-4o-mini,claude:claude-3-5-sonnet-20241022'."
  exit 0
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "[SKIP] curl is required for provider smoke matrix."
  exit 0
fi

if [ "${WAIT_FOR_READY}" = "1" ]; then
  attempt=0
  while [ "${attempt}" -lt "${READY_ATTEMPTS}" ]; do
    response_code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time "${REQUEST_TIMEOUT}" "${BASE_URL}/v1/models" || true)"
    case "${response_code}" in
      200|401|403)
        echo "[OK] proxy ready (GET /v1/models -> ${response_code})"
        break
        ;;
    esac
    attempt=$((attempt + 1))
    if [ "${attempt}" -ge "${READY_ATTEMPTS}" ]; then
      echo "[WARN] proxy not ready at ${BASE_URL}/v1/models after ${READY_ATTEMPTS} attempts"
      break
    fi
    sleep "${READY_SLEEP_SECONDS}"
  done
fi

export LC_ALL=C
IFS=',' read -r -a CASE_LIST <<< "${CASES}"

failures=0
for case_pair in "${CASE_LIST[@]}"; do
  case_pair="$(echo "${case_pair}" | tr -d '[:space:]')"
  [ -z "${case_pair}" ] && continue

  if [[ "${case_pair}" == *:* ]]; then
    model="${case_pair#*:}"
  else
    model="${case_pair}"
  fi

  if [ -z "${model}" ]; then
    echo "[WARN] empty case in CLIPROXY_PROVIDER_SMOKE_CASES; skipping"
    continue
  fi

  payload="$(printf '{"model":"%s","stream":false,"messages":[{"role":"user","content":"ping"}]}' "${model}")"
  body_file="$(mktemp)"
  http_code="0"

  # shellcheck disable=SC2086
  if ! http_code="$(curl -sS -o "${body_file}" -w '%{http_code}' \
    -X POST \
    -H 'Content-Type: application/json' \
    -d "${payload}" \
    --max-time "${REQUEST_TIMEOUT}" \
    "${BASE_URL}/v1/responses")"; then
    http_code="0"
  fi

  body="$(cat "${body_file}")"
  rm -f "${body_file}"

  if [ "${http_code}" -eq 0 ]; then
    echo "[FAIL] ${model}: request failed (curl/network failure)"
    failures=$((failures + 1))
    continue
  fi

  if [ "${EXPECT_SUCCESS}" = "1" ]; then
    if [ "${http_code}" -ge 400 ]; then
      echo "[FAIL] ${model}: HTTP ${http_code} body=${body}"
      failures=$((failures + 1))
    else
      echo "[OK] ${model}: HTTP ${http_code}"
    fi
    continue
  fi

  if echo "${http_code}" | grep -qE '^(200|401|403)$'; then
    echo "[OK] ${model}: HTTP ${http_code} (non-fatal for matrix smoke)"
  else
    echo "[FAIL] ${model}: HTTP ${http_code} body=${body}"
    failures=$((failures + 1))
  fi
done

if [ "${failures}" -ne 0 ]; then
  echo "[FAIL] provider smoke matrix had ${failures} failing cases"
  exit 1
fi

echo "[OK] provider smoke matrix completed"
