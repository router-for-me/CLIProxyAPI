#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-deploy}"
APP_PASSWORD="${APP_PASSWORD:-deploy123}"
ROOT_PASSWORD="${ROOT_PASSWORD:-root123}"

mkdir -p /app/current /app/releases /data/auths /data/plugins /data/logs/app /data/logs/supervisor /data/home /data/static /run/sshd

if ! id "${APP_USER}" >/dev/null 2>&1; then
    useradd -m -s /bin/bash "${APP_USER}"
fi

echo "root:${ROOT_PASSWORD}" | chpasswd
echo "${APP_USER}:${APP_PASSWORD}" | chpasswd
chown -R "${APP_USER}:${APP_USER}" /app /data/logs || true

exec /usr/bin/supervisord -n -c /etc/supervisor/supervisord.conf
