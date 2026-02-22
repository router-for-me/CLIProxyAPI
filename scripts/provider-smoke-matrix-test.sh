#!/usr/bin/env bash
set -euo pipefail

run_matrix_check() {
  local label="$1"
  local expect_exit_code="$2"
  shift 2

  local output status
  output=""
  status=0
  set +e
  output="$("$@" 2>&1)"
  status=$?
  set -e

  printf '===== %s =====\n' "$label"
  echo "${output}"

  if [ "${status}" -ne "${expect_exit_code}" ]; then
    echo "[FAIL] ${label}: expected exit code ${expect_exit_code}, got ${status}"
    exit 1
  fi
}

create_fake_curl() {
  local output_path="$1"
  local state_file="$2"

  cat >"${output_path}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

url=""
output_file=""
next_is_output=0
for arg in "$@"; do
  if [ "${next_is_output}" -eq 1 ]; then
    output_file="${arg}"
    next_is_output=0
    continue
  fi
  if [ "${arg}" = "-o" ]; then
    next_is_output=1
    continue
  fi
  if [[ "${arg}" == http* ]]; then
    url="${arg}"
  fi
done

count=0
if [ -f "${STATE_FILE}" ]; then
  count="$(cat "${STATE_FILE}")"
fi
count=$((count + 1))
printf '%s' "${count}" > "${STATE_FILE}"

case "${url}" in
  *"/v1/models"*)
    if [ -n "${output_file}" ]; then
      printf '%s\n' '{"object":"list","data":[]}' > "${output_file}"
    fi
    echo "200"
    ;;
  *"/v1/responses"*)
    IFS=',' read -r -a statuses <<< "${STATUS_SEQUENCE}"
    index=$((count - 1))
    if [ "${index}" -ge "${#statuses[@]}" ]; then
      index=$(( ${#statuses[@]} - 1 ))
    fi
    status="${statuses[${index}]}"
    if [ -n "${output_file}" ]; then
      printf '%s\n' '{"id":"mock","object":"response"}' > "${output_file}"
    fi
    printf '%s\n' "${status}"
    ;;
  *)
    if [ -n "${output_file}" ]; then
      printf '%s\n' '{"error":"unexpected request"}' > "${output_file}"
    fi
    echo "404"
    ;;
esac
EOF

  chmod +x "${output_path}"
  printf '%s\n' "${state_file}"
}

run_skip_case() {
  local workdir
  workdir="$(mktemp -d)"
  local fake_curl="${workdir}/fake-curl.sh"
  local state="${workdir}/state"

  create_fake_curl "${fake_curl}" "${state}"

  run_matrix_check "empty cases are skipped" 0 \
    env \
      CLIPROXY_PROVIDER_SMOKE_CASES="" \
      CLIPROXY_SMOKE_CURL_BIN="${fake_curl}" \
      CLIPROXY_SMOKE_WAIT_FOR_READY="0" \
      ./scripts/provider-smoke-matrix.sh

  rm -rf "${workdir}"
}

run_pass_case() {
  local workdir
  workdir="$(mktemp -d)"
  local fake_curl="${workdir}/fake-curl.sh"
  local state="${workdir}/state"

  create_fake_curl "${fake_curl}" "${state}"

  run_matrix_check "successful responses complete without failure" 0 \
    env \
      STATUS_SEQUENCE="200,200" \
      STATE_FILE="${state}" \
      CLIPROXY_PROVIDER_SMOKE_CASES="openai:gpt-4o-mini,claude:claude-sonnet-4" \
      CLIPROXY_SMOKE_CURL_BIN="${fake_curl}" \
      CLIPROXY_SMOKE_WAIT_FOR_READY="1" \
      CLIPROXY_SMOKE_READY_ATTEMPTS="1" \
      CLIPROXY_SMOKE_READY_SLEEP_SECONDS="0" \
      ./scripts/provider-smoke-matrix.sh

  rm -rf "${workdir}"
}

run_fail_case() {
  local workdir
  workdir="$(mktemp -d)"
  local fake_curl="${workdir}/fake-curl.sh"
  local state="${workdir}/state"

  create_fake_curl "${fake_curl}" "${state}"

  run_matrix_check "non-2xx responses fail when EXPECT_SUCCESS=0" 1 \
    env \
      STATUS_SEQUENCE="500" \
      STATE_FILE="${state}" \
      CLIPROXY_PROVIDER_SMOKE_CASES="openai:gpt-4o-mini" \
      CLIPROXY_SMOKE_CURL_BIN="${fake_curl}" \
      CLIPROXY_SMOKE_WAIT_FOR_READY="0" \
      CLIPROXY_SMOKE_TIMEOUT_SECONDS="1" \
      ./scripts/provider-smoke-matrix.sh

  rm -rf "${workdir}"
}

run_skip_case
run_pass_case
run_fail_case

echo "[OK] provider-smoke-matrix script test suite passed"
