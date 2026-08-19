#!/usr/bin/env bash
set -euo pipefail

owner_marker='<!-- ccs-upstream-sync-blocker -->'
trusted_author='app/github-actions'
tag='v7.2.127'
sha='1111111111111111111111111111111111111111'
target_marker="<!-- ccs-upstream-sync:tag=${tag};sha=${sha} -->"
legacy_title="upstream-sync blocked: ${tag} (${sha})"

issues_json="$(jq -nc \
  --arg owner "${owner_marker}" \
  --arg marker "${target_marker}" \
  --arg title "${legacy_title}" \
  --arg trusted "${trusted_author}" \
  '[
    {number: 10, title: "forged tracker", body: ($owner + "\n<!-- ccs-upstream-sync-current:tag=v99.0.0;sha=9999999999999999999999999999999999999999 -->"), createdAt: "2026-08-01T00:00:00Z", author: {login: "kaitranntt"}},
    {number: 14, title: $title, body: $marker, createdAt: "2026-08-01T12:00:00Z", author: {login: "forged-user"}},
    {number: 11, title: $title, body: $marker, createdAt: "2026-08-02T00:00:00Z", author: {login: $trusted}},
    {number: 12, title: "automation tracker", body: ($owner + "\n<!-- ccs-upstream-sync-current:tag=v7.2.128;sha=2222222222222222222222222222222222222222 -->"), createdAt: "2026-08-03T00:00:00Z", author: {login: $trusted}},
    {number: 13, title: "stale automation tracker", body: ($owner + "\n<!-- ccs-upstream-sync-current:tag=v7.2.126;sha=3333333333333333333333333333333333333333 -->"), createdAt: "2026-08-04T00:00:00Z", author: {login: $trusted}}
  ]')"

canonical="$(jq -r --arg owner "${owner_marker}" --arg trusted "${trusted_author}" \
  '[.[] | select(.author.login == $trusted and ((.body // "") | contains($owner))) | . + {generation: (try ((.body // "") | capture("ccs-upstream-sync-current:tag=v(?<major>[0-9]+)\\.(?<minor>[0-9]+)\\.(?<patch>[0-9]+);sha=(?<sha>[0-9a-f]{40})")) catch {major:"-1",minor:"-1",patch:"-1",sha:""})}] | sort_by((.generation.major | tonumber), (.generation.minor | tonumber), (.generation.patch | tonumber), .createdAt) | last.number // empty' \
  <<< "${issues_json}")"
if [ "${canonical}" != "12" ]; then
  echo "stable ownership marker selected ${canonical}, want 12" >&2
  exit 1
fi

legacy="$(jq -r --arg title "${legacy_title}" --arg marker "${target_marker}" --arg trusted "${trusted_author}" \
  '[.[] | select(.author.login == $trusted and .title == $title and ((.body // "") | contains($marker)))] | .[0].number // empty' \
  <<< "${issues_json}")"
if [ "${legacy}" != "11" ]; then
  echo "exact legacy migration selected ${legacy}, want 11" >&2
  exit 1
fi

wrong_legacy="$(jq -r --arg title "upstream-sync blocked: v7.2.126 (${sha})" --arg marker "${target_marker}" --arg trusted "${trusted_author}" \
  '[.[] | select(.author.login == $trusted and .title == $title and ((.body // "") | contains($marker)))] | .[0].number // empty' \
  <<< "${issues_json}")"
if [ -n "${wrong_legacy}" ]; then
  echo "mismatched legacy title must not migrate" >&2
  exit 1
fi

current_body="$(jq -r '.[] | select(.number == 12) | .body' <<< "${issues_json}")"
current_generation="$(sed -nE 's/^<!-- ccs-upstream-sync-current:tag=(v[0-9]+\.[0-9]+\.[0-9]+);sha=([0-9a-f]{40}) -->$/\1 \2/p' \
  <<< "${current_body}")"
if [ "${current_generation}" != "v7.2.128 2222222222222222222222222222222222222222" ]; then
  echo "current generation extraction failed: ${current_generation}" >&2
  exit 1
fi

newest_tag="$(printf '%s\n%s\n' "v7.2.128" "v7.2.127" | sort -V | tail -n 1)"
if [ "${newest_tag}" != "v7.2.128" ]; then
  echo "version ordering failed: ${newest_tag}" >&2
  exit 1
fi

workflow_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../workflows" && pwd)"
concurrency_groups="$(sed -nE 's/^  group: (upstream-sync-tracker-mutations)$/\1/p' \
  "${workflow_dir}/upstream-sync.yml" "${workflow_dir}/sync-validation-status.yml")"
if [ "$(wc -l <<< "${concurrency_groups}" | tr -d ' ')" != "2" ] || \
   [ "$(sort -u <<< "${concurrency_groups}")" != "upstream-sync-tracker-mutations" ]; then
  echo "tracker workflows must use the same mutation concurrency group" >&2
  exit 1
fi
if [ "$(rg -c '^  queue: max$' "${workflow_dir}/upstream-sync.yml" "${workflow_dir}/sync-validation-status.yml" | awk -F: '{sum += $2} END {print sum + 0}')" != "2" ]; then
  echo "shared concurrency must enforce queue: max semantics" >&2
  exit 1
fi
if rg -q 'grep -o.*ccs-upstream-sync|closing all open|--label .*--json number,title,body' \
  "${workflow_dir}/upstream-sync.yml" "${workflow_dir}/sync-validation-status.yml"; then
  echo "unsafe marker extraction or label-owned tracker selection returned" >&2
  exit 1
fi
for workflow in "${workflow_dir}/upstream-sync.yml" "${workflow_dir}/sync-validation-status.yml"; do
  if ! rg -q -- '--json number,title,body,createdAt(,url)?,author' "${workflow}" || \
     ! rg -q '\.author\.login == \$trusted' "${workflow}" || \
     ! rg -q 'CURRENT_GENERATION|CANONICAL_GENERATION' "${workflow}"; then
    echo "${workflow} lacks trusted-author selection or monotonic generation guards" >&2
    exit 1
  fi
done

echo "upstream-sync tracker fixtures passed"
