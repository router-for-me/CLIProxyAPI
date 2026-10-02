GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED ?= 0
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
TAGS ?= netgo osusergo
OUTPUT ?= bin/server

# Technical Debt Note: -checklinkname=0
# According to Go documentation:
#   "-checklinkname=0 can be used to disable this check, for debugging and experimenting purposes"
# This is NOT a standard build parameter; it is a temporary technical debt caused by:
#   pion/ice -> pion/transport -> wlynxg/anet (links to internal net.zoneCache)
# See docs/tech-debt-checklinkname.md for full details and removal roadmap (B-4).
CHECKLINKNAME_FLAG := -checklinkname=0

LDFLAGS_COMMON := $(CHECKLINKNAME_FLAG) -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(BUILD_DATE)
LDFLAGS_RELEASE := -s -w $(LDFLAGS_COMMON)

.PHONY: all build release clean test

all: build

# 默认调试构建：加入 -trimpath 去除本地文件系统路径，注入版本信息，保留符号表
build:
	go build -trimpath -ldflags="$(LDFLAGS_COMMON)" -o $(OUTPUT) ./cmd/server

# 生产发布构建：CGO=0 + 纯静态标签 + 去符号表 + 去路径
release:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -tags "$(TAGS)" -ldflags="$(LDFLAGS_RELEASE)" -o $(OUTPUT) ./cmd/server

clean:
	rm -rf bin/ dist/

# 单测命令：附带 -checklinkname=0 确保含 live 依赖的测试包在 Go 1.23+ 正常链接
test:
	go test -ldflags="$(CHECKLINKNAME_FLAG)" ./...
