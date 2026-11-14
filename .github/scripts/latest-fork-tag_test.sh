#!/usr/bin/env bash
set -euo pipefail

# Tests the inline selector logic used by the 'latest' step in .github/workflows/docker-image.yml
resolve_latest_fork_tag() {
  local latest=""
  while IFS= read -r tag; do
    if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$ ]]; then
      latest="$tag"
      break
    fi
  done < <(git tag -l --sort=-version:refname)

  if [[ -z "${latest}" ]]; then
    echo "Error: no numeric fork release tag found matching ^v[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$" >&2
    return 1
  fi
  printf '%s\n' "${latest}"
}

# Test 1: Real repo smoke check - format/nonempty check resilient to future version bumps
latest="$(resolve_latest_fork_tag)"
if [[ ! "${latest}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$ ]]; then
  echo "FAIL: expected numeric fork tag format, got ${latest}" >&2
  exit 1
fi
echo "PASS: real repo tag resolved to valid format: ${latest}"

# Test 2: In synthetic git repo with mixed tags, numeric fork format wins
temp_repo="$(mktemp -d)"
trap 'rm -rf "${temp_repo}"' EXIT
(
  cd "${temp_repo}"
  git init -q
  git config user.name "test"
  git config user.email "test@example.com"
  git commit -q --allow-empty -m "init"
  git tag "v7.3.14"
  git tag "v7.3.12"
  git tag "v7.2.127-11"
  git tag "v7.2.127-21"
  git tag "v7.2.127-invalid"

  res="$(resolve_latest_fork_tag)"
  if [[ "${res}" != "v7.2.127-21" ]]; then
    echo "FAIL: expected v7.2.127-21 to win over v7.3.14 and v7.2.127-11, got ${res}" >&2
    exit 1
  fi

  # Test workflow inclusion logic
  is_latest_21="false"
  if [[ "v7.2.127-21" == "${res}" ]]; then
    is_latest_21="true"
  fi
  if [[ "${is_latest_21}" != "true" ]]; then
    echo "FAIL: v7.2.127-21 must set is_latest=true" >&2
    exit 1
  fi

  is_latest_11="false"
  if [[ "v7.2.127-11" == "${res}" ]]; then
    is_latest_11="true"
  fi
  if [[ "${is_latest_11}" != "false" ]]; then
    echo "FAIL: v7.2.127-11 must set is_latest=false" >&2
    exit 1
  fi
)
echo "PASS: synthetic tag competition and workflow inclusion verified"

# Test 3: No-match path returns non-zero error
temp_empty_repo="$(mktemp -d)"
trap 'rm -rf "${temp_repo}" "${temp_empty_repo}"' EXIT
(
  cd "${temp_empty_repo}"
  git init -q
  git config user.name "test"
  git config user.email "test@example.com"
  git commit -q --allow-empty -m "init"
  git tag "v7.3.14"
  git tag "v7.3.12"

  if resolve_latest_fork_tag 2>/dev/null; then
    echo "FAIL: expected error when no fork tags exist" >&2
    exit 1
  fi
)
echo "PASS: no-match path correctly returned non-zero error"
