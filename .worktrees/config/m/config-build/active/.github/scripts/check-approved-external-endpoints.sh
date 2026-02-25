#!/usr/bin/env bash
set -euo pipefail

policy_file=".github/policies/approved-external-endpoints.txt"
if [[ ! -f "${policy_file}" ]]; then
  echo "Missing policy file: ${policy_file}"
  exit 1
fi

mapfile -t approved_hosts < <(grep -Ev '^\s*#|^\s*$' "${policy_file}" | tr '[:upper:]' '[:lower:]')
if [[ "${#approved_hosts[@]}" -eq 0 ]]; then
  echo "No approved hosts in policy file"
  exit 1
fi

matches_policy() {
  local host="$1"
  local approved
  for approved in "${approved_hosts[@]}"; do
    if [[ "${host}" == "${approved}" || "${host}" == *."${approved}" ]]; then
      return 0
    fi
  done
  return 1
}

mapfile -t discovered_hosts < <(
  rg -No --hidden \
    --glob '!docs/**' \
    --glob '!**/*_test.go' \
    --glob '!**/node_modules/**' \
    --glob '!**/*.png' \
    --glob '!**/*.jpg' \
    --glob '!**/*.jpeg' \
    --glob '!**/*.gif' \
    --glob '!**/*.svg' \
    --glob '!**/*.webp' \
    'https?://[^"\047 )\]]+' \
    cmd pkg sdk scripts .github/workflows config.example.yaml README.md README_CN.md 2>/dev/null \
    | awk -F'://' '{print $2}' \
    | cut -d/ -f1 \
    | cut -d: -f1 \
    | tr '[:upper:]' '[:lower:]' \
    | sort -u
)

unknown=()
for host in "${discovered_hosts[@]}"; do
  [[ -z "${host}" ]] && continue
  [[ "${host}" == *"%"* ]] && continue
  [[ "${host}" == *"{"* ]] && continue
  [[ "${host}" == "localhost" || "${host}" == "127.0.0.1" || "${host}" == "0.0.0.0" ]] && continue
  [[ "${host}" == "example.com" || "${host}" == "www.example.com" ]] && continue
  [[ "${host}" == "proxy.com" || "${host}" == "proxy.local" ]] && continue
  [[ "${host}" == "api.example.com" ]] && continue
  if ! matches_policy "${host}"; then
    unknown+=("${host}")
  fi
done

if [[ "${#unknown[@]}" -ne 0 ]]; then
  echo "Found external hosts not in ${policy_file}:"
  printf '  - %s\n' "${unknown[@]}"
  exit 1
fi

echo "external endpoint policy check passed"
