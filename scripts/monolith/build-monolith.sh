#!/usr/bin/env bash
# Build a CLIProxyAPI release package for the monolith runtime container.
#
# The package contains the static-ish linux binary plus bin/{start,stop,restart,status}.sh
# helpers used by supervisord inside the container. Runtime state (config.yaml, auths,
# plugins, logs, static assets) lives on the persistent /data volume and is NOT packaged.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

default_version() {
    if command -v git >/dev/null 2>&1; then
        local described
        described="$(git describe --tags --always --dirty 2>/dev/null || true)"
        if [ -n "${described}" ]; then
            printf '%s' "${described}"
            return
        fi
    fi
    date +%Y%m%d-%H%M%S
}

APP_NAME="${APP_NAME:-cliproxyapi}"
VERSION="${VERSION:-$(default_version)}"
TARGET_OS="${TARGET_OS:-linux}"
TARGET_ARCH="${TARGET_ARCH:-amd64}"
OUTPUT_ROOT="${OUTPUT_ROOT:-${ROOT_DIR}/build/monolith}"
PACKAGE_NAME="${APP_NAME}-${VERSION}-${TARGET_OS}-${TARGET_ARCH}"
PACKAGE_DIR="${OUTPUT_ROOT}/${PACKAGE_NAME}"
TARBALL="${OUTPUT_ROOT}/${PACKAGE_NAME}.tar.gz"
BINARY_NAME="cli-proxy-api"

usage() {
    cat <<EOF
Usage: $0 [options]

Options:
  --version <version>      Release version, default git describe or a timestamp
  --output-root <dir>      Output root, default build/monolith
  -h, --help               Show help

Environment:
  APP_NAME                 App name, default cliproxyapi
  TARGET_OS                Target OS, default linux
  TARGET_ARCH              Target arch, default amd64
  CGO_ENABLED              Default 1 (matches the upstream Dockerfile and keeps Go plugin support)
  SKIP_GO_MOD_DOWNLOAD=1   Skip go mod download
  GOPROXY                  Go module proxy, use https://goproxy.cn,direct on hosts without proxy.golang.org
EOF
}

refresh_package_paths() {
    PACKAGE_NAME="${APP_NAME}-${VERSION}-${TARGET_OS}-${TARGET_ARCH}"
    PACKAGE_DIR="${OUTPUT_ROOT}/${PACKAGE_NAME}"
    TARBALL="${OUTPUT_ROOT}/${PACKAGE_NAME}.tar.gz"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version)
            VERSION="$2"
            refresh_package_paths
            shift 2
            ;;
        --output-root)
            OUTPUT_ROOT="$2"
            refresh_package_paths
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

require_cmd() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo -e "${RED}Missing command: $1${NC}"
        exit 1
    fi
}

git_commit() {
    if command -v git >/dev/null 2>&1; then
        git rev-parse --short HEAD 2>/dev/null || printf 'unknown'
        return
    fi
    printf 'unknown'
}

echo "======================================"
echo "CLIProxyAPI monolith release build"
echo "Version: ${VERSION}"
echo "Target: ${TARGET_OS}/${TARGET_ARCH}"
echo "Output: ${PACKAGE_DIR}"
echo "======================================"

require_cmd go
require_cmd tar

echo -e "${YELLOW}[1/4] Preparing release directory...${NC}"
rm -rf "${PACKAGE_DIR}" "${TARBALL}"
mkdir -p "${PACKAGE_DIR}/bin" "${OUTPUT_ROOT}"

echo -e "${YELLOW}[2/4] Building server binary...${NC}"
if [ "${SKIP_GO_MOD_DOWNLOAD:-0}" != "1" ]; then
    go mod download
fi

COMMIT_VALUE="$(git_commit)"
BUILD_DATE="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

CGO_ENABLED="${CGO_ENABLED:-1}" GOOS="${TARGET_OS}" GOARCH="${TARGET_ARCH}" go build \
    -buildvcs=false \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT_VALUE} -X main.BuildDate=${BUILD_DATE}" \
    -o "${PACKAGE_DIR}/${BINARY_NAME}" \
    ./cmd/server/

echo -e "${YELLOW}[3/4] Assembling release package...${NC}"

cat > "${PACKAGE_DIR}/bin/start.sh" <<'START_SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${APP_DIR}"

MODE="${1:-daemon}"
DATA_DIR="${DATA_DIR:-/data}"
CONFIG_PATH="${CONFIG_PATH:-${DATA_DIR}/config.yaml}"
PID_FILE="${PID_FILE:-${DATA_DIR}/logs/app/cliproxyapi.pid}"
STDOUT_LOG="${STDOUT_LOG:-${DATA_DIR}/logs/app/stdout.log}"
STDERR_LOG="${STDERR_LOG:-${DATA_DIR}/logs/app/stderr.log}"
BINARY="${APP_DIR}/cli-proxy-api"

mkdir -p "$(dirname "${PID_FILE}")" "${DATA_DIR}/auths" "${DATA_DIR}/plugins" "${DATA_DIR}/home"

# Runtime state (logs, management panel assets, LAN discovery state) must stay on the
# persistent volume, never inside the release directory.
export WRITABLE_PATH="${DATA_DIR}"
export HOME="${HOME_DIR:-${DATA_DIR}/home}"
export TZ="${TZ:-Asia/Shanghai}"

if [ ! -f "${CONFIG_PATH}" ]; then
    echo "config file not found: ${CONFIG_PATH}" >&2
    exit 1
fi

if [ "${MODE}" = "foreground" ]; then
    exec "${BINARY}" --config "${CONFIG_PATH}"
fi

if [ -f "${PID_FILE}" ] && kill -0 "$(cat "${PID_FILE}")" >/dev/null 2>&1; then
    echo "cli-proxy-api is already running, PID=$(cat "${PID_FILE}")"
    exit 0
fi

nohup "${BINARY}" --config "${CONFIG_PATH}" >"${STDOUT_LOG}" 2>"${STDERR_LOG}" &
echo "$!" > "${PID_FILE}"
echo "cli-proxy-api started, PID=$(cat "${PID_FILE}")"
START_SCRIPT

cat > "${PACKAGE_DIR}/bin/stop.sh" <<'STOP_SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

DATA_DIR="${DATA_DIR:-/data}"
PID_FILE="${PID_FILE:-${DATA_DIR}/logs/app/cliproxyapi.pid}"

if command -v supervisorctl >/dev/null 2>&1 && [ -S /run/supervisor.sock ]; then
    supervisorctl -s unix:///run/supervisor.sock stop cliproxyapi
    exit 0
fi

if [ ! -f "${PID_FILE}" ]; then
    echo "cli-proxy-api is not running: PID file not found"
    exit 0
fi

PID="$(cat "${PID_FILE}")"
if ! kill -0 "${PID}" >/dev/null 2>&1; then
    rm -f "${PID_FILE}"
    echo "cli-proxy-api is not running: removed stale PID file"
    exit 0
fi

kill "${PID}"
for _ in $(seq 1 15); do
    if ! kill -0 "${PID}" >/dev/null 2>&1; then
        rm -f "${PID_FILE}"
        echo "cli-proxy-api stopped"
        exit 0
    fi
    sleep 1
done

kill -9 "${PID}" >/dev/null 2>&1 || true
rm -f "${PID_FILE}"
echo "cli-proxy-api force stopped"
STOP_SCRIPT

cat > "${PACKAGE_DIR}/bin/restart.sh" <<'RESTART_SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if command -v supervisorctl >/dev/null 2>&1 && [ -S /run/supervisor.sock ]; then
    supervisorctl -s unix:///run/supervisor.sock restart cliproxyapi
else
    "${SCRIPT_DIR}/stop.sh"
    "${SCRIPT_DIR}/start.sh"
fi
RESTART_SCRIPT

cat > "${PACKAGE_DIR}/bin/status.sh" <<'STATUS_SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

DATA_DIR="${DATA_DIR:-/data}"
PID_FILE="${PID_FILE:-${DATA_DIR}/logs/app/cliproxyapi.pid}"

if command -v supervisorctl >/dev/null 2>&1 && [ -S /run/supervisor.sock ]; then
    supervisorctl -s unix:///run/supervisor.sock status
    exit 0
fi

if [ -f "${PID_FILE}" ] && kill -0 "$(cat "${PID_FILE}")" >/dev/null 2>&1; then
    echo "cli-proxy-api is running, PID=$(cat "${PID_FILE}")"
else
    echo "cli-proxy-api is not running"
fi
STATUS_SCRIPT

chmod +x "${PACKAGE_DIR}/${BINARY_NAME}" "${PACKAGE_DIR}"/bin/*.sh

cat > "${PACKAGE_DIR}/VERSION" <<EOF
APP_NAME=${APP_NAME}
VERSION=${VERSION}
COMMIT=${COMMIT_VALUE}
TARGET=${TARGET_OS}/${TARGET_ARCH}
BUILD_TIME=${BUILD_DATE}
EOF

echo -e "${YELLOW}[4/4] Creating tarball...${NC}"
tar -C "${OUTPUT_ROOT}" -czf "${TARBALL}" "${PACKAGE_NAME}"
tar -tzf "${TARBALL}" >/dev/null

echo ""
echo "======================================"
echo -e "${GREEN}Build complete${NC}"
echo "Release dir: ${PACKAGE_DIR}"
echo "Tarball: ${TARBALL}"
echo "======================================"
