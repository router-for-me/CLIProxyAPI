#!/usr/bin/env bash
set -euo pipefail

patterns=(
  'sk-[A-Za-z0-9]{20,}'
  'ghp_[A-Za-z0-9]{20,}'
  'AKIA[0-9A-Z]{16}'
  'AIza[0-9A-Za-z_-]{20,}'
  '-----BEGIN (RSA|OPENSSH|EC|DSA|PRIVATE) KEY-----'
)

allowed_context='\$\{|\{\{.*\}\}|<[^>]+>|\[REDACTED|your[_-]?|example|dummy|sample|placeholder'

tmp_hits="$(mktemp)"
trap 'rm -f "${tmp_hits}"' EXIT

for pattern in "${patterns[@]}"; do
  rg -n --pcre2 --hidden \
    --glob '!docs/node_modules/**' \
    --glob '!**/*.min.*' \
    --glob '!**/*.svg' \
    --glob '!**/*.png' \
    --glob '!**/*.jpg' \
    --glob '!**/*.jpeg' \
    --glob '!**/*.gif' \
    --glob '!**/*.webp' \
    --glob '!**/*.pdf' \
    --glob '!**/*.lock' \
    --glob '!**/*.snap' \
    -e "${pattern}" docs README.md README_CN.md examples >> "${tmp_hits}" || true
done

if [[ ! -s "${tmp_hits}" ]]; then
  echo "docs secret sample check passed"
  exit 0
fi

violations=0
while IFS= read -r hit; do
  line_content="${hit#*:*:}"
  if printf '%s' "${line_content}" | rg -qi "${allowed_context}"; then
    continue
  fi
  echo "Potential secret detected: ${hit}"
  violations=1
done < "${tmp_hits}"

if [[ "${violations}" -ne 0 ]]; then
  echo "Secret sample check failed. Replace with placeholders or redact."
  exit 1
fi

echo "docs secret sample check passed"
