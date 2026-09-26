EXAMPLES := simple model auth frontend-auth executor protocol-format request-translator request-normalizer response-translator response-normalizer thinking usage cli management-api host-callback host-callback-auth-files host-model-callback claude-web-search-router
LANGUAGES := go c rust
CODEX_TOOL_SEARCH_SHIM := codex-tool-search-shim
BIN_DIR := $(CURDIR)/bin
BUILD_DIR := $(BIN_DIR)/build
CLIPROXYAPI_PLUGIN_DIR ?= $(HOME)/.cliproxyapi/plugins

UNAME_S := $(shell uname -s)

ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
RUST_DYLIB_PREFIX :=
RUST_DYLIB_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
RUST_DYLIB_PREFIX := lib
RUST_DYLIB_EXT := dylib
else
PLUGIN_EXT := so
RUST_DYLIB_PREFIX := lib
RUST_DYLIB_EXT := so
endif

.PHONY: build build-codex-tool-search-shim install-codex-tool-search-shim list clean

build: $(foreach example,$(EXAMPLES),$(foreach lang,$(LANGUAGES),$(BIN_DIR)/$(example)-$(lang).$(PLUGIN_EXT))) \
	$(BIN_DIR)/$(CODEX_TOOL_SEARCH_SHIM).$(PLUGIN_EXT)

build-codex-tool-search-shim: $(BIN_DIR)/$(CODEX_TOOL_SEARCH_SHIM).$(PLUGIN_EXT)

install-codex-tool-search-shim: $(BIN_DIR)/$(CODEX_TOOL_SEARCH_SHIM).$(PLUGIN_EXT)
	mkdir -p $(CLIPROXYAPI_PLUGIN_DIR)
	install -m 755 $< $(CLIPROXYAPI_PLUGIN_DIR)/$(CODEX_TOOL_SEARCH_SHIM).$(PLUGIN_EXT)

list:
	@$(foreach example,$(EXAMPLES),$(foreach lang,$(LANGUAGES),echo $(example)/$(lang);))
	@echo $(CODEX_TOOL_SEARCH_SHIM)/go

clean:
	rm -rf $(BIN_DIR)

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

$(BIN_DIR)/%-go.$(PLUGIN_EXT): %/go/main.go %/go/go.mod | $(BIN_DIR)
	cd $*/go && go build -buildmode=c-shared -o $(abspath $@) .
	rm -f $(BIN_DIR)/$*-go.h

$(BIN_DIR)/%-c.$(PLUGIN_EXT): %/c/CMakeLists.txt %/c/src/plugin.c | $(BIN_DIR) $(BUILD_DIR)
	cmake -S $*/c -B $(BUILD_DIR)/$*/c -DCMAKE_LIBRARY_OUTPUT_DIRECTORY=$(BIN_DIR)
	cmake --build $(BUILD_DIR)/$*/c

$(BIN_DIR)/%-rust.$(PLUGIN_EXT): %/rust/Cargo.toml %/rust/Cargo.lock %/rust/src/lib.rs | $(BIN_DIR) $(BUILD_DIR)
	cd $*/rust && CARGO_TARGET_DIR=$(abspath $(BUILD_DIR)/$*/rust) cargo build --release --locked
	cp "$(BUILD_DIR)/$*/rust/release/$(RUST_DYLIB_PREFIX)cliproxy_$(subst -,_,$*)_rust.$(RUST_DYLIB_EXT)" "$@"

$(BIN_DIR)/$(CODEX_TOOL_SEARCH_SHIM).$(PLUGIN_EXT): $(wildcard $(CODEX_TOOL_SEARCH_SHIM)/go/*.go) ../../go.mod ../../go.sum | $(BIN_DIR)
	go build -buildmode=c-shared -o $(abspath $@) ./$(CODEX_TOOL_SEARCH_SHIM)/go
	rm -f $(BIN_DIR)/$(CODEX_TOOL_SEARCH_SHIM).h
