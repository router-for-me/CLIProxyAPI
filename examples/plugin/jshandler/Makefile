PLUGIN_NAME ?= jshandler
BUILD_DIR ?= .
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_OUTPUT ?= $(BUILD_DIR)/$(PLUGIN_NAME).$(PLUGIN_EXT)
PLUGIN_HEADER = $(basename $(PLUGIN_OUTPUT)).h

.PHONY: build clean

build:
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildmode=c-shared -o $(PLUGIN_OUTPUT) .

clean:
	rm -f $(BUILD_DIR)/$(PLUGIN_NAME).so
	rm -f $(BUILD_DIR)/$(PLUGIN_NAME).dylib
	rm -f $(BUILD_DIR)/$(PLUGIN_NAME).dll
	rm -f $(PLUGIN_HEADER)
