#!/usr/bin/env bash
set -euo pipefail

REMOTE_USER="${REMOTE_USER:-mike}"
REMOTE_HOST="${REMOTE_HOST:-43.163.225.67}"
REMOTE="${REMOTE_USER}@${REMOTE_HOST}"

CA_FILE="${CA_FILE:-/home/mike/Documents/ca.pem}"
CONTAINER="${CONTAINER:-}"
PUBLISHED_PORT="${PUBLISHED_PORT:-8317}"
CERT_NAME="${CERT_NAME:-reclaude-local-root.crt}"
REMOTE_TMP="${REMOTE_TMP:-/tmp/cliproxy-reclaude-ca.pem}"
RESTART_CONTAINER="${RESTART_CONTAINER:-1}"

PASSWORD="${CLIPROXY_REMOTE_PASSWORD:-${SSHPASS_PASSWORD:-}}"
SSH_OPTS=(
  -o StrictHostKeyChecking=accept-new
  -o UserKnownHostsFile="${KNOWN_HOSTS_FILE:-$HOME/.ssh/known_hosts}"
  -o ConnectTimeout="${SSH_CONNECT_TIMEOUT:-8}"
)

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

ssh_cmd() {
  if [[ -n "$PASSWORD" ]]; then
    sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" "$REMOTE" "$@"
  else
    ssh "${SSH_OPTS[@]}" "$REMOTE" "$@"
  fi
}

scp_cmd() {
  if [[ -n "$PASSWORD" ]]; then
    sshpass -p "$PASSWORD" scp "${SSH_OPTS[@]}" "$CA_FILE" "$REMOTE:$REMOTE_TMP"
  else
    scp "${SSH_OPTS[@]}" "$CA_FILE" "$REMOTE:$REMOTE_TMP"
  fi
}

remote_quote() {
  printf "%q" "$1"
}

detect_container() {
  if [[ -n "$CONTAINER" ]]; then
    echo "$CONTAINER"
    return
  fi

  ssh_cmd "docker ps --format '{{.Names}} {{.Ports}} {{.Image}}' | awk 'index(\$0, \":${PUBLISHED_PORT}->\") { print \$1; exit }'"
}

main() {
  require_cmd ssh
  require_cmd scp
  if [[ -z "$PASSWORD" ]] && command -v sshpass >/dev/null 2>&1 && [[ -t 0 ]]; then
    read -r -s -p "SSH password for $REMOTE: " PASSWORD
    echo
  fi
  if [[ -n "$PASSWORD" ]]; then
    require_cmd sshpass
  fi

  if [[ ! -f "$CA_FILE" ]]; then
    echo "CA file does not exist: $CA_FILE" >&2
    exit 1
  fi

  if command -v openssl >/dev/null 2>&1; then
    echo "Local CA certificate:"
    openssl x509 -in "$CA_FILE" -noout -subject -issuer -dates -fingerprint -sha256
    echo
  fi

  echo "Connecting to $REMOTE ..."
  container="$(detect_container)"
  if [[ -z "$container" ]]; then
    echo "could not find a running container publishing port $PUBLISHED_PORT" >&2
    echo "set CONTAINER=<container_name_or_id> and rerun this script" >&2
    exit 1
  fi
  echo "Target container: $container"

  echo "Copying CA file to remote host ..."
  scp_cmd

  quoted_container="$(remote_quote "$container")"
  quoted_tmp="$(remote_quote "$REMOTE_TMP")"
  quoted_cert="$(remote_quote "/usr/local/share/ca-certificates/$CERT_NAME")"

  echo "Installing CA into container ..."
  ssh_cmd "docker exec $quoted_container mkdir -p /usr/local/share/ca-certificates"
  ssh_cmd "docker cp $quoted_tmp $quoted_container:$quoted_cert"

  echo "Refreshing container trust store ..."
  ssh_cmd "docker exec $quoted_container sh -c 'if command -v update-ca-certificates >/dev/null 2>&1; then :; elif command -v apk >/dev/null 2>&1; then apk add --no-cache ca-certificates; elif command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y ca-certificates; elif command -v update-ca-trust >/dev/null 2>&1; then :; else echo \"no supported CA trust update tool found\" >&2; exit 1; fi; if command -v update-ca-certificates >/dev/null 2>&1; then update-ca-certificates; elif command -v update-ca-trust >/dev/null 2>&1; then update-ca-trust; fi'"

  echo "Cleaning remote temporary file ..."
  ssh_cmd "rm -f $quoted_tmp"

  if [[ "$RESTART_CONTAINER" != "0" ]]; then
    echo "Restarting container ..."
    ssh_cmd "docker restart $quoted_container >/dev/null"
  else
    echo "Skipping container restart because RESTART_CONTAINER=0"
  fi

  echo "Verifying installation ..."
  ssh_cmd "docker ps --filter name=$quoted_container --format '{{.ID}} {{.Image}} {{.Status}} {{.Names}} {{.Ports}}'"
  ssh_cmd "docker exec $quoted_container sh -c 'ls -l /usr/local/share/ca-certificates/$CERT_NAME /etc/ssl/certs/ca-certificates.crt 2>/dev/null || ls -l /usr/local/share/ca-certificates/$CERT_NAME; test -s /usr/local/share/ca-certificates/$CERT_NAME'"

  echo "Done."
}

main "$@"
