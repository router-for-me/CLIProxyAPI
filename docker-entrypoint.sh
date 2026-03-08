#!/bin/sh
set -eu

APP_DIR="/CLIProxyAPI"
DEFAULT_CONFIG_DIR="${APP_DIR}/config"
LEGACY_CONFIG_PATH="${APP_DIR}/config.yaml"
CONFIG_DIR="${CLI_PROXY_CONFIG_DIR:-${DEFAULT_CONFIG_DIR}}"
AUTH_DIR="${CLI_PROXY_AUTH_DIR:-/root/.cli-proxy-api}"
LOG_DIR="${CLI_PROXY_LOG_DIR:-${APP_DIR}/logs}"
EXAMPLE_CONFIG="${APP_DIR}/config.example.yaml"

mkdir -p "${CONFIG_DIR}" "${AUTH_DIR}" "${LOG_DIR}"

if [ -n "${CLI_PROXY_CONFIG_FILE:-}" ]; then
	CONFIG_PATH="${CLI_PROXY_CONFIG_FILE}"
elif [ -f "${LEGACY_CONFIG_PATH}" ]; then
	CONFIG_PATH="${LEGACY_CONFIG_PATH}"
else
	CONFIG_PATH="${CONFIG_DIR}/config.yaml"
fi

mkdir -p "$(dirname "${CONFIG_PATH}")"

if [ ! -f "${CONFIG_PATH}" ]; then
	cp "${EXAMPLE_CONFIG}" "${CONFIG_PATH}"
fi

exec "${APP_DIR}/CLIProxyAPIPlus" --config "${CONFIG_PATH}" "$@"
