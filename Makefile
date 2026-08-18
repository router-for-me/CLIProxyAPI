# Makefile — development workflows for NixLLM.
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
ROOT          := $(CURDIR)
BIN_DIR       := $(ROOT)/bin
SERVER_BIN    := $(BIN_DIR)/nixllm
COVERAGE_OUT  := $(BIN_DIR)/coverage.out
COVERAGE_HTML := $(BIN_DIR)/coverage.html
DEV_PORT_FILE := $(BIN_DIR)/dev.ports
RUN_LOG       := $(BIN_DIR)/run.log
DASH_LOG      := $(BIN_DIR)/dash-dev.log

# Development service ports. Keep API_PORT aligned with the active server config.
API_PORT  ?= 8317
DASH_PORT ?= 9173

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
.PHONY: help all build run dev-up dev-down logs logs-api logs-dash fmt vet tidy test test-unit test-pg test-pg-only \
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

# --- Development services -----------------------------------------------------
dev-up: ## Run the API and dashboard as background services.
	@mkdir -p $(BIN_DIR)
	@printf 'API_PORT=%s\nDASH_PORT=%s\n' '$(API_PORT)' '$(DASH_PORT)' > $(DEV_PORT_FILE)
	@nohup $(MAKE) run >$(RUN_LOG) 2>&1 &
	@nohup env VITE_API_HOST=http://127.0.0.1:$(API_PORT) $(MAKE) dash-dev >$(DASH_LOG) 2>&1 &
	@echo "API starting on port $(API_PORT) (log: $(RUN_LOG))"
	@echo "Dashboard starting on port $(DASH_PORT) (log: $(DASH_LOG))"
	@echo "Ports recorded in $(DEV_PORT_FILE)"

dev-down: ## Stop the API and dashboard by finding listener PIDs from recorded ports.
	@test -f $(DEV_PORT_FILE) || { echo "Port file not found: $(DEV_PORT_FILE)"; exit 1; }
	@command -v lsof >/dev/null || { echo "lsof not found in PATH"; exit 1; }
	@source $(DEV_PORT_FILE); \
	for service_port in "$$API_PORT" "$$DASH_PORT"; do \
		pids=$$(lsof -tiTCP:"$$service_port" -sTCP:LISTEN 2>/dev/null || true); \
		if [[ -n "$$pids" ]]; then \
			echo "Stopping PID(s) $$(echo $$pids | tr '\n' ' ') listening on port $$service_port"; \
			kill $$pids; \
		else \
			echo "No process is listening on port $$service_port"; \
		fi; \
	done
	@rm -f $(DEV_PORT_FILE)

# --- Logs ---------------------------------------------------------------------
# Follow the dev services' output. `make logs` tails both the API (run.log) and
# the dashboard (dash-dev.log) written by `make dev-up`. Falls back to a
# helpful message when no log file exists yet, so a bare `make logs` before
# `make dev-up` reads cleanly instead of erroring.
logs: ## Follow API + dashboard dev logs (tail -F both run.log and dash-dev.log).
	@for f in $(RUN_LOG) $(DASH_LOG); do \
		if [[ ! -f $$f ]]; then \
			echo "Log file not found: $$f  (run \`make dev-up\` first)"; \
		fi; \
	done
	@if [[ -f $(RUN_LOG) || -f $(DASH_LOG) ]]; then \
		tail -F -n 50 $(RUN_LOG) $(DASH_LOG); \
	else \
		echo "No dev logs yet. Start the services with: make dev-up"; \
	fi

logs-api: ## Follow only the API dev log (run.log).
	@if [[ -f $(RUN_LOG) ]]; then \
		tail -F -n 50 $(RUN_LOG); \
	else \
		echo "Log file not found: $(RUN_LOG)  (run \`make dev-up\` first)"; exit 1; \
	fi

logs-dash: ## Follow only the dashboard dev log (dash-dev.log).
	@if [[ -f $(DASH_LOG) ]]; then \
		tail -F -n 50 $(DASH_LOG); \
	else \
		echo "Log file not found: $(DASH_LOG)  (run \`make dev-up\` first)"; exit 1; \
	fi

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

DASH_EMBED_DIR := $(ROOT)/internal/dashboardasset/dist

dash-embed: dash-build ## Build the dashboard and rebuild the Go binary so /dashboard serves it.
	@rm -rf $(DASH_EMBED_DIR)
	@mkdir -p $(DASH_EMBED_DIR)
	@cp -r $(DASH_DIR)/dist/* $(DASH_EMBED_DIR)/
	# Keep the tracked .gitkeep placeholder so `go:embed dist/*` always has a
	# file to compile on a clean checkout (dashboardasset.Available() then
	# reports false -> dev-mode notice until dash-embed is run again).
	@touch $(DASH_EMBED_DIR)/.gitkeep
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
