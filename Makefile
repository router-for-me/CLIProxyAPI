# Makefile — development workflows for CLIProxyAPI.
#
# Conventions match AGENTS.md:
#   - `gofmt -w .` is the source of truth for formatting.
#   - `go build -o test-output ./cmd/server && rm test-output` verifies compile.
#   - `go test ./...` runs the full suite.
#
# PostgreSQL-backed features (api_keys policies, usage_events, models_catalog)
# are gated behind PGSTORE_TEST_DSN. Without it, integration tests are
# skipped automatically — see `make test-pg` to spin up a local Postgres
# container and run the full suite against it.

SHELL          := /usr/bin/env bash
.DEFAULT_SHELL := /usr/bin/env bash

# --- Tooling -----------------------------------------------------------------
GO       ?= go
GOBUILD  := $(GO) build
GOTEST   := $(GO) test
GOFMT    := gofmt
GOCOVER  := $(GO) tool cover

# --- Paths --------------------------------------------------------------------
ROOT         := $(CURDIR)
BIN_DIR      := $(ROOT)/bin
SERVER_BIN   := $(BIN_DIR)/cli-proxy-api
COVERAGE_OUT := $(BIN_DIR)/coverage.out
COVERAGE_HTML := $(BIN_DIR)/coverage.html

# --- Packages ----------------------------------------------------------------
# Packages touched by the PostgreSQL persistence work. Used by targeted test
# targets so contributors can iterate quickly without running the whole tree.
PG_PKGS := \
	./internal/store \
	./internal/policy \
	./internal/api/middleware \
	./internal/api/handlers/management \
	./internal/access/pg_access \
	./internal/registry

# Postgres integration test connection string. Override with:
#   make test-pg PGSTORE_TEST_DSN=postgresql://user:pass@host:5432/db
PG_TEST_DSN ?= postgresql://cliproxy:cliproxy@localhost:5433/cliproxy_test

# Postgres container settings (used by `make pg-up` / `make pg-down`).
PG_IMAGE   ?= postgres:17-alpine
PG_USER    ?= cliproxy
PG_PASS    ?= cliproxy
PG_DB      ?= cliproxy_test
PG_PORT    ?= 5433
PG_CONTAINER := cliproxy-pg-test

# --- Phony targets -----------------------------------------------------------
.PHONY: help all build run fmt vet tidy test test-unit test-pg test-pg-only \
        test-cover cover-html pg-up pg-down pg-reset pg-shell \
        dash-install dash-dev dash-build dash-preview dash-embed \
        clean verify check-deps

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: fmt vet test build ## Format, vet, test, then build (default when running plain `make`.

# --- Build -------------------------------------------------------------------
build: ## Build the server binary into ./bin.
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -o $(SERVER_BIN) ./cmd/server
	@echo "Built $(SERVER_BIN)"

run: build ## Build and run the dev server (`go run` equivalent with cached binary).
	$(SERVER_BIN)

# --- Formatting & hygiene ----------------------------------------------------
fmt: ## Apply gofmt to the entire module.
	$(GOFMT) -w .

vet: ## Run `go vet` on the whole module.
	$(GO) vet ./...

tidy: ## Run `go mod tidy`.
	$(GO) mod tidy

# Verify compile matches the AGENTS.md convention exactly.
verify: ## Verify the project compiles (AGENTS.md required check).
	$(GOBUILD) -o $(BIN_DIR)/test-output ./cmd/server && rm -f $(BIN_DIR)/test-output
	@echo "Compile verification OK"

# --- Tests -------------------------------------------------------------------
test: ## Run the full test suite.
	$(GOTEST) ./...

test-unit: ## Run unit tests for the PostgreSQL-backed packages (no DB needed).
	$(GOTEST) $(PG_PKGS)

test-pg-only: ## Run only the PG integration tests (requires running Postgres).
	PGSTORE_TEST_DSN='$(PG_TEST_DSN)' $(GOTEST) -v -run 'PG|Usage|Model|Policy' $(PG_PKGS)

test-pg: pg-up ## Spin up a local Postgres container, then run the full test suite against it.
	@echo "Running test suite with PGSTORE_TEST_DSN=$(PG_TEST_DSN)"
	PGSTORE_TEST_DSN='$(PG_TEST_DSN)' $(GOTEST) ./...
	@echo "Tip: run \`make pg-down\` to stop the Postgres container when done."

# --- Coverage ----------------------------------------------------------------
test-cover: ## Run tests with coverage across the whole module.
	@mkdir -p $(BIN_DIR)
	$(GOTEST) -covermode=atomic -coverprofile=$(COVERAGE_OUT) ./...
	$(GO) tool cover -func=$(COVERAGE_OUT) | tail -1

cover-html: test-cover ## Generate an HTML coverage report and open it.
	$(GOCOVER) -html=$(COVERAGE_OUT) -o $(COVERAGE_HTML)
	@echo "Coverage report: $(COVERAGE_HTML)"

# --- Postgres container management -------------------------------------------
pg-up: ## Start a local Postgres container on port $(PG_PORT).
	@docker run -d --name $(PG_CONTAINER) \
		-e POSTGRES_USER=$(PG_USER) \
		-e POSTGRES_PASSWORD=$(PG_PASS) \
		-e POSTGRES_DB=$(PG_DB) \
		-p $(PG_PORT):5432 \
		$(PG_IMAGE) >/dev/null
	@echo "Postgres starting on $(PG_TEST_DSN)"
	@echo -n "Waiting for Postgres to accept connections"
	@for i in $$(seq 1 30); do \
		if docker exec $(PG_CONTAINER) pg_isready -U $(PG_USER) -d $(PG_DB) >/dev/null 2>&1; then \
			echo " ready"; exit 0; \
		fi; \
		echo -n "."; sleep 1; \
	done; \
	echo " timeout"; exit 1

pg-down: ## Stop and remove the local Postgres container.
	@docker rm -f $(PG_CONTAINER) >/dev/null 2>&1 || true
	@echo "Postgres container removed"

pg-reset: pg-down pg-up ## Recreate the Postgres container from scratch (drops all data).

pg-shell: ## Open a psql shell against the test Postgres.
	@docker exec -it $(PG_CONTAINER) psql -U $(PG_USER) -d $(PG_DB)

# --- NixLLM Dashboard (React + Vite SPA) -------------------------------------
DASH_DIR := $(ROOT)/web/dashboard

dash-install: ## Install dashboard npm dependencies.
	cd $(DASH_DIR) && npm install

dash-dev: ## Run the dashboard Vite dev server on :9173 (proxies /v0 to the Go API).
	cd $(DASH_DIR) && npm run dev

dash-build: ## Build the production dashboard bundle into web/dashboard/dist.
	cd $(DASH_DIR) && npm run build

dash-preview: ## Preview the production dashboard bundle on :9173.
	cd $(DASH_DIR) && npm run preview

dash-embed: dash-build ## Build the dashboard and rebuild the Go binary so /dashboard serves it.
	$(GOBUILD) -o $(SERVER_BIN) ./cmd/server
	@echo "Dashboard embedded. Run $(SERVER_BIN) and visit /dashboard."

# --- Cleanup -----------------------------------------------------------------
clean: ## Remove build artifacts and coverage files.
	rm -rf $(BIN_DIR) $(DASH_DIR)/dist $(DASH_DIR)/node_modules

check-deps: ## Verify required tools are installed.
	@command -v $(GO) >/dev/null || { echo "go not found in PATH"; exit 1; }
	@command -v $(GOFMT) >/dev/null || { echo "gofmt not found in PATH"; exit 1; }
	@command -v docker >/dev/null || { echo "docker not found (only required for pg-* targets)"; }
	@echo "Dependencies OK"
