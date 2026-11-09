#!/usr/bin/env bash
# Build the CLIProxyAPI monolith base image on a remote Docker host.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

REMOTE_HOST="${REMOTE_HOST:-}"
REMOTE_PORT="${REMOTE_PORT:-22}"
REMOTE_USER="${REMOTE_USER:-root}"
REMOTE_WORK_DIR="${REMOTE_WORK_DIR:-/tmp/cliproxyapi-monolith-build}"
SSH_OPTS="${SSH_OPTS:-}"

IMAGE_NAME="${IMAGE_NAME:-cliproxyapi-monolith-base:linux-amd64}"
REBUILD=0

usage() {
    cat <<EOF
Usage: $0 --host <remote-host> [options]

Options:
  --host <host>      Remote Docker host, or use REMOTE_HOST
  --port <port>      SSH port, default 22
  --user <user>      SSH user, default root
  --work-dir <dir>   Remote build context directory, default /tmp/cliproxyapi-monolith-build
  --rebuild          Remove the existing remote image before building
  -h, --help         Show help

Environment:
  SSH_OPTS           Extra ssh/scp options, for example '-i ~/.ssh/id_rsa'
  IMAGE_NAME         Image name, default cliproxyapi-monolith-base:linux-amd64
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
        --work-dir)
            REMOTE_WORK_DIR="$2"
            shift 2
            ;;
        --rebuild)
            REBUILD=1
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

if ! command -v ssh >/dev/null 2>&1 || ! command -v scp >/dev/null 2>&1; then
    echo -e "${RED}ssh and scp commands are required${NC}"
    exit 1
fi

REMOTE="${REMOTE_USER}@${REMOTE_HOST}"

echo "======================================"
echo "Build remote CLIProxyAPI monolith base image"
echo "Remote: ${REMOTE}:${REMOTE_PORT}"
echo "Work dir: ${REMOTE_WORK_DIR}"
echo "Image: ${IMAGE_NAME}"
echo "======================================"

echo -e "${YELLOW}[1/3] Checking remote Docker...${NC}"
ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE}" "command -v docker >/dev/null && docker --version"

echo -e "${YELLOW}[2/3] Uploading build context...${NC}"
ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE}" "rm -rf '${REMOTE_WORK_DIR}' && mkdir -p '${REMOTE_WORK_DIR}/deploy/monolith/base'"
scp ${SSH_OPTS} -P "${REMOTE_PORT}" \
    deploy/monolith/base/Dockerfile \
    deploy/monolith/base/entrypoint.sh \
    deploy/monolith/base/supervisord.conf \
    "${REMOTE}:${REMOTE_WORK_DIR}/deploy/monolith/base/"

echo -e "${YELLOW}[3/3] Building remote image...${NC}"
ssh ${SSH_OPTS} -p "${REMOTE_PORT}" "${REMOTE}" "bash -s" <<REMOTE_BUILD
set -euo pipefail
cd '${REMOTE_WORK_DIR}'
if [ '${REBUILD}' -eq 1 ]; then
    docker image rm -f '${IMAGE_NAME}' >/dev/null 2>&1 || true
fi
docker build --platform linux/amd64 -t '${IMAGE_NAME}' -f deploy/monolith/base/Dockerfile .
REMOTE_BUILD

echo ""
echo -e "${GREEN}Remote base image build complete${NC}"
