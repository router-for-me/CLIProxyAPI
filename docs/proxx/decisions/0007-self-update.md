# 0007: Self-update in place

Date: 2026-11-17. Status: accepted.

## Context

Users install proxx with `install.sh` (launchd agent on macOS, systemd user unit on Linux) and update by
re-running it. The panel should be able to update the backend, and updates must not break running clients.

An audit showed that two proxx processes running side by side corrupt state:
- auth files are rewritten from memory, so a draining old process writes back a rotated (dead) Claude refresh token;
- bbolt `kv.db` holds an exclusive flock;
- the usage snapshot is last-writer-wins.

So exactly one proxx process may run at any time. Blue/green or socket-handoff designs with two live
processes are out.

## Decision

Restart in place when the server is idle: same PID, same listening socket, with no second process.

### Check
- Every `proxx.update.interval-hours` (default 24; the first check runs 30s after start) and on demand, GET
  `https://api.github.com/repos/iamkunal9/proxx/releases/latest` with `If-None-Match`.
- `buildinfo.Version` is compared with the release tag: `MAJOR.MINOR.PATCH[-proxx.N][-rcM]`, where an rc sorts
  below its final release.
- Only a strictly newer version is an update. Downgrades from the network are never offered, nor are
  drafts or prereleases. Unparseable versions (e.g. `dev`) never update.
- The update source (`apiBaseURL`, `repo`, `releasePath`), `allowPrerelease` and the signing public key are
  package variables in `internal/proxx/selfupdate/source.go`. They can be changed **only** with
  `-ldflags -X` (tests, release pinning) and are never read from config, environment or a request.

### Download and verify
1. Asset: `proxx_<ver>_<os>_<arch>.tar.gz`, where arch is `amd64` or `aarch64`. A Linux build without CGO
   (the release `_no-plugin` build) picks `_no-plugin`.
2. Download `checksums.txt` and `checksums.txt.sig`. The signature is ed25519 over the exact bytes of
   `checksums.txt`; the file holds the base64 of the 64-byte signature. It is verified with the embedded
   key `f1FBolCTWjL8K973t2XK+u+TzLp7ZnVybU/v8NgOFxc=`. A release without a `.sig` is refused.
3. The archive's sha256 must match `checksums.txt`. The top-level `proxx` file is then extracted.
4. It is written to `<exe>.new` in the install directory, and `<exe>.new --proxx-selfcheck <config>` runs.
   The self-check prints the version, which must equal the release version, and loads a **copy** of the
   config from a temp dir, because loading rewrites the file.

This is state `staged`.

### Apply when idle
- `selfupdate.Track` wraps the server's root handler and counts in-flight work:
  - every proxied request, from handler entry to return (SSE streams included);
  - every hijacked connection (Codex WS, wsrelay, responses WS, wstunnel) until it is closed. Hijacks are
    counted through the ResponseWriter, because `http.Server` does not track hijacked connections.
- Management requests (`/v8/management`, `/v0/management`, `/management.html`) are **not** counted. The panel
  polls and keeps live-tail streams open, which would otherwise block an update forever. Their contexts are
  cancelled right before the restart instead, which ends those streams.
- The status reports the count as `inflight`.
- Wait until the count is 0. Then:
  1. For 1s, every response carries `Connection: close`.
  2. The listener is paused: `Accept` is not called, so new connections queue in the kernel backlog and are
     not refused.
  3. Wait up to 2s more for the count to reach 0. If anything is still running, unpause, stop draining and
     wait again.
- A stream is never cut. If the server never goes idle, the apply keeps waiting until it is cancelled.
- To restart:
  1. `dup` the listening socket (`F_DUPFD_CLOEXEC`), cancel the management contexts, and call the restart
     trigger. The trigger is the `signal.NotifyContext` stop func registered in `internal/cmd/run.go`, so
     the **normal graceful shutdown** runs: drain, watchers and refresh loops stopped, `kv.db` closed, usage
     snapshot flushed, `proxx.Shutdown`.
  2. In `main`, after `run()` returns, `selfupdate.Finish`:
     - re-hashes `<exe>.new` against the verified digest;
     - writes the state file (`pending_version`, `prev_path`, `attempts: 0`);
     - renames `<exe>` → `<exe>.prev` and `<exe>.new` → `<exe>` (it never writes into the running file);
     - clears close-on-exec on the dup'd fd;
     - calls `syscall.Exec(exe, os.Args, env + PROXX_LISTEN_FD=<fd>)`.
- The new image's `selfupdate.Listen` (the hook where the server called `net.Listen`) adopts the fd with
  `net.FileListener`, checks that the port matches, and unsets the variable.
- launchd and systemd see the same PID. The socket never closes, so connections made during the restart
  wait in the backlog and are served by the new version.
- If the exec fails, `Finish` restores `<exe>.prev`, records the error and exits 1, and the supervisor
  restarts the old version. Restarting in-process is not feasible: `run()` parses global flags and has
  already torn everything down.

### Rollback
- `selfupdate.Startup()` runs first in `main()`. It reads `<config dir>/proxx-update-state.json`. When
  `pending_version` equals the running version, it increments `attempts`.
- The new process confirms its health by requesting its own `GET /healthz` (http, then https). It then
  clears the pending fields and writes `last_result {from,to,at,ok:true}`.
- A process that is not healthy within 5 minutes exits with 1, which counts as a failed start.
- On the 3rd start without health (a crash loop under launchd KeepAlive or systemd `Restart=on-failure`),
  `Startup` renames `<exe>.prev` back and execs it, recording `last_result {ok:false, rolled_back:true, error}`.
- If a different version starts while an update is pending (e.g. a manual restore), the pending update is dropped.

### Supported
Supported means all of:
- linux or darwin;
- not a container (`/.dockerenv`, `/run/.containerenv`, or `docker`/`kubepods`/`containerd`/`libpod` in
  `/proc/1/cgroup`; Linux only);
- not a Homebrew `Cellar` path;
- running as the server, i.e. the restart trigger is registered (TUI and embedded modes are not);
- the executable's directory is writable.

Otherwise the status says `supported:false` with a reason that says how to update instead. Windows (and
FreeBSD) compile and get notify-only: check and status work, apply returns 409.

### Auto-apply
Config `proxx.update.auto-apply` (default false) and `window` (`HH:MM-HH:MM` local time, may wrap
midnight; empty, or equal start and end, means any time).
- When a check finds an update, it is downloaded, verified and staged right away.
- It is applied at the next idle moment inside the window. Outside the window the state is `staged`.
- The whole `proxx.update` block hot-reloads. Turning auto-apply off cancels a pending auto apply.
- A release that failed or was cancelled is not retried automatically until the release itself changes (a
  new ETag or body).

### Default drain
Upstream `server.shutdown-timeout` defaults to 0, which closes every stream at once on any restart, installer
upgrades included.
- proxx sets an unset (0) value to **120s** in the facade (`internal/proxx/proxx.go`, on start and on every
  reload). A negative value keeps the immediate close.
- Upstream's `Run` caps the whole shutdown at 30s. The hook in `service_lifecycle.go` replaces that with
  `proxx.ShutdownContext`, which allows max(30s, drain + 15s). A watchdog exits the process if shutdown
  overruns that budget by 15s, so proxx always stops on its own.
- install.sh:
  - systemd gets `TimeoutStopSec=130`.
  - launchd gets `ExitTimeOut 0`. launchd caps agent `ExitTimeOut` at 60s (verified on macOS: 130 is
    reported as 60, while 0 means no limit), and the watchdog above bounds the stop instead.
  - The installer waits up to ~135s for the old process to exit.

### Panel/backend compatibility
- The status includes `api_level` (`selfupdate.APILevel`, currently 1).
- A panel release can declare a line `proxx-min-backend-api: N` in its release body, at the start of a line,
  optionally inside an HTML comment: `<!-- proxx-min-backend-api: 2 -->`.
- When N is greater than the backend's level, the panel updater (`internal/managementasset`) skips that
  release and keeps the installed panel.
- Bump `APILevel` when the panel starts depending on a new backend feature.

### Security
- All endpoints sit behind the management secret (the `/v8/management` middleware).
- `apply` reads no body or query: it applies only the latest release that verified against the embedded
  key. Signature and checksum failures leave the running binary untouched.
- No secrets are logged. The update source cannot be redirected at runtime.

### Release pipeline
- `.github/workflows/release.yaml` `publish-checksums` builds `cmd/proxx-sign`.
- `proxx-sign` reads the base64 32-byte seed from the repo secret `PROXX_RELEASE_SIGNING_KEY` and writes
  `checksums.txt.sig`. The same tool then verifies the signature against the key embedded in `source.go`,
  so a key mismatch fails the release. Both files are uploaded.
- Tags containing `-rc` are created or edited as GitHub prereleases, so they never become "latest".

## API (JSON snake_case, management auth)

| Method | Path | Result |
|---|---|---|
| GET | `/v8/management/proxx/update` | status (below) |
| POST | `/v8/management/proxx/update/check` | 200 + status after the check |
| POST | `/v8/management/proxx/update/apply` | 202 + status; 409 `{error}` when unsupported or nothing newer |
| POST | `/v8/management/proxx/update/cancel` | 200 + status; 409 `{error}` when nothing is pending or the restart already began |

Status:

```
{api_level, current_version, latest_version, update_available, release_url,
 release_notes (markdown, truncated to 32 KB), checked_at (RFC3339|null), last_check_error,
 supported, unsupported_reason,
 state: idle|checking|downloading|verifying|staged|waiting_idle|applying|failed,
 inflight, staged_version, config:{check, interval_hours, auto_apply, window},
 last_result:{from,to,at,ok,rolled_back,error}|null,
 last_error, pid}
```

`last_error` and `pid` go beyond the panel contract. `last_error` explains `state: failed` (download or
verification errors), and `pid` lets tests and the panel confirm the PID is unchanged.

Config writes use the generic v8 config endpoint with a raw JSON body, exactly like ADR 0006:
- `PUT /v8/management/config/proxx/update/check`: `true`/`false`.
- `PUT .../proxx/update/interval-hours`: an integer.
- `PUT .../proxx/update/auto-apply`: `true`/`false`.
- `PUT .../proxx/update/window`: a string such as `"02:00-05:00"`, or `""`.

## Hooks (upstream files)

| File | Hook |
|---|---|
| `cmd/server/main.go` | `selfupdate.Startup()` and `os.Exit(selfupdate.Finish(run()))` in `main` (+ import) |
| `internal/cmd/run.go` | `selfupdate.SetRestartTrigger(cancel)` after `signal.NotifyContext` (+ import) |
| `internal/api/server.go` | `selfupdate.Track(...)` around the root handler; `selfupdate.Listen(addr)` instead of `net.Listen` (+ import) |
| `sdk/cliproxy/service_lifecycle.go` | `proxx.ShutdownContext(...)` instead of the 30s `context.WithTimeout` |
| `internal/managementasset/updater.go` | `proxxPanelCompatible(data)` after decoding the panel release |
| `.github/workflows/release.yaml` | prerelease flag for `-rc` tags; checkout + setup-go + sign/verify/upload `checksums.txt.sig` in `publish-checksums` |
| `install.sh` (fork-owned) | `ExitTimeOut`, `TimeoutStopSec`, longer stop wait, `.install-method`, update hint, `PROXX_SERVICE_LABEL` |

## Limitations
- Redis RESP connections on the shared port (usage queue consumers) are not counted. They are closed at the
  restart, and consumers reconnect.
- Long-lived WebSocket peers (wsrelay providers, wstunnel) keep the count above 0. The update then waits
  until they disconnect or the apply is cancelled; `inflight` in the status shows why.
- Keep-alive race: a client that sends on an idle keep-alive connection at the exact moment the old process
  closes it gets a connection error, which Go's transport retries only for replayable requests. The
  `Connection: close` second and the paused listener make this rare. The e2e counts such retries
  separately, and saw 0.
- A new binary that crashes before `main()` (in a package `init`) cannot count its own attempts. The
  `--proxx-selfcheck` run before install catches that case.
- With store backends (Postgres, git, object store), the state file still lives next to the `--config` path.

## Tests
- Unit tests (`go test -race ./internal/proxx/selfupdate/ ./internal/proxx/ ./internal/managementasset/`):
  - signature (good, modified, wrong key, garbage, short);
  - checksum mismatch and extraction; asset names;
  - version order, no downgrade, and prerelease/draft ignored;
  - release parsing and ETag/304;
  - windows, including wrap past midnight; the state file round trip;
  - the rollback attempt counter (3rd start restores `.prev` and execs it) and the health confirmation;
  - supported detection (fake environment and a real unwritable dir);
  - the in-flight counter with a hijacked WebSocket that outlives its handler, and management cancellation;
  - `Connection: close` while draining, and the paused listener queueing instead of refusing;
  - auto-apply staying staged outside the window;
  - the drain default, shutdown budget and watchdog; the panel `proxx-min-backend-api` skip.
- E2E: `PROXX_E2E=1 go test ./test/proxx/selfupdate_e2e/ -run TestE2ESelfUpdate -v -count=1 -timeout 30m`.
  - Setup:
    - builds v1–v4 plus a downgrade build with ldflags (version, a test key, the mock API base);
    - a mock GitHub serves the release JSON (with ETags), archives, `checksums.txt` and `.sig`;
    - a mock upstream serves fast and slow SSE;
    - a supervisor restarts the process on exit, like KeepAlive.
  - Scenarios:
    1. 20 rps keep-alive load across the upgrade, with zero failures (connection-error retries reported).
    2. A 6s stream started before apply finishes with every chunk, and v2 answers only after it ended.
    3. Same PID, no supervisor restart, v2 reported, `last_result.ok`.
    4. A bad signature, a tampered archive and a downgrade are refused, and v1 keeps serving.
    5. A crash-on-start v4 rolls back to v3 within 3 starts, with `rolled_back:true`.
    6. Auto-apply stays staged outside the window and applies inside it.
    7. Usage counted before the upgrade is still there after it.
    8. An unwritable install dir reports `supported:false` and apply returns 409.
  - CI: `.github/workflows/proxx-e2e.yml` runs this on ubuntu-latest and macos-latest.
- Real launchd: `PROXX_E2E=1 PROXX_E2E_LAUNCHD=1 go test ./test/proxx/selfupdate_e2e/ -run TestE2ELaunchd -v -count=1`.
  It runs on macOS only, with a temporary agent label, a free port and temp dirs, and never touches the
  user's `io.github.iamkunal9.proxx`.

## Pinning a release candidate (pre-merge release test)
Build a binary that reads one tagged prerelease instead of "latest":

```bash
go build -ldflags "-X main.Version=<older version> \
  -X github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/selfupdate.releasePath=releases/tags/<rc tag> \
  -X github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/selfupdate.allowPrerelease=true" -o proxx ./cmd/server
```
