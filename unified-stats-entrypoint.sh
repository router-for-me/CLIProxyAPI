#!/bin/sh
set -eu

DATA_DIR="/CLIProxyAPI/data"
STATS_FILE="${DATA_DIR}/usage_backup.json"
STATS_REMOTE_DIR="usage"
STATS_REMOTE_KEY="${STATS_REMOTE_DIR}/usage_backup.json"
RCLONE_REMOTE="statsremote"
RCLONE_CONFIG_DIR="/root/.config/rclone"
RCLONE_CONFIG_FILE="${RCLONE_CONFIG_DIR}/rclone.conf"
BACKEND=""
SERVER_PID=""

log() {
  echo "[stats] $*" >&2
}

get_management_key() {
  printf '%s' "${MANAGEMENT_PASSWORD:-}"
}

get_pg_dsn() {
  if [ -n "${PGSTORE_DSN:-}" ]; then
    printf '%s' "${PGSTORE_DSN}"
    return 0
  fi
  if [ -n "${DATABASE_URL:-}" ]; then
    printf '%s' "${DATABASE_URL}"
    return 0
  fi
  return 1
}

is_identifier() {
  case "$1" in
    "" | [0-9]* | *[!A-Za-z0-9_]*)
      return 1
      ;;
    *)
      return 0
      ;;
  esac
}

pg_sql_prefix() {
  if [ -n "${PGSTORE_SCHEMA:-}" ] && is_identifier "${PGSTORE_SCHEMA}"; then
    printf 'CREATE SCHEMA IF NOT EXISTS %s; SET search_path TO %s, public;' "${PGSTORE_SCHEMA}" "${PGSTORE_SCHEMA}"
    return 0
  fi
  if [ -n "${PGSTORE_SCHEMA:-}" ]; then
    log "Ignoring unsupported PGSTORE_SCHEMA value for usage backups."
  fi
  return 1
}

pgsql_exec() {
  dsn="$(get_pg_dsn || true)"
  if [ -z "${dsn}" ]; then
    return 1
  fi

  prefix="$(pg_sql_prefix || true)"
  if [ -n "${prefix}" ]; then
    psql "${dsn}" -v ON_ERROR_STOP=1 -q -c "${prefix} $1"
    return $?
  fi

  psql "${dsn}" -v ON_ERROR_STOP=1 -q -c "$1"
}

pgsql_query_raw() {
  dsn="$(get_pg_dsn || true)"
  if [ -z "${dsn}" ]; then
    return 1
  fi

  prefix="$(pg_sql_prefix || true)"
  if [ -n "${prefix}" ]; then
    psql "${dsn}" -v ON_ERROR_STOP=1 -q -t -A -c "${prefix} $1"
    return $?
  fi

  psql "${dsn}" -v ON_ERROR_STOP=1 -q -t -A -c "$1"
}

pgsql_upsert_usage_file() {
  dsn="$(get_pg_dsn || true)"
  if [ -z "${dsn}" ] || [ ! -s "${STATS_FILE}" ]; then
    return 1
  fi

  prefix="$(pg_sql_prefix || true)"
  json_data="$(tr -d '\r\n' < "${STATS_FILE}")"

  if [ -n "${prefix}" ]; then
    psql "${dsn}" -v ON_ERROR_STOP=1 -v usage_json="${json_data}" >/dev/null <<EOF
${prefix}
INSERT INTO usage_backup (id, data, updated_at)
VALUES (1, :'usage_json'::jsonb, NOW())
ON CONFLICT (id) DO UPDATE
SET data = EXCLUDED.data, updated_at = NOW();
EOF
    return $?
  fi

  psql "${dsn}" -v ON_ERROR_STOP=1 -v usage_json="${json_data}" >/dev/null <<EOF
INSERT INTO usage_backup (id, data, updated_at)
VALUES (1, :'usage_json'::jsonb, NOW())
ON CONFLICT (id) DO UPDATE
SET data = EXCLUDED.data, updated_at = NOW();
EOF
}

normalize_backend() {
  case "$1" in
    pgsql | postgres | postgresql)
      printf 'pgsql'
      ;;
    s3 | objectstore | object-storage)
      printf 's3'
      ;;
    local | none | disabled)
      printf 'local'
      ;;
    *)
      return 1
      ;;
  esac
}
read_port_from_file() {
  if [ ! -f "$1" ]; then
    return 1
  fi

  port="$(grep -E '^port:' "$1" 2>/dev/null | sed -E 's/^port: *["'"'"']?([0-9]+)["'"'"']?.*$/\1/' | head -n1)"
  if [ -n "${port}" ]; then
    printf '%s' "${port}"
    return 0
  fi
  return 1
}

get_port() {
  if [ -n "${USAGE_BACKUP_PORT:-}" ]; then
    printf '%s' "${USAGE_BACKUP_PORT}"
    return 0
  fi
  if [ -n "${PORT:-}" ]; then
    printf '%s' "${PORT}"
    return 0
  fi

  for file in \
    "/CLIProxyAPI/config.yaml" \
    "/CLIProxyAPI/objectstore/config/config.yaml" \
    "/CLIProxyAPI/pgstore/config/config.yaml" \
    "/CLIProxyAPI/gitstore/config/config.yaml"
  do
    port="$(read_port_from_file "${file}" || true)"
    if [ -n "${port}" ]; then
      printf '%s' "${port}"
      return 0
    fi
  done

  writable_root="${WRITABLE_PATH:-${writable_path:-}}"
  if [ -n "${writable_root}" ]; then
    for file in \
      "${writable_root}/objectstore/config/config.yaml" \
      "${writable_root}/pgstore/config/config.yaml" \
      "${writable_root}/gitstore/config/config.yaml"
    do
      port="$(read_port_from_file "${file}" || true)"
      if [ -n "${port}" ]; then
        printf '%s' "${port}"
        return 0
      fi
    done
  fi

  printf '8317'
}

detect_backend() {
  if [ -n "${STATS_BACKEND:-}" ]; then
    if backend="$(normalize_backend "${STATS_BACKEND}")"; then
      printf '%s' "${backend}"
      return 0
    fi
    log "Ignoring unsupported STATS_BACKEND value: ${STATS_BACKEND}"
  fi

  if get_pg_dsn >/dev/null 2>&1; then
    printf 'pgsql'
    return 0
  fi

  if [ -n "${OBJECTSTORE_ENDPOINT:-}" ] && [ -n "${OBJECTSTORE_BUCKET:-}" ] && \
     [ -n "${OBJECTSTORE_ACCESS_KEY:-}" ] && [ -n "${OBJECTSTORE_SECRET_KEY:-}" ]; then
    printf 's3'
    return 0
  fi

  printf 'local'
}

setup_rclone() {
  provider="Other"
  force_path_style="true"
  endpoint="${OBJECTSTORE_ENDPOINT:-}"

  case "${endpoint}" in
    *r2.cloudflarestorage.com*)
      provider="Cloudflare"
      force_path_style=""
      ;;
  esac

  mkdir -p "${RCLONE_CONFIG_DIR}"
  cat > "${RCLONE_CONFIG_FILE}" <<EOF
[${RCLONE_REMOTE}]
type = s3
provider = ${provider}
access_key_id = ${OBJECTSTORE_ACCESS_KEY}
secret_access_key = ${OBJECTSTORE_SECRET_KEY}
endpoint = ${OBJECTSTORE_ENDPOINT}
region = auto
no_check_bucket = true
EOF
  if [ -n "${force_path_style}" ]; then
    printf 'force_path_style = true\n' >> "${RCLONE_CONFIG_FILE}"
  fi
}

pull_stats() {
  mkdir -p "${DATA_DIR}"

  case "${BACKEND}" in
    pgsql)
      if ! get_pg_dsn >/dev/null 2>&1; then
        log "PostgreSQL backup requested but no PGSTORE_DSN or DATABASE_URL is configured."
        return 0
      fi
      log "Initializing PostgreSQL usage backup store."
      if ! pgsql_exec "
        CREATE TABLE IF NOT EXISTS usage_backup (
          id INT PRIMARY KEY,
          data JSONB NOT NULL,
          updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
      " >/dev/null 2>&1; then
        log "PostgreSQL initialization failed; continuing without a preloaded backup."
        return 0
      fi

      tmp_file="${STATS_FILE}.tmp"
      if pgsql_query_raw "SELECT data::text FROM usage_backup WHERE id = 1;" > "${tmp_file}" 2>/dev/null && \
         [ -s "${tmp_file}" ] && grep -q '"usage"' "${tmp_file}"; then
        mv "${tmp_file}" "${STATS_FILE}"
        log "Restored usage statistics from PostgreSQL."
      else
        rm -f "${tmp_file}"
        log "No PostgreSQL usage backup found."
      fi
      ;;
    s3)
      if [ -z "${OBJECTSTORE_ENDPOINT:-}" ] || [ -z "${OBJECTSTORE_BUCKET:-}" ] || \
         [ -z "${OBJECTSTORE_ACCESS_KEY:-}" ] || [ -z "${OBJECTSTORE_SECRET_KEY:-}" ]; then
        log "Object storage backup requested but OBJECTSTORE_* configuration is incomplete."
        return 0
      fi
      log "Configuring object storage access."
      setup_rclone
      if rclone copyto "${RCLONE_REMOTE}:${OBJECTSTORE_BUCKET}/${STATS_REMOTE_KEY}" "${STATS_FILE}" \
        --contimeout 5s --timeout 15s --retries 1 --s3-no-check-bucket >/dev/null 2>&1; then
        log "Restored usage statistics from object storage."
      else
        rm -f "${STATS_FILE}"
        log "No object storage usage backup found."
      fi
      ;;
    local)
      log "Using local-only usage backup mode."
      ;;
  esac
}

push_stats() {
  if [ ! -s "${STATS_FILE}" ]; then
    return 0
  fi

  case "${BACKEND}" in
    pgsql)
      if ! get_pg_dsn >/dev/null 2>&1; then
        log "PostgreSQL backup requested but no PGSTORE_DSN or DATABASE_URL is configured."
        return 0
      fi
      if pgsql_upsert_usage_file 2>/tmp/usage-pgsql-error.log; then
        log "Synced usage statistics to PostgreSQL."
      else
        log "Failed to sync usage statistics to PostgreSQL."
        if [ -s /tmp/usage-pgsql-error.log ]; then
          cat /tmp/usage-pgsql-error.log >&2
        fi
      fi
      rm -f /tmp/usage-pgsql-error.log
      ;;
    s3)
      if [ -z "${OBJECTSTORE_ENDPOINT:-}" ] || [ -z "${OBJECTSTORE_BUCKET:-}" ] || \
         [ -z "${OBJECTSTORE_ACCESS_KEY:-}" ] || [ -z "${OBJECTSTORE_SECRET_KEY:-}" ]; then
        log "Object storage backup requested but OBJECTSTORE_* configuration is incomplete."
        return 0
      fi
      if rclone copyto "${STATS_FILE}" "${RCLONE_REMOTE}:${OBJECTSTORE_BUCKET}/${STATS_REMOTE_KEY}" \
        --contimeout 5s --timeout 15s --retries 2 --s3-no-check-bucket >/dev/null 2>&1; then
        log "Synced usage statistics to object storage."
      else
        log "Failed to sync usage statistics to object storage."
      fi
      ;;
  esac
}

wait_for_service() {
  port="$1"
  retry=0
  while [ "${retry}" -lt 30 ]; do
    if [ -n "${SERVER_PID}" ] && ! kill -0 "${SERVER_PID}" 2>/dev/null; then
      return 1
    fi
    if curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}/" 2>/dev/null | grep -q "200"; then
      return 0
    fi
    retry=$((retry + 1))
    sleep 1
  done
  return 1
}

export_usage_snapshot() {
  port="$1"
  management_key="$2"
  tmp_file="${STATS_FILE}.tmp"

  response="$(curl -s -w '\n%{http_code}' -H "X-Management-Key: ${management_key}" \
    "http://localhost:${port}/v0/management/usage/export" 2>/dev/null || true)"
  http_code="$(printf '%s\n' "${response}" | tail -n1)"
  if [ "${http_code}" != "200" ]; then
    rm -f "${tmp_file}"
    return 1
  fi

  printf '%s\n' "${response}" | sed '$d' > "${tmp_file}"
  if [ ! -s "${tmp_file}" ]; then
    rm -f "${tmp_file}"
    return 1
  fi

  mv "${tmp_file}" "${STATS_FILE}"
  return 0
}

import_usage_snapshot() {
  port="$1"
  management_key="$2"

  if [ ! -s "${STATS_FILE}" ]; then
    return 0
  fi

  response="$(curl -s -w '\n%{http_code}' -X POST \
    -H "X-Management-Key: ${management_key}" \
    -H "Content-Type: application/json" \
    -d @"${STATS_FILE}" \
    "http://localhost:${port}/v0/management/usage/import" 2>/dev/null || true)"
  http_code="$(printf '%s\n' "${response}" | tail -n1)"
  if [ "${http_code}" = "200" ]; then
    log "Imported usage statistics into the running service."
    push_stats
    return 0
  fi

  log "Usage import failed with HTTP ${http_code}."
  return 1
}

start_periodic_export_loop() {
  port="$1"
  management_key="$2"
  interval="${USAGE_BACKUP_INTERVAL:-300}"

  (
    while [ -n "${SERVER_PID}" ] && kill -0 "${SERVER_PID}" 2>/dev/null; do
      sleep "${interval}"
      if [ -n "${SERVER_PID}" ] && ! kill -0 "${SERVER_PID}" 2>/dev/null; then
        exit 0
      fi
      if export_usage_snapshot "${port}" "${management_key}"; then
        push_stats
      fi
    done
  ) &
}

graceful_shutdown() {
  trap '' TERM INT
  log "Shutting down."

  port="$(get_port)"
  management_key="$(get_management_key)"
  if [ -n "${management_key}" ] && [ -n "${SERVER_PID}" ] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    if export_usage_snapshot "${port}" "${management_key}"; then
      push_stats
    fi
  fi

  if [ -n "${SERVER_PID}" ] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill -TERM "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  exit 0
}

main() {
  mkdir -p "${DATA_DIR}"

  BACKEND="$(detect_backend)"
  log "Detected usage backup backend: ${BACKEND}"

  if [ "${BACKEND}" = "local" ] && \
     { [ -n "${OBJECTSTORE_ENDPOINT:-}" ] || [ -n "${OBJECTSTORE_BUCKET:-}" ] || [ -n "${OBJECTSTORE_ACCESS_KEY:-}" ] || [ -n "${OBJECTSTORE_SECRET_KEY:-}" ]; }; then
    log "Incomplete OBJECTSTORE_* configuration detected; falling back to local-only backups."
  fi

  pull_stats

  trap graceful_shutdown TERM INT

  log "Starting CLIProxyAPI."
  /CLIProxyAPI/CLIProxyAPI &
  SERVER_PID=$!

  management_key="$(get_management_key)"
  port="$(get_port)"
  if [ -z "${management_key}" ]; then
    log "MANAGEMENT_PASSWORD is not set; usage backup import/export is disabled."
    wait "${SERVER_PID}"
    return $?
  fi

  if wait_for_service "${port}"; then
    import_usage_snapshot "${port}" "${management_key}" || true
    start_periodic_export_loop "${port}" "${management_key}"
  else
    log "Service did not become ready; continuing without usage import."
  fi

  wait "${SERVER_PID}"
}

main "$@"
