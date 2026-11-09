#!/usr/bin/env bash
# Upload a CLIProxyAPI monolith release package into the running container and restart it.
#
# The target is the container's SSH endpoint (host port mapped to container 22), so the
# script runs the release switch from inside the container. Use `--container <name>` when
# running this script directly on the Docker host to also collect `docker logs` on failure.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

PACKAGE=""
REMOTE_HOST="${REMOTE_HOST:-127.0.0.1}"
REMOTE_PORT="${REMOTE_PORT:-58524}"
REMOTE_USER="${REMOTE_USER:-deploy}"
REMOTE_APP_DIR="${REMOTE_APP_DIR:-/app}"
REMOTE_DATA_DIR="${REMOTE_DATA_DIR:-/data}"
CONTAINER_NAME="${CONTAINER_NAME:-cliproxyapi-monolith}"
SSH_OPTS="${SSH_OPTS:-}"

usage() {
    cat <<EOF
Usage: $0 --package <release.tar.gz> [options]

Options:
  --package <file>    Release tarball, required
  --host <host>       SSH host, default 127.0.0.1
  --port <port>       SSH port of the container endpoint, default 58524
  --user <user>       SSH user, default deploy
  --app-dir <dir>     Container app directory, default /app
  --container <name>  Container name, used for docker logs diagnostics on the Docker host
  -h, --help          Show help

Environment:
  SSH_OPTS            Extra ssh/scp options, for example '-i ~/.ssh/id_ed25519_cliproxyapi'
  CONTAINER_NAME      Container name, default cliproxyapi-monolith
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --package)
            PACKAGE="$2"
            shift 2
            ;;
        --host)
            REMOTE_HOST="$2"
            shift 2
            ;;
        --port)
            REMOTE_PORT="$2"
            shift 2
            ;;
        --user)
            REMOTE_USER="$2"
            shift 2
            ;;
        --app-dir)
            REMOTE_APP_DIR="$2"
            shift 2
            ;;
        --container)
            CONTAINER_NAME="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown argument: $1${NC}"
            usage
            exit 1
            ;;
    esac
done

if [ -z "${PACKAGE}" ]; then
    echo -e "${RED}--package is required${NC}"
    usage
    exit 1
fi

if [ ! -f "${PACKAGE}" ]; then
    echo -e "${RED}Release package not found: ${PACKAGE}${NC}"
    exit 1
fi

if ! command -v ssh >/dev/null 2>&1 || ! command -v scp >/dev/null 2>&1; then
    echo -e "${RED}ssh and scp commands are required${NC}"
    exit 1
fi

PACKAGE_BASENAME="$(basename "${PACKAGE}")"
RELEASE_NAME="${PACKAGE_BASENAME%.tar.gz}"
REMOTE_RELEASES_DIR="${REMOTE_APP_DIR}/releases"
REMOTE_PACKAGE_PATH="${REMOTE_RELEASES_DIR}/${PACKAGE_BASENAME}"
DEPLOY_STAMP="$(date +%Y%m%d%H%M%S)"
REMOTE_EXTRACTED_RELEASE_NAME="${RELEASE_NAME}-${DEPLOY_STAMP}"

echo "======================================"
echo "Deploy CLIProxyAPI monolith release"
echo "Package: ${PACKAGE}"
echo "Target: ${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_PORT}:${REMOTE_APP_DIR}"
echo "Release: ${RELEASE_NAME}"
echo "Extracted release: ${REMOTE_EXTRACTED_RELEASE_NAME}"
echo "======================================"

collect_diagnostics() {
    echo -e "${YELLOW}Collecting diagnostics...${NC}"
    ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE_USER}@${REMOTE_HOST}" "bash -lc '
        set +e
        cd ${REMOTE_APP_DIR}/current || true
        ./bin/status.sh || true
        echo \"===== stdout.log =====\"
        tail -n 80 ${REMOTE_DATA_DIR}/logs/app/stdout.log 2>/dev/null || true
        echo \"===== stderr.log =====\"
        tail -n 80 ${REMOTE_DATA_DIR}/logs/app/stderr.log 2>/dev/null || true
        echo \"===== app logs =====\"
        ls -1t ${REMOTE_DATA_DIR}/logs 2>/dev/null | head -5
        echo \"===== supervisord =====\"
        tail -n 80 ${REMOTE_DATA_DIR}/logs/supervisor/supervisord.log 2>/dev/null || true
    '" || true

    if command -v docker >/dev/null 2>&1; then
        echo "===== docker logs (${CONTAINER_NAME}) ====="
        docker logs --tail 120 "${CONTAINER_NAME}" 2>/dev/null || true
    fi
}

ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE_USER}@${REMOTE_HOST}" "mkdir -p '${REMOTE_RELEASES_DIR}'"

echo -e "${YELLOW}[1/4] Uploading release package...${NC}"
scp ${SSH_OPTS} -P "${REMOTE_PORT}" "${PACKAGE}" "${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_PACKAGE_PATH}"

echo -e "${YELLOW}[2/4] Extracting and switching current...${NC}"
if ! ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE_USER}@${REMOTE_HOST}" "bash -s" <<REMOTE_SCRIPT
set -euo pipefail
cd '${REMOTE_RELEASES_DIR}'
rm -rf '${REMOTE_EXTRACTED_RELEASE_NAME}'
mkdir -p '${REMOTE_EXTRACTED_RELEASE_NAME}'
tar -xzf '${REMOTE_PACKAGE_PATH}' -C '${REMOTE_EXTRACTED_RELEASE_NAME}' --strip-components=1
test -x '${REMOTE_RELEASES_DIR}/${REMOTE_EXTRACTED_RELEASE_NAME}/cli-proxy-api'
rm -rf '${REMOTE_APP_DIR}/current'
ln -s '${REMOTE_RELEASES_DIR}/${REMOTE_EXTRACTED_RELEASE_NAME}' '${REMOTE_APP_DIR}/current'
REMOTE_SCRIPT
then
    echo -e "${RED}Extract or symlink switch failed${NC}"
    collect_diagnostics
    exit 1
fi

echo -e "${YELLOW}[3/4] Restarting CLIProxyAPI...${NC}"
if ! ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE_USER}@${REMOTE_HOST}" "bash -lc 'cd ${REMOTE_APP_DIR}/current && ./bin/restart.sh'"; then
    echo -e "${RED}Restart failed${NC}"
    collect_diagnostics
    exit 1
fi

echo -e "${YELLOW}[4/4] Checking status...${NC}"
if ! ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE_USER}@${REMOTE_HOST}" "bash -lc 'cd ${REMOTE_APP_DIR}/current && ./bin/status.sh'"; then
    echo -e "${RED}Status check failed${NC}"
    collect_diagnostics
    exit 1
fi

echo ""
echo -e "${GREEN}Deploy complete${NC}"
echo "Health check on the Docker host: curl -s -o /dev/null -w '%{http_code}\\n' http://127.0.0.1:8082/healthz"
