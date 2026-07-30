# Codex Fingerprint Synchronization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give official ChatGPT OAuth Codex traffic a coherent, automatically refreshed application fingerprint and Chrome uTLS coverage for both HTTP and WebSocket transports while preserving API-key and custom-gateway behavior.

**Architecture:** A registry-owned immutable profile is loaded from an embedded fallback and atomically refreshed from official OpenAI Codex sources. Executor-owned identity assembly projects one request identity into headers and `client_metadata`; the existing HTTP uTLS transport is retained and the official WebSocket path gains an HTTP/1.1 uTLS dialer.

**Tech Stack:** Go 1.26, `net/http`, Gorilla WebSocket, `refraction-networking/utls`, Google UUID, `tidwall/gjson`/`sjson`, embedded JSON, existing CLIProxyAPI registry/config/executor patterns.

---

### Task 1: Immutable Codex Fingerprint Profile

**Files:**
- Create: `internal/registry/models/codex_fingerprint_profile.json`
- Create: `internal/registry/codex_fingerprint_profile.go`
- Create: `internal/registry/codex_fingerprint_profile_test.go`

- [ ] **Step 1: Write failing profile tests**

Cover embedded fallback loading, cloned snapshots, required header/metadata fields, User-Agent expansion, invalid header names, invalid templates, and semantic version downgrade rejection.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/registry -run 'TestCodexFingerprintProfile' -count=1`

Expected: build failure because the profile API does not exist.

- [ ] **Step 3: Add the embedded profile and minimal validated store**

Implement:

```go
type CodexFingerprintProfile struct {
    SchemaVersion     int                          `json:"schema_version"`
    SourceRevision   string                       `json:"source_revision"`
    Version          string                       `json:"version"`
    Originator       string                       `json:"originator"`
    UserAgentTemplate string                      `json:"user_agent_template"`
    WebsocketBeta    string                       `json:"websocket_beta"`
    Headers          CodexFingerprintHeaders      `json:"headers"`
    MetadataKeys     CodexFingerprintMetadataKeys `json:"metadata_keys"`
}
```

Expose `GetCodexFingerprintProfile`, `GetCodexFingerprintProfileSnapshot`, and `UserAgent`. Clone all maps/slices returned to callers and increment the store revision only when validated content changes.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `go test ./internal/registry -run 'TestCodexFingerprintProfile' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the profile store**

```bash
git add internal/registry/models/codex_fingerprint_profile.json internal/registry/codex_fingerprint_profile.go internal/registry/codex_fingerprint_profile_test.go
git commit -m "feat(codex): add validated fingerprint profile"
```

### Task 2: Official-Source Profile Updater

**Files:**
- Create: `internal/registry/codex_fingerprint_updater.go`
- Create: `internal/registry/codex_fingerprint_updater_test.go`

- [ ] **Step 1: Write failing updater fixture tests**

Use `httptest.Server` fixtures for NPM dist-tags, `default_client.rs`, `client.rs`, `responses_metadata.rs`, and the GitHub commit payload. Cover a successful complete update, no downgrade, partial-source failure, malformed constants, oversized data, and unchanged revision.

- [ ] **Step 2: Run the focused updater tests and verify RED**

Run: `go test ./internal/registry -run 'TestCodexFingerprintUpdater' -count=1`

Expected: build failure because the updater functions do not exist.

- [ ] **Step 3: Implement bounded official-source parsing**

Add startup plus hourly refresh, a 30-second acquisition timeout, bounded reads, exact constant extraction, semantic version comparison, full candidate validation, and atomic publication. Keep endpoint variables injectable from tests.

- [ ] **Step 4: Run registry tests and verify GREEN**

Run: `go test ./internal/registry -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the updater**

```bash
git add internal/registry/codex_fingerprint_updater.go internal/registry/codex_fingerprint_updater_test.go
git commit -m "feat(codex): sync fingerprint from official sources"
```

### Task 3: Configuration and Service Lifecycle

**Files:**
- Modify: `internal/config/config_types.go`
- Modify: `internal/config/config_normalization.go`
- Modify: `internal/config/clone_test.go`
- Modify: `internal/watcher/diff/config_diff.go`
- Modify: `internal/watcher/diff/config_diff_test.go`
- Modify: `config.example.yaml`
- Modify: `cmd/server/main.go`
- Test: `cmd/server/main_test.go`

- [ ] **Step 1: Write failing config and lifecycle tests**

Assert that `codex.disable-fingerprint-auto-sync` parses/clones/diffs correctly and that the server updater plan starts fingerprint synchronization by default but not when disabled.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/config ./internal/watcher/diff ./cmd/server -run 'CodexFingerprint|FingerprintAutoSync' -count=1`

Expected: build or assertion failure for the missing config field and updater plan.

- [ ] **Step 3: Wire the config and updater lifecycle**

Add `DisableFingerprintAutoSync bool` to `CodexConfig`, document it in `config.example.yaml`, include it in config diff reporting, and start `registry.StartCodexFingerprintUpdater` for the server unless disabled.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `go test ./internal/config ./internal/watcher/diff ./cmd/server -run 'CodexFingerprint|FingerprintAutoSync' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit lifecycle wiring**

```bash
git add internal/config internal/watcher/diff config.example.yaml cmd/server/main.go cmd/server/main_test.go
git commit -m "feat(codex): start fingerprint auto sync"
```

### Task 4: Official OAuth Application Identity

**Files:**
- Create: `internal/runtime/executor/codex_fingerprint.go`
- Create: `internal/runtime/executor/codex_fingerprint_test.go`
- Modify: `internal/runtime/executor/codex_executor_request.go`
- Modify: `internal/runtime/executor/codex_executor_cache_test.go`
- Modify: `internal/runtime/executor/codex_websockets_execute.go`
- Modify: `internal/runtime/executor/codex_websockets_stream.go`

- [ ] **Step 1: Write failing identity assembly tests**

Assert deterministic per-auth installation identity, stable per-session UUIDv7 window identity, fresh UUIDv7 turn identity, canonical turn metadata, body/header parity, preserved parent/subagent metadata, and request-kind selection for `/responses` and `/responses/compact`.

- [ ] **Step 2: Write failing scope regression tests**

Assert that API-key auth, OAuth with custom `base_url`, and `disable-codex-cloaking: true` bypass body and identity projection.

- [ ] **Step 3: Run focused tests and verify RED**

Run: `go test ./internal/runtime/executor -run 'TestCodexOfficialFingerprint|TestCodexApplicationIdentity' -count=1`

Expected: build failure because the assembler does not exist.

- [ ] **Step 4: Implement one canonical identity snapshot**

Create a concurrency-safe one-hour session window cache. Assemble identity after the existing account-confusion projection, write Codex-owned metadata from one snapshot, and project it to compatibility headers at each HTTP and WebSocket call site.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run: `go test ./internal/runtime/executor -run 'TestCodexOfficialFingerprint|TestCodexApplicationIdentity|TestCodexIdentityConfuse' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit identity assembly**

```bash
git add internal/runtime/executor/codex_fingerprint.go internal/runtime/executor/codex_fingerprint_test.go internal/runtime/executor/codex_executor_request.go internal/runtime/executor/codex_executor_cache_test.go internal/runtime/executor/codex_websockets_execute.go internal/runtime/executor/codex_websockets_stream.go
git commit -m "feat(codex): project official request identity"
```

### Task 5: Coherent HTTP and WebSocket Headers

**Files:**
- Modify: `internal/runtime/executor/codex_executor_request.go`
- Modify: `internal/runtime/executor/codex_websockets_request.go`
- Modify: `internal/runtime/executor/codex_websockets_executor_test.go`
- Modify: `internal/runtime/executor/codex_openai_images_test.go`

- [ ] **Step 1: Write failing coherent-profile tests**

Publish a test profile and prove that HTTP and WebSocket requests use its User-Agent, Originator, Version, and WebSocket beta as one unit. Add regressions for config/custom attributes and all bypass scopes.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/runtime/executor -run 'TestApplyCodex.*Fingerprint|TestApplyCodexWebsocket.*Profile' -count=1`

Expected: assertions show compile-time values or mismatched Version headers.

- [ ] **Step 3: Replace compile-time request identity with profile snapshots**

Use one profile snapshot per request, force coherent software headers only within the approved OAuth scope, and keep existing custom/manual behavior outside that scope.

- [ ] **Step 4: Run executor tests and verify GREEN**

Run: `go test ./internal/runtime/executor -count=1`

Expected: PASS.

- [ ] **Step 5: Commit header integration**

```bash
git add internal/runtime/executor
git commit -m "feat(codex): apply coherent dynamic headers"
```

### Task 6: WebSocket Chrome uTLS With Proxy Support

**Files:**
- Modify: `internal/runtime/executor/helps/utls_client.go`
- Modify: `internal/runtime/executor/helps/utls_client_test.go`
- Modify: `internal/runtime/executor/codex_websockets_connection.go`
- Modify: `internal/runtime/executor/codex_websockets_executor_test.go`

- [ ] **Step 1: Write failing dial selection tests**

Assert that official `wss://chatgpt.com/backend-api/codex` OAuth connections install a uTLS `NetDialTLSContext` with `http/1.1` ALPN, while API-key/custom targets retain standard TLS. Cover direct, HTTP, HTTPS, SOCKS5, and SOCKS5H dialer selection without exposing proxy credentials.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/runtime/executor/helps ./internal/runtime/executor -run 'Test.*UTLS.*Websocket|Test.*Websocket.*UTLS' -count=1`

Expected: assertion failure because the current WebSocket dialer uses standard TLS.

- [ ] **Step 3: Implement the context-aware uTLS WebSocket dial path**

Use `proxyutil.BuildDialer` to establish the raw target tunnel, wrap it in `tls.UClient` with `HelloChrome_Auto`, set `ServerName` and `NextProtos: []string{"http/1.1"}`, run `HandshakeContext`, and close the raw connection on error. Enable it only for approved official OAuth WSS URLs.

- [ ] **Step 4: Run transport and executor tests and verify GREEN**

Run: `go test ./internal/runtime/executor/helps ./internal/runtime/executor -count=1`

Expected: PASS.

- [ ] **Step 5: Commit WebSocket uTLS**

```bash
git add internal/runtime/executor/helps/utls_client.go internal/runtime/executor/helps/utls_client_test.go internal/runtime/executor/codex_websockets_connection.go internal/runtime/executor/codex_websockets_executor_test.go
git commit -m "feat(codex): use uTLS for official websockets"
```

### Task 7: Full Verification and Branch Publication

**Files:**
- Modify as required by verification findings.

- [ ] **Step 1: Format all Go changes**

Run: `gofmt -w <all changed .go files>`

- [ ] **Step 2: Run static diff checks**

Run: `git diff --check`

Expected: no output.

- [ ] **Step 3: Run the full test suite**

Run: `go test ./...`

Expected: PASS with zero failing packages.

- [ ] **Step 4: Run the required build**

Run: `go build -o test-output ./cmd/server`

Expected: exit 0. Remove `test-output` after recording the result.

- [ ] **Step 5: Audit every requested capability**

Verify from current source and tests that official OAuth traffic has application identity, current UA/Version, full profile synchronization, HTTP uTLS, WebSocket uTLS, and unchanged API-key/custom behavior.

- [ ] **Step 6: Push the completed branch**

Run: `git push origin feat/codex-fingerprint-sync`

Expected: the remote branch points to the verified local HEAD.
