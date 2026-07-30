# Codex Fingerprint Synchronization Design

## Goal

Bring CLIProxyAPI's ChatGPT-backed Codex requests into application-layer parity with the current official Codex client while preserving the existing behavior of Codex API-key entries and custom gateways. Keep the software fingerprint current without changing the stable per-account device identity when a new Codex release is published.

## Scope

The feature applies by default only when all of the following are true:

- The selected auth is OAuth/file-backed rather than an API-key entry.
- The auth does not configure a custom `base_url`.
- The upstream target is the official `chatgpt.com/backend-api/codex` service.
- `codex.disable-codex-cloaking` is false.

API-key entries, custom gateways, and requests with cloaking disabled keep their current header, body, and transport behavior.

## Considered Approaches

### 1. Parse the official release and source contract at runtime

Fetch the stable version from the official `@openai/codex` NPM dist-tags and extract public constants from the official `openai/codex` source files. Validate the complete candidate before atomically publishing it. This is the selected approach because it follows the primary source and does not depend on a separately maintained fingerprint service.

### 2. Fetch a project-maintained JSON manifest

Publish a complete profile in a CLIProxyAPI-owned repository and make the runtime consume it. This is easier to parse, but creates an additional trust and maintenance layer and can drift from the official client.

### 3. Keep a compile-time profile and update only the version

Use NPM dist-tags to replace the version inside a fixed User-Agent template. This is the smallest change but does not satisfy contract synchronization when header names, metadata fields, or WebSocket beta values change.

## Architecture

### Versioned fingerprint profile

Add an immutable `CodexFingerprintProfile` in `internal/registry`. It contains:

- Schema version and official source revision.
- Stable Codex release version.
- Originator and official User-Agent template.
- WebSocket beta value.
- Header names used by the current Responses HTTP/WebSocket contract.
- Reserved `client_metadata` and turn-metadata keys.

An embedded JSON profile provides the startup and offline fallback. Readers receive cloned snapshots so request execution never observes a partially updated profile.

### Official-source updater

Add a background updater that runs at startup and every hour. It retrieves bounded responses from official sources, parses only explicitly named constants, rejects downgrades, validates every required field, and publishes the profile under a lock only after the full candidate passes validation. A failed refresh preserves the previous snapshot.

The updater reads:

- `@openai/codex` NPM dist-tags for the stable release version.
- `openai/codex` `default_client.rs` for the default originator and User-Agent contract marker.
- `openai/codex` `client.rs` for Codex headers and the Responses WebSocket beta value.
- `openai/codex` `responses_metadata.rs` for metadata key names.
- The official repository commit endpoint for source provenance.

`codex.disable-fingerprint-auto-sync` disables network refresh while retaining the embedded profile. It defaults to false.

### Stable application identity

Add a request identity assembler for official OAuth traffic. It runs after existing identity-confusion projection so all outbound representations use the final account-scoped values.

- Installation identity is deterministic per selected auth and remains stable across releases.
- Session and thread identity reuse the final prompt-cache/session identity when available.
- A UUIDv7 window identity is cached per session for one hour.
- A fresh UUIDv7 turn identity is created for a new request when the client did not supply one.
- HTTP headers, WebSocket handshake headers, `client_metadata`, and `x-codex-turn-metadata` are generated from one identity snapshot.
- Existing parent-thread, subagent, and client metadata values are preserved unless they conflict with Codex-owned identity fields.

The canonical turn metadata includes installation, session, thread, turn, window, request kind, and turn start time. Compatibility headers are projections of the same object.

### Header consistency

For scoped official OAuth traffic, the active profile supplies `User-Agent`, `Originator`, and `Version` as one atomic unit. Manual/custom behavior remains available through the existing cloaking switch. WebSocket `OpenAI-Beta` uses the active profile rather than a compile-time constant.

### TLS parity

Keep the existing Chrome uTLS HTTP/2 path for `chatgpt.com`. Extend the official `wss://chatgpt.com` path to perform a Chrome uTLS handshake with HTTP/1.1 ALPN before the Gorilla WebSocket upgrade. The dial path uses the existing proxy abstraction so direct, HTTP, HTTPS, SOCKS5, and SOCKS5H egress continue to work. Custom WebSocket gateways retain the standard TLS path.

## Data Flow

1. The service loads the embedded profile.
2. The updater builds and validates a candidate from official sources, then atomically swaps it in.
3. A Codex executor selects an auth and resolves the upstream target.
4. Official OAuth requests assemble one stable application identity.
5. The active profile and identity snapshot populate the body and all transport headers.
6. HTTP uses the existing ChatGPT uTLS transport; WebSocket uses the new uTLS dial path.
7. API-key or custom-gateway requests bypass the new profile and identity assembler.

## Error Handling

- Remote timeouts, non-200 responses, oversized responses, parse failures, invalid profiles, and release downgrades are logged without replacing the active profile.
- Identity generation falls back to deterministic UUIDs where a client session value is absent or malformed.
- A uTLS connection error closes the underlying connection and is returned with transport context.
- Proxy credentials remain redacted in logs.

## Testing

- Profile validation, cloning, no-downgrade behavior, and atomic revision tests.
- Updater tests with local HTTP fixtures for official-source parsing, fallback, oversized responses, and partial-source failure.
- HTTP tests proving coherent UA/Originator/Version plus complete identity projections.
- WebSocket tests proving dynamic beta/profile use, body metadata parity, and uTLS selection only for the official OAuth target.
- Regression tests proving API-key, custom base URL, and disabled-cloaking behavior remain unchanged.
- Existing executor, registry, config, and proxy tests, followed by `go test ./...` and the required server build.

## Boundaries

This feature synchronizes the public application request contract. It preserves a real inbound attestation header but does not synthesize an OpenAI attestation token. TLS uses the project's established Chrome uTLS profile; reproducing a specific Rustls binary build byte-for-byte is outside this profile contract.
