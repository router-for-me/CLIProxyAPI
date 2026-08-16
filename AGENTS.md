# Repository Guidelines

## Project Structure & Module Organization

CLIProxyAPI is a Go 1.26 proxy server exposing OpenAI-, Gemini-, Claude-, and Codex-compatible APIs. The executable entry point is `cmd/server/`; maintenance utilities live in other `cmd/*` directories. Core implementation is under `internal/`, grouped by responsibility such as API handlers, authentication, provider executors, protocol translators, storage, configuration, and WebSocket relay. Reusable public packages belong in `sdk/`. Cross-module integration tests are in `test/`, while unit tests sit beside their source as `*_test.go`. Examples, plugin samples, documentation, and images live in `examples/`, `docs/`, and `assets/` respectively.

## Build, Test, and Development Commands

- `go run ./cmd/server` starts the server locally; it reads `config.yaml` by default.
- `go build -o cc-proxy ./cmd/server` builds the main binary.
- `make build` creates a stripped `cc-proxy` binary; `make build-amd` cross-compiles for Linux AMD64.
- `go test ./path/to/pkg` runs the tests for one affected package.
- `go test -v -run TestName ./path/to/pkg` runs one focused test.
- `gofmt -w path/to/file.go` formats changed Go files before review.

Copy `config.example.yaml` to `config.yaml` for local setup. Environment variables can be placed in `.env`; never commit credentials or files generated under `auths/`.

## Coding Style & Naming Conventions

Follow standard Go conventions and `gofmt`. Package names should be short and lowercase; exported identifiers use `PascalCase`, local identifiers use `camelCase`, and test functions use `TestDescriptiveBehavior`. Keep changes small, keep provider-specific logic in its existing package, and return contextual errors instead of panicking. Preserve established user-facing language in each file and write new code comments in English.

## Testing Guidelines

Use Go's `testing` package and table-driven tests where multiple cases share behavior. Add or update tests beside the changed package; use `test/` only for behavior spanning modules or provider pipelines. During development, run only the necessary package or named tests rather than the full suite. Verify that the affected command still compiles when changing runtime code.

## Commit & Pull Request Guidelines

Recent history generally follows Conventional Commit-style subjects such as `feat(cursor): ...`, `fix(config): ...`, and `perf(translator): ...`. Use an imperative, focused subject and keep unrelated changes separate. Pull requests should explain the problem and solution, list tests run, link relevant issues, and call out configuration or compatibility changes. Include screenshots only for UI or management-asset changes. Do not create a Git commit unless explicitly requested.
