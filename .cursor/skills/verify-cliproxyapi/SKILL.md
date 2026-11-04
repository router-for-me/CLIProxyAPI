---
name: verify-cliproxyapi
description: Build and launch an isolated CLIProxyAPI server (Go, cmd/server) on a random localhost port with throwaway API/management keys, then drive its HTTP surface (healthz, OpenAI/Claude/Gemini model listing, client auth, chat-completions error path, Management API config writes) and CLI flags with curl, capturing status/headers/bodies as evidence. Use after changing routes, auth, config handling or management endpoints, before claiming CLIProxyAPI behavior works.
---

# Verify CLIProxyAPI

CLIProxyAPI is an HTTP proxy that exposes OpenAI (`/v1/chat/completions`, `/v1/responses`), Claude (`/v1/messages`) and Gemini (`/v1beta/...`) compatible APIs backed by OAuth'd CLI accounts, plus a Management API under `/v0/management`. Primary surface: **HTTP API**. Secondary: the `cli-proxy-api` binary's flags (`-*-login`, `-tui`, `-discover`). The bundled web control panel is downloaded from GitHub at runtime and is disabled here.

No upstream provider credentials exist in the verification instance (empty `auth-dir`), so real completions are out of scope; drives prove routing, auth, model catalog (`-local-model` embedded catalog, no network fetch) and management behavior.

## One-shot

```bash
.cursor/skills/verify-cliproxyapi/scripts/cpa-verify.sh all                       # launch, doctor, all features, cleanup; exit 0 iff all PASS
.cursor/skills/verify-cliproxyapi/scripts/cpa-verify.sh all models management     # subset
CPA_EVIDENCE=/tmp/cpa-verify-evidence/my-proof .cursor/skills/verify-cliproxyapi/scripts/cpa-verify.sh all models
```

## Launch

`.cursor/skills/verify-cliproxyapi/scripts/cpa-verify.sh launch` — builds `go build ./cmd/server` (~25 s cold) into `$CPA_RUN` (default `/tmp/cpa-verify-run`), writes a scratch `config.yaml` (host 127.0.0.1, random free port, random `api-keys` entry, random `remote-management.secret-key`, `disable-control-panel: true`, `auth-dir` inside the run dir) and starts `cli-proxy-api -config <run>/config.yaml -local-model` with nohup. Ready = `GET /healthz` returns 200; prints `READY pid=… port=… evidence=…`. Never uses `~/.cli-proxy-api` or the repo's `config.yaml`.

Isolation: one instance per `CPA_RUN`. Launch refuses if that run dir's pid is alive. Run side-by-side instances with distinct `CPA_RUN=/tmp/cpa-verify-run-<x>`.

Keys live in `$CPA_RUN/apikey` and `$CPA_RUN/mgmtkey` (mode 600, ephemeral). Do not print them; the harness redacts them from evidence.

## Doctor

`.cursor/skills/verify-cliproxyapi/scripts/cpa-verify.sh doctor` (read-only): pid alive, the port is listened on by that pid (`ss -ltnp`), `/healthz` 200, management key accepted on `GET /v0/management/config`. Prints `OK pid port rev evidence`. Run it whenever anything looks off; `drive` runs it first.

## Drive

`.cursor/skills/verify-cliproxyapi/scripts/cpa-verify.sh drive <feature>` with feature in `health | models | management | chat-completions | cli`. Each sends real HTTP requests via curl to the user routes (no test-only endpoints) and asserts status/body. Manual curl: `B=http://127.0.0.1:$(cat $CPA_RUN/port)`; client auth is `Authorization: Bearer <apikey>`, `x-api-key` or `x-goog-api-key`; management auth is `Authorization: Bearer <mgmtkey>` or `X-Management-Key`. Recipes per feature: [features/README.md](features/README.md).

## Evidence

Location: `$CPA_EVIDENCE` (default `/tmp/cpa-verify-evidence/<timestamp>-<pid>/`), outside the run dir. Per feature dir: `<step>.cmd` (redacted curl line), `.status`, `.headers`, `.body`, plus `server.log.tail`; management also copies `config-after.yaml` (proves the on-disk side effect). Cleanup adds the full redacted `server.log`.

Proof standards: hit the real routes with real auth headers; record both the rejected (no/bad key) and accepted request; for mutations verify the side effect (config.yaml on disk) with a second read, not just the 200. A 4xx from chat-completions without credentials is the expected end state here — do not report it as a completion working.

## Cleanup

`.cursor/skills/verify-cliproxyapi/scripts/cpa-verify.sh cleanup` kills only the pid recorded in `$CPA_RUN/pid` (TERM, then KILL after 5 s), copies a redacted server log into the evidence dir and deletes `$CPA_RUN` (binary, config, keys, auths). Evidence dirs are never removed. Run it after every failed iteration too. Never `pkill cli-proxy-api` — the user may run their own instance.

## Helpers

- `scripts/cpa-verify.sh` — `launch | doctor | drive <feature> | cleanup | all [features…]`. Env: `CPA_RUN`, `CPA_EVIDENCE`. Needs go, curl, python3, openssl, ss.
- Unit/integration tests remain `go test ./...` (see AGENTS.md); this skill is the black-box complement.
