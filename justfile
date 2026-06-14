# Standard justfile for cliproxyapi-plusplus

set shell := ["bash", "-cu"]

# List available recipes
default:
    @just --list

# Start the development server / watch mode
dev:
    task dev

# Produce release artifacts
build:
    task build

# Run the test suite
test:
    task test

# Run the linter
lint:
    task lint

# Apply formatter
fmt:
    gofmt -w $(git ls-files '*.go')

# Remove build artifacts
clean:
    task clean

# Measure code coverage (SSOT: see grade.sh for the canonical command)
coverage:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ -f "Cargo.toml" ]]; then
        cargo llvm-cov --workspace --fail-under-lines 85
    elif [[ -f "package.json" ]]; then
        npx jest --coverage --coverageThreshold='{"global":{"branches":85,"functions":85,"lines":85,"statements":85}}'
    elif [[ -f "pyproject.toml" || -f "setup.py" ]]; then
        pytest --cov=src --cov-report=term-missing --cov-fail-under=85
    elif [[ -f "go.mod" ]]; then
        go test -coverprofile=coverage.out -covermode=atomic ./... && go tool cover -func=coverage.out | grep total | awk '{print $3}' | sed 's/%//' | awk '{exit($1 < 85 ? 1 : 0)}'
    else
        echo "No recognized stack (Cargo.toml / package.json / pyproject.toml / go.mod) found." >&2
        exit 1
    fi
