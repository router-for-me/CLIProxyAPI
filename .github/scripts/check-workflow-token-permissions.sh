#!/usr/bin/env bash
set -euo pipefail

violations=0
allowed_write_keys='security-events|id-token|pages'

for workflow in .github/workflows/*.yml .github/workflows/*.yaml; do
  [[ -f "${workflow}" ]] || continue

  if rg -n '^permissions:\s*write-all\s*$' "${workflow}" >/dev/null; then
    echo "${workflow}: uses permissions: write-all"
    violations=1
  fi

  if rg -n '^on:' "${workflow}" >/dev/null && rg -n 'pull_request:' "${workflow}" >/dev/null; then
    while IFS= read -r line; do
      key="$(printf '%s' "${line}" | sed -E 's/^[0-9]+:\s*([a-zA-Z-]+):\s*write\s*$/\1/')"
      if [[ "${key}" != "${line}" ]] && ! printf '%s' "${key}" | grep -Eq "^(${allowed_write_keys})$"; then
        echo "${workflow}: pull_request workflow grants '${key}: write'"
        violations=1
      fi
    done < <(rg -n '^\s*[a-zA-Z-]+:\s*write\s*$' "${workflow}")
  fi
done

if [[ "${violations}" -ne 0 ]]; then
  echo "workflow token permission check failed"
  exit 1
fi

echo "workflow token permission check passed"
