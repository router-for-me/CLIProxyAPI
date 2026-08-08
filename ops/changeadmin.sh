#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage:
  ./changeadmin.sh <new-shared-admin-key>

Changes both admin keys to the same plaintext value:
  - CLIProxyAPI remote-management.secret-key
  - CPA Manager Plus admin key
  - CPA Manager Plus upstream CPA management key

The script backs up config/state, resets CPA Manager Plus' persisted
admin credential, restarts services, and verifies both logins.
USAGE
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    die "run as root on kr01"
  fi
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

redact_json() {
  sed -E 's/("(adminKey|managementKey|key|api_key|apiKey)"[[:space:]]*:[[:space:]]*)"[^"]*"/\1"REDACTED"/g'
}

replace_cpa_secret_key() {
  local config_path="$1"
  local new_key="$2"

  python3 - "$config_path" "$new_key" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
new_key = sys.argv[2]
text = path.read_text()
lines = text.splitlines()

out = []
in_remote = False
remote_seen = False
secret_written = False

for line in lines:
    stripped = line.strip()

    if line.startswith("remote-management:"):
        in_remote = True
        remote_seen = True
        out.append(line)
        continue

    if in_remote and line and not line.startswith((" ", "\t")):
        if not secret_written:
            out.append(f'  secret-key: "{new_key}"')
            secret_written = True
        in_remote = False

    if in_remote and stripped.startswith("secret-key:"):
        out.append(f'  secret-key: "{new_key}"')
        secret_written = True
        continue

    out.append(line)

if in_remote and not secret_written:
    out.append(f'  secret-key: "{new_key}"')
    secret_written = True

if not remote_seen:
    out.extend([
        "remote-management:",
        "  allow-remote: true",
        f'  secret-key: "{new_key}"',
        "  disable-control-panel: false",
    ])

path.write_text("\n".join(out) + "\n")
PY
}

reset_cpam_admin_credential() {
  local db_path="$1"

  [ -f "$db_path" ] || return 0

  python3 - "$db_path" <<'PY'
import sqlite3
import sys

db_path = sys.argv[1]
conn = sqlite3.connect(db_path)
try:
    conn.execute("delete from settings where key = 'admin_credential_v1'")
    conn.commit()
finally:
    conn.close()
PY
}

main() {
  require_root
  require_command python3
  require_command systemctl
  require_command curl

  if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    usage
    exit 0
  fi

  local new_key="${1:-}"
  [ -n "$new_key" ] || { usage; exit 2; }
  case "$new_key" in
    *$'\n'*|*$'\r'*) die "key must be a single line" ;;
  esac

  local cpa_config="/root/cliproxyapi/config.yaml"
  local cpa_key_dir="/etc/cliproxyapi"
  local cpa_key_file="${cpa_key_dir}/management.key"
  local cpam_dir="/etc/cpa-manager-plus"
  local cpam_admin_file="${cpam_dir}/admin.key"
  local cpam_env="${cpam_dir}/env"
  local cpam_db="/var/lib/cpa-manager-plus/usage.sqlite"
  local backup_dir="/root/admin-key-change-backups/$(date +%Y%m%d_%H%M%S)"

  [ -f "$cpa_config" ] || die "CLIProxyAPI config not found: $cpa_config"
  [ -d "/opt/cpa-manager-plus/current" ] || die "CPA Manager Plus install not found: /opt/cpa-manager-plus/current"

  echo "Creating backup: $backup_dir"
  mkdir -p "$backup_dir"
  cp -a "$cpa_config" "$backup_dir/config.yaml"
  [ -f "$cpa_key_file" ] && cp -a "$cpa_key_file" "$backup_dir/cliproxyapi.management.key"
  [ -f "$cpam_admin_file" ] && cp -a "$cpam_admin_file" "$backup_dir/cpa-manager-plus.admin.key"
  [ -f "$cpam_env" ] && cp -a "$cpam_env" "$backup_dir/cpa-manager-plus.env"

  echo "Stopping CPA Manager Plus for SQLite credential reset..."
  systemctl stop cpa-manager-plus.service 2>/dev/null || true

  if [ -f "$cpam_db" ]; then
    cp -a "$cpam_db" "$backup_dir/usage.sqlite"
    [ -f "${cpam_db}-wal" ] && cp -a "${cpam_db}-wal" "$backup_dir/usage.sqlite-wal"
    [ -f "${cpam_db}-shm" ] && cp -a "${cpam_db}-shm" "$backup_dir/usage.sqlite-shm"
    reset_cpam_admin_credential "$cpam_db"
  fi

  echo "Writing shared key files (root-only)..."
  install -d -m 700 "$cpa_key_dir" "$cpam_dir"
  umask 077
  printf '%s\n' "$new_key" > "$cpa_key_file"
  printf '%s\n' "$new_key" > "$cpam_admin_file"
  chmod 600 "$cpa_key_file" "$cpam_admin_file"

  echo "Updating CLIProxyAPI config..."
  replace_cpa_secret_key "$cpa_config" "$new_key"
  chmod 600 "$cpa_config"

  echo "Writing CPA Manager Plus service environment..."
  cat > "$cpam_env" <<'ENV'
HTTP_ADDR=0.0.0.0:18317
USAGE_DATA_DIR=/var/lib/cpa-manager-plus
CPA_MANAGER_ADMIN_KEY_FILE=/etc/cpa-manager-plus/admin.key
CPA_UPSTREAM_URL=http://127.0.0.1:8317
CPA_MANAGEMENT_KEY_FILE=/etc/cliproxyapi/management.key
USAGE_COLLECTOR_MODE=http
USAGE_POLL_INTERVAL_MS=500
USAGE_BATCH_SIZE=100
USAGE_QUERY_LIMIT=50000
ENV
  chmod 600 "$cpam_env"

  echo "Restarting services..."
  systemctl daemon-reload
  systemctl restart cliproxyapi.service
  sleep 2
  systemctl restart cpa-manager-plus.service
  sleep 5

  echo "Verifying services..."
  systemctl is-active --quiet cliproxyapi.service || die "cliproxyapi.service is not active"
  systemctl is-active --quiet cpa-manager-plus.service || die "cpa-manager-plus.service is not active"

  echo "Verifying CLIProxyAPI login key..."
  curl -fsS \
    http://127.0.0.1:8317/v0/management/usage-statistics-enabled \
    -H "Authorization: Bearer ${new_key}" \
    | redact_json
  echo

  echo "Verifying CPA Manager Plus admin key..."
  curl -fsS \
    http://127.0.0.1:18317/status \
    -H "Authorization: Bearer ${new_key}" \
    | redact_json
  echo

  echo "Verifying public panels..."
  curl -fsS -I https://clifree.karldigi.dev/management.html | sed -n '1,8p'
  curl -fsS -I https://clifree03.karldigi.dev/management.html | sed -n '1,8p'

  echo
  echo "OK: shared key applied to both panels."
  echo "Use the same key for:"
  echo "  https://clifree.karldigi.dev/management.html"
  echo "  https://clifree03.karldigi.dev/management.html"
  echo
  echo "Key files:"
  echo "  $cpa_key_file"
  echo "  $cpam_admin_file"
  echo
  echo "Backup:"
  echo "  $backup_dir"
}

main "$@"
