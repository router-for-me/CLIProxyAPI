#!/usr/bin/env bash
# Start the CLIProxyAPI monolith base container on a remote Docker host.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

REMOTE_HOST="${REMOTE_HOST:-}"
REMOTE_PORT="${REMOTE_PORT:-22}"
REMOTE_USER="${REMOTE_USER:-root}"
REMOTE_APP_DIR="${REMOTE_APP_DIR:-/app/monolith-cliproxyapi}"
REMOTE_DATA_DIR="${REMOTE_DATA_DIR:-/data/monolith-cliproxyapi}"
SSH_OPTS="${SSH_OPTS:-}"

IMAGE_NAME="${IMAGE_NAME:-cliproxyapi-monolith-base:linux-amd64}"
CONTAINER_NAME="${CONTAINER_NAME:-cliproxyapi-monolith}"
HTTP_PORT="${HTTP_PORT:-8082}"
SSH_PORT="${SSH_PORT:-58524}"
# Provider OAuth callback ports (Codex 1455, Claude 54545, Antigravity 51121, xAI 11451, Gemini 8085)
OAUTH_CALLBACK_PORTS="${OAUTH_CALLBACK_PORTS:-1455 54545 51121 11451 8085}"
APP_USER="${APP_USER:-deploy}"
APP_PASSWORD="${APP_PASSWORD:-deploy123}"
ROOT_PASSWORD="${ROOT_PASSWORD:-root123}"

RECREATE=0

usage() {
    cat <<EOF
Usage: $0 --host <remote-host> [options]

Options:
  --host <host>      Remote Docker host, or use REMOTE_HOST
  --port <port>      SSH port on the Docker host, default 22
  --user <user>      SSH user, default root
  --app-dir <dir>    Host directory mounted to /app, default /app/monolith-cliproxyapi
  --data-dir <dir>   Host directory mounted to /data, default /data/monolith-cliproxyapi
  --recreate         Remove the existing container and recreate it
  -h, --help         Show help

Environment:
  SSH_OPTS           Extra ssh options, for example '-i ~/.ssh/id_rsa'
  IMAGE_NAME         Image name, default cliproxyapi-monolith-base:linux-amd64
  CONTAINER_NAME     Container name, default cliproxyapi-monolith
  HTTP_PORT          Host port mapped to the container API port 8317, default 8082
  SSH_PORT           Host port mapped to container SSH, default 58524
  OAUTH_CALLBACK_PORTS  Host ports mapped 1:1 for OAuth callbacks, default '1455 54545 51121 11451 8085'
  APP_USER           Container SSH deploy user, default deploy
  APP_PASSWORD       Container SSH deploy password, default deploy123
  ROOT_PASSWORD      Container root password, default root123
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
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
        --data-dir)
            REMOTE_DATA_DIR="$2"
            shift 2
            ;;
        --recreate)
            RECREATE=1
            shift
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

if [ -z "${REMOTE_HOST}" ]; then
    echo -e "${RED}Remote host is required: --host or REMOTE_HOST${NC}"
    usage
    exit 1
fi

if ! command -v ssh >/dev/null 2>&1; then
    echo -e "${RED}ssh command is required${NC}"
    exit 1
fi

CALLBACK_PORT_ARGS=""
for port in ${OAUTH_CALLBACK_PORTS}; do
    CALLBACK_PORT_ARGS="${CALLBACK_PORT_ARGS} -p ${port}:${port}"
done

REMOTE="${REMOTE_USER}@${REMOTE_HOST}"

echo "======================================"
echo "Start remote CLIProxyAPI monolith base container"
echo "Remote: ${REMOTE}:${REMOTE_PORT}"
echo "Image: ${IMAGE_NAME}"
echo "Container: ${CONTAINER_NAME}"
echo "Ports: ${HTTP_PORT} -> 8317, ${SSH_PORT} -> 22, callbacks: ${OAUTH_CALLBACK_PORTS}"
echo "Mounts: ${REMOTE_APP_DIR} -> /app, ${REMOTE_DATA_DIR} -> /data"
echo "======================================"

echo -e "${YELLOW}[1/2] Checking remote Docker image...${NC}"
ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE}" "docker image inspect '${IMAGE_NAME}' >/dev/null"

echo -e "${YELLOW}[2/2] Starting remote base container...${NC}"
ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE}" "bash -s" <<REMOTE_RUN
set -euo pipefail

mkdir -p '${REMOTE_APP_DIR}' '${REMOTE_DATA_DIR}' '${REMOTE_DATA_DIR}/auths' '${REMOTE_DATA_DIR}/plugins' '${REMOTE_DATA_DIR}/logs' '${REMOTE_DATA_DIR}/home'

if docker ps -a --format '{{.Names}}' | grep -Fxq '${CONTAINER_NAME}'; then
    if [ '${RECREATE}' -eq 1 ]; then
        docker rm -f '${CONTAINER_NAME}' >/dev/null
    else
        docker start '${CONTAINER_NAME}' >/dev/null
        docker ps --filter 'name=^/${CONTAINER_NAME}$'
        exit 0
    fi
fi

docker run -d \\
    --name '${CONTAINER_NAME}' \\
    --restart unless-stopped \\
    --platform linux/amd64 \\
    -p '${HTTP_PORT}:8317' \\
    -p '${SSH_PORT}:22' \\
    ${CALLBACK_PORT_ARGS} \\
    -v '${REMOTE_APP_DIR}:/app' \\
    -v '${REMOTE_DATA_DIR}:/data' \\
    -e APP_USER='${APP_USER}' \\
    -e APP_PASSWORD='${APP_PASSWORD}' \\
    -e ROOT_PASSWORD='${ROOT_PASSWORD}' \\
    '${IMAGE_NAME}' >/dev/null

docker ps --filter 'name=^/${CONTAINER_NAME}$'
REMOTE_RUN

echo ""
echo -e "${GREEN}Remote base runtime container is ready${NC}"
echo "API: http://${REMOTE_HOST}:${HTTP_PORT}/healthz"
echo "Management panel: http://${REMOTE_HOST}:${HTTP_PORT}/management.html"
echo "Deploy target: REMOTE_HOST=${REMOTE_HOST} REMOTE_PORT=${SSH_PORT} REMOTE_USER=${APP_USER}"
