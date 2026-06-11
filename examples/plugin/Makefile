EXAMPLES := simple model auth frontend-auth executor protocol-format request-translator request-normalizer response-translator response-normalizer thinking usage cli management-api host-callback
LANGUAGES := go c rust
BIN_DIR := $(CURDIR)/bin
BUILD_DIR := $(BIN_DIR)/build

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

.PHONY: build list clean

build: $(foreach example,$(EXAMPLES),$(foreach lang,$(LANGUAGES),$(BIN_DIR)/$(example)-$(lang).$(PLUGIN_EXT)))

list:
	@$(foreach example,$(EXAMPLES),$(foreach lang,$(LANGUAGES),echo $(example)/$(lang);))

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
