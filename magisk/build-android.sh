#!/bin/bash
# CLIProxyAPI Android 构建脚本
# 用于交叉编译 Android 版本

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
OUTPUT_DIR="$SCRIPT_DIR/bin"
VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_go() {
    if ! command -v go &> /dev/null; then
        log_error "Go is not installed. Please install Go 1.21 or later."
        exit 1
    fi

    GO_VERSION=$(go version | grep -oP 'go\d+\.\d+' | head -1)
    log_info "Go version: $GO_VERSION"
}

build_android() {
    local GOOS=$1
    local GOARCH=$2
    local OUTPUT_NAME="cli-proxy-api-${GOOS}-${GOARCH}"

    log_info "Building for $GOOS/$GOARCH..."

    mkdir -p "$OUTPUT_DIR"

    cd "$PROJECT_ROOT"

    CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build \
        -ldflags="-s -w -X 'main.Version=$VERSION' -X 'main.Commit=$COMMIT' -X 'main.BuildDate=$BUILD_DATE'" \
        -o "$OUTPUT_DIR/$OUTPUT_NAME" \
        ./cmd/server/

    if [ $? -eq 0 ]; then
        log_info "Successfully built: $OUTPUT_DIR/$OUTPUT_NAME"
        FILE_SIZE=$(du -h "$OUTPUT_DIR/$OUTPUT_NAME" | cut -f1)
        log_info "File size: $FILE_SIZE"
    else
        log_error "Failed to build for $GOOS/$GOARCH"
        return 1
    fi
}

build_all_android() {
    log_info "Building CLIProxyAPI for Android..."
    log_info "Version: $VERSION"
    log_info "Commit: $COMMIT"
    log_info "Build Date: $BUILD_DATE"
    echo ""

    build_android "android" "arm64"
    build_android "android" "arm"
    build_android "android" "amd64"

    echo ""
    log_info "All Android builds completed!"
    log_info "Output directory: $OUTPUT_DIR"
    ls -la "$OUTPUT_DIR"
}

build_magisk_module() {
    local ARCH=$1
    local BINARY_NAME="cli-proxy-api-android-${ARCH}"
    local MODULE_NAME="cliproxyapi-${ARCH}-${VERSION}"

    if [ ! -f "$OUTPUT_DIR/$BINARY_NAME" ]; then
        log_error "Binary not found: $OUTPUT_DIR/$BINARY_NAME"
        log_error "Please run 'build' first"
        return 1
    fi

    log_info "Creating Magisk module for $ARCH..."

    local MODULE_DIR="$OUTPUT_DIR/$MODULE_NAME"

    rm -rf "$MODULE_DIR"
    mkdir -p "$MODULE_DIR"

    cp "$OUTPUT_DIR/$BINARY_NAME" "$MODULE_DIR/cli-proxy-api"
    cp "$SCRIPT_DIR/module.prop" "$MODULE_DIR/"
    cp "$SCRIPT_DIR/service.sh" "$MODULE_DIR/"
    cp "$SCRIPT_DIR/post-fs-data.sh" "$MODULE_DIR/"
    cp "$SCRIPT_DIR/uninstall.sh" "$MODULE_DIR/"
    cp "$SCRIPT_DIR/config.yaml" "$MODULE_DIR/"

    mkdir -p "$MODULE_DIR/auths"
    mkdir -p "$MODULE_DIR/logs"
    mkdir -p "$MODULE_DIR/config_backup"
    touch "$MODULE_DIR/auths/.gitkeep"

    chmod 755 "$MODULE_DIR/cli-proxy-api"
    chmod 755 "$MODULE_DIR/service.sh"
    chmod 755 "$MODULE_DIR/post-fs-data.sh"
    chmod 755 "$MODULE_DIR/uninstall.sh"

    sed -i "s/version=v1.0.0/version=$VERSION/" "$MODULE_DIR/module.prop"
    local version_code_num=$(echo $VERSION | tr -d '.v' | grep -oE '^[0-9]+' || echo "1")
    [ -z "$version_code_num" ] && version_code_num="1"
    sed -i "s/versionCode=10000/versionCode=${version_code_num}000/" "$MODULE_DIR/module.prop"

    cd "$OUTPUT_DIR"
    zip -r "${MODULE_NAME}.zip" "$MODULE_NAME"

    log_info "Created Magisk module: $OUTPUT_DIR/${MODULE_NAME}.zip"
}

pack_all_magisk() {
    log_info "Packing Magisk modules..."

    build_magisk_module "arm64"
    build_magisk_module "arm"
    build_magisk_module "amd64"

    log_info "All Magisk modules packed!"
}

clean() {
    log_info "Cleaning build artifacts..."
    rm -rf "$OUTPUT_DIR"
    log_info "Clean completed"
}

show_help() {
    echo "CLIProxyAPI Android Build Script"
    echo ""
    echo "Usage: $0 [command]"
    echo ""
    echo "Commands:"
    echo "  build       Build for all Android architectures (arm64, arm, amd64)"
    echo "  pack        Create Magisk module packages (requires build first)"
    echo "  all         Build and pack everything"
    echo "  clean       Remove build artifacts"
    echo "  help        Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  VERSION     Version string (default: dev)"
    echo "  COMMIT      Git commit hash (default: auto-detect)"
    echo "  BUILD_DATE  Build date (default: current time)"
    echo ""
    echo "Examples:"
    echo "  $0 build                    # Build Android binaries"
    echo "  VERSION=v1.0.0 $0 all       # Build and pack with version"
}

main() {
    check_go

    case "${1:-build}" in
        build)
            build_all_android
            ;;
        pack)
            pack_all_magisk
            ;;
        all)
            build_all_android
            echo ""
            pack_all_magisk
            ;;
        clean)
            clean
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            log_error "Unknown command: $1"
            show_help
            exit 1
            ;;
    esac
}

main "$@"
