GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED ?= 0
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
TAGS ?= netgo osusergo
OUTPUT ?= bin/server

LDFLAGS_COMMON := -checklinkname=0 -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(BUILD_DATE)
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

# 单测命令：明确指定 -checklinkname=0（因 wlynxg/anet 链接私有符号 net.zoneCache）
test:
	go test -ldflags="-checklinkname=0" ./...
