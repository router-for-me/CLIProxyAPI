#!/bin/sh
set -e

# Fix data directory permissions when running as root.
if [ "$(id -u)" = "0" ]; then
    mkdir -p /app/data /app/logs /app/auths
    chown -R cliproxy:cliproxy /app/data 2>/dev/null || true
    chown -R cliproxy:cliproxy /app/logs 2>/dev/null || true
    chown -R cliproxy:cliproxy /app/auths 2>/dev/null || true
    exec su-exec cliproxy "$0" "$@"
fi

# If the first arg looks like a flag, prepend the default binary.
if [ "${1#-}" != "$1" ]; then
    set -- /app/cli-proxy-api "$@"
fi

exec "$@"
