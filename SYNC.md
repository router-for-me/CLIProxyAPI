# Fork maintenance & upstream sync (SYNC.md)

This fork is a **soft fork** of `router-for-me/CLIProxyAPI`: the Go module path is
kept unchanged and ~80% of the fingerprint-hardening logic lives in **new files**
under `internal/runtime/executor/helps/`, so upstream merges are low-friction. The
files that touch upstream code (the conflict surface) are kept to small call-site
hooks.

There are **two independent kinds of "keeping up"**:

1. **Merging upstream code** — automated below. `git merge upstream/main` works
   because the module path is preserved.
2. **Re-calibrating fingerprint *values*** — separate manual task, because the real
   `claude` / `codex` clients keep updating. See "Fingerprint re-capture".

---

## Automation (GitHub Actions)

| Workflow | Trigger | What it does |
|---|---|---|
| `.github/workflows/fork-ci.yml` | push / PR to `feat/fingerprint-hardening` or `main` | gofmt check + `go build ./...` + `go test ./...` (self-contained gate) |
| `.github/workflows/fork-sync.yml` | daily cron 03:00 UTC + manual | merge `upstream/main`; **clean+green → push**; **conflict → open a `sync/*` PR**; broken build/test → red run, nothing pushed |
| `.github/workflows/fork-image.yml` | push to mod branch / `v*` tag | build + push Docker image to `ghcr.io/<owner>/cliproxyapi` (`:latest`, `:<branch>`, `:sha`) using `GITHUB_TOKEN` — no extra secrets |
| `.github/workflows/fork-release.yml` | `v*` tag / manual | cross-compile Linux **static** binaries (CGO=0) amd64+arm64, package with config + systemd unit + `install.sh` + `DEPLOY.md`, publish to **GitHub Releases** + checksums |

Flow (day-to-day): `fork-sync` merges upstream → pushes → that push triggers
`fork-ci` (verify) and `fork-image` (publish image). Deploy is an optional,
commented step in `fork-image.yml` (fill in your SSH/registry target + secrets).

Flow (release): push a `v*` tag → `fork-release` (static tarballs → GitHub
Releases) and `fork-image` (multi-arch image → GHCR) run in parallel. Both use
only `GITHUB_TOKEN`.

### One-time setup on the fork (you must do these in the GitHub UI)

1. **Settings → Actions → General → Workflow permissions →** select **"Read and
   write permissions"** (lets `fork-sync` push merges and open PRs, and
   `fork-image` publish to GHCR).
2. **Settings → Actions → General →** ensure Actions are **enabled** for the fork.
3. **Disable interfering upstream workflows on the fork** (Actions tab → pick the
   workflow → ⋯ → Disable). Disabling via the UI does not delete the files, so
   upstream merges stay clean.
   - **Must disable** `release` and `docker-image`: upstream `release.yaml` triggers
     on `tags: ['*']` (every tag) and would race `fork-release` for the same GitHub
     Release; `docker-image.yml` needs DockerHub secrets this fork doesn't have and
     will fail. `fork-release` + `fork-image` replace both.
   - **Should disable** (upstream-governance, mis-fire here): `pr-path-guard`,
     `agents-md-guard`, `auto-retarget-main-pr-to-dev`.
4. First image publish makes the GHCR package **private** by default — make it
   public under the repo's *Packages* if you want to `docker pull` without auth.
5. (Optional) adjust the cron cadence in `fork-sync.yml`. Daily is a good default;
   weekly means fewer, larger merges.

---

## Releasing (打 tag 出产物)

Cut a versioned release — this produces the deployable server artifacts:

```bash
git checkout feat/fingerprint-hardening
git pull
# choose a version; keeping upstream's vX.Y.Z base + a fork suffix is tidy:
git tag v7.2.49-fp1 -m "fingerprint-hardening release"
git push origin v7.2.49-fp1
```

That tag triggers, in parallel (both `GITHUB_TOKEN`-only):
- **`fork-release`** → `CLIProxyAPI_<ver>_linux_amd64.tar.gz` + `_arm64.tar.gz`
  (static, CGO=0) + `checksums.txt` attached to the GitHub Release. Each tarball
  contains the binary, `config.example.yaml`, the systemd unit, `install.sh`, and
  `DEPLOY.md` — `sudo ./install.sh` on the server does the rest.
- **`fork-image`** → multi-arch image at `ghcr.io/<owner>/cliproxyapi:<tag>`.

Manual dry-run without tagging: Actions → `fork-release` → *Run workflow* builds
the tarballs as run **artifacts** (no Release published).

> Prerequisite: upstream `release` + `docker-image` disabled (see step 3 above),
> and "Read and write" workflow permissions (step 1).

---

## Manual merge (fallback / when you want control)

```bash
git fetch upstream
git checkout feat/fingerprint-hardening
git merge upstream/main            # or: git rebase upstream/main
# resolve conflicts if any (see conflict-prone files below)
gofmt -w . && go build ./... && go test ./...
FP_VERIFY=1 go test ./internal/runtime/executor/helps/ -run TestFingerprintAgainstReporter  # fingerprint safety net
git push
```

### Conflict-prone files (where this fork edits upstream code)

Ranked by how much fork code sits in them (bigger = more likely to conflict when
upstream also changes them). Everything else the fork adds is in **new files** and
never conflicts.

1. `internal/runtime/executor/helps/utls_client.go` — `NewUtlsHTTPClient` rewrite
2. `internal/runtime/executor/codex_websockets_executor.go` — UA/originator guards
3. `internal/runtime/executor/claude_executor.go` — Accept-Encoding, device-header
   injection, dateline hook
4. `internal/runtime/executor/codex_executor.go` — codex UA constant + UA guard
5. `internal/config/config.go` — new config flags (additive; easy)
6. `internal/runtime/executor/openai_compat_executor.go`, `kimi_executor.go`,
   `helps/claude_device_profile.go` — small hooks/constants

**Escape hatch:** every feature has a `disable-*` config flag. If upstream
refactors a subsystem and re-applying a hook is hard, disable that feature, merge
clean, and re-apply the hook later.

### Post-merge checklist
- [ ] `gofmt -l .` clean
- [ ] `go build ./...` and `go build -o /tmp/x ./cmd/server`
- [ ] `go test ./...` green
- [ ] `FP_VERIFY=1 go test ./internal/runtime/executor/helps/ -run TestFingerprintAgainstReporter` → node/h1 JA3 `44f88fca…`, chatgpt h2 OK
- [ ] fingerprint values still current (see below)

---

## Fingerprint re-capture (the other maintenance)

Upstream code merges do **not** keep fingerprint *values* current — the real
clients themselves change (UA/pkg versions, header order, Accept-Encoding…). When
`claude` / `codex` update, re-capture and update the constants/pools.

Method (all local, nothing leaves the machine; redact/delete captures after):

```bash
# 1. tiny local raw-TCP server that logs request bytes (see git history of this
#    session for capture_server.py), on 127.0.0.1:PORT
# 2. Claude:
ANTHROPIC_BASE_URL=http://127.0.0.1:PORT claude -p "hi" --model claude-3-5-haiku-20241022
# 3. Codex (custom/OAuth provider):
codex exec --skip-git-repo-check -c 'model_providers.<name>.base_url="http://127.0.0.1:PORT/v1"' "hi" </dev/null
```

Then update, if changed:
- `helps/device_profile_pool.go` — `claudeProfilePool` (cli/pkg/node versions), `codexUAPool`
- `helps/claude_device_profile.go` — default UA / package / runtime constants
- `claude_executor.go` — `Accept-Encoding` value, `X-Stainless-Timeout` default
- `helps/utls_h1_ordered.go` — `headerWireCasing` / `transportHeaderTrailer` if the
  order or casing changed (assert with `TestWriteOrderedRequest_MatchesRealCaptureOrder`)
- `codex_executor.go` — `codexUserAgent` / `codexOriginator`

TLS JA3/JA4 (`helps/utls_profiles.go`) rarely changes (Node/OpenSSL). Verify with
the `FP_VERIFY=1` test against `tls.peet.ws`.

---

## Known follow-up (documented, not yet done for stability)
- **Codex `thread_id` alignment**: real codex sends `session_id`+`thread_id`
  (underscore); this fork still emits `Conversation_id` / `Thread-Id` on some
  paths. `session_id` is already correct. Aligning fully touches routing +
  prompt-cache across `codex_executor.go:395/1506` and
  `codex_websockets_executor.go:889` plus tests — do it in a focused pass.
- chatgpt.com HTTP/2 SETTINGS fingerprint; Gemini host profile.
