# Maintenance

## Background

Maintained fork: `0xble/CLIProxyAPI` of `router-for-me/CLIProxyAPI`.
Both default branches are `main`. Canonical checkout: `~/Repos/CLIProxyAPI`.
Initial local, `origin/main`, and `upstream/main` baseline on 2026-09-13:
`44e62bc8acc2f224bff9c62d222717d3f6723dea` (upstream v7.3.1), with exact parity.
Accepted upstream baseline before CPA-001: `d318bcc3afb9ea8782862f5e8cdb541afdc9dfa5` (`v8.0.23`).
Current accepted upstream release: `bf9341629ef2300ac9d846a2e80fe2756f63a671` (`v8.0.27`), merged after reviewing the `v8.0.25`–`v8.0.27` release notes. Its canceled-stream cooldown path (`recordCanceledUpstreamCooldown`) carries the CPA-001 explicit cooldown. The new request-body limit and pre-first-token heartbeats remain disabled when unset; session-affinity rebinding retains the existing `any` default. Sign in with ChatGPT is opt-in through credential metadata and does not replace the existing Codex route.
`origin` publishes to the owned fork. `upstream` is fetch-only.
This fork supplies the local gateway build from fork `main` and tracks upstream RELEASE TAGS, not upstream `main`.

## Preserve

- Preserve upstream routing, credential refresh ownership, and protocol compatibility unless a separately accepted feature changes them.
- Keep credentials, account identities, quota snapshots, and live configuration outside this public repository.
- The dedicated `update-cliproxyapi` Hermes job owns synchronization and the authorized local installation. Other maintenance agents must not race that job.
- This fork intentionally diverges from upstream by removing the `agents-md-guard`, `auto-retarget-main-pr-to-dev`, and `pr-path-guard` workflows: fork PRs target `main`, while those guards closed or retargeted them. Re-check this divergence on each upstream tag merge.
- Install only published fork revisions using `cliproxyapi-install <full-sha>`. The installer verifies the active process, binary checksum, and provider inference and restores the previous release on failed activation.

## Active patches

### CPA-001 — request-scoped error cooldown duration

- Required behavior: an optional per-rule `cooldown` Go duration string (for example `1h`) on `request-scoped-errors` sets a minimum cooldown for `stop-and-cooldown` and `continue-and-cooldown` (it replaces the short transient cooldown and never shortens a longer status, provider, or quota deadline); absent `cooldown` preserves the existing behavior. Invalid durations ignore the rule.
- Source surfaces: `internal/config/config_types.go`, request-scoped rule normalization, `sdk/cliproxy/auth` result/cooldown handling, focused config and selection regressions, and the documented example config.
- Rationale: allow credential-specific upstream failures such as exhausted credit to remain unavailable for an explicit period while another credential serves requests. The implementation applies the deadline to the matched model's credential state; unrelated models on the same credential retain existing per-model behavior.
- Upstream source and feedback: closed issue https://github.com/router-for-me/CLIProxyAPI/issues/3858; open upstream contribution PR https://github.com/router-for-me/CLIProxyAPI/pull/6496 at `778618952441794f1709c54ec0ca45098b513b45` against `dev`. Checked 2026-10-13 UTC; its three checks passed, but no released upstream implementation exists.
- Upstream review disposition: the two unresolved automated threads concern Home dispatch and propagation to sibling models. Home dispatch has no local cooldown owner and requires a separately scoped control-plane design; the local gateway does not enable Home. Extending the explicit duration to sibling models would change the accepted per-model contract above. Neither suggestion is adopted by this release-only sync; the upstream contribution still requires its own reconciliation before a Home or credential-wide expansion.
- Upstream disposition: proposed separately on `router-for-me/CLIProxyAPI` against `dev`, with only the CPA-001 implementation and tests.
- Fork delivery: reviewed PR https://github.com/0xble/CLIProxyAPI/pull/11 targets `0xble/CLIProxyAPI:main` and also advances fork `main` to upstream release tag `v8.0.23` (`d318bcc3afb9ea8782862f5e8cdb541afdc9dfa5`).
- Focused regressions: `go test ./internal/config ./sdk/cliproxy/auth -run 'Test(ParseConfigRequestScopedErrors|ParseConfigOAuthRequestScopedErrors|SanitizeOAuthRequestScopedErrors|RequestScopedCooldown|RequestScopedErrors_)' -count=1`; full `go test ./...`; `go build -o /tmp/cliproxyapi-fork-check ./cmd/server`.
- Source rollback: revert the CPA-001 change through a new reviewed PR; runtime rollback remains a separate installation operation.
- Retirement condition: remove the fork implementation only after a released upstream version satisfies the complete cooldown regression contract.

### CPA-002 — finite provider cache observation

- Required behavior: optional `cache-observation` observes only 1 exact session for an absolute expiry within 1h and 1-100 model executions per process. Capture stays disabled by default, preserves outgoing bytes and emits only scoped hashes, marker TTLs and allowlisted usage evidence in the existing application logger.
- Source surfaces: `internal/config/config.go`, Claude/Codex HTTP execution seams, `internal/runtime/executor/helps/cache_observation.go`, the native logger field registry, usage reporter completion, focused helper and mocked transport tests, and `docs/cache-observation.md`.
- Rationale: the cache audit observed reuse but could not correlate final signed/normalized prefixes with actual usage. The existing plugin interceptor runs before those transforms, and general request logging retains raw bodies and headers.
- Upstream preflight: root guidance at `bf934162` requires helpers under `helps/`, payload rules as the final semantic barrier, and secret-free logging. Searches of open/closed/merged cache and usage work found no equivalent finite final-boundary observer. Upstream issue #6273 was closed as `NOT_PLANNED` on 2026-10-02 and rejects custom TTL fields in translated OpenAI schemas, so private observation preserves raw counters without changing those schemas. Checked 2026-10-13 UTC: issue #6561 is closed by PR #6560 (`1fbb06d5ddd5e0e56271698add7dabcaccfc5924`); that separate tool-search quota/cache fix is not an ancestor of `v8.0.27` and is not adopted by this release-only sync.
- Verification: `go test ./internal/runtime/executor/helps ./internal/runtime/executor ./internal/config ./sdk/cliproxy/usage`, race tests on observation paths, full `go test ./...`, and server build. No inference traffic is needed to prove the observer. Actual provider reuse/economics remain separate evidence.
- Upstream association: none. This is an explicitly approved fork-local measurement capability. A public upstream contribution requires separate authority.
- Verification prerequisite: upstream test-only commit `77cf88b0883d48b45a677e26e26739448e09cb2e` by Gracjan Kubicki pins the tool-search golden clock and resets diagnostics. It shipped in `v8.0.26` and is included by the `v8.0.27` merge, so it is no longer a fork-only prerequisite. The previously accepted release `091ff27f` reproduced the date-dependent failure. No production request behavior or golden assertion was changed.
- A second test-only prerequisite synchronizes the HTTP interrupt fixture on its cancellation callback, rather than racing an immediate atomic flag read after the socket acknowledgement. The same intermittent failure reproduced on untouched `091ff27f`. The assertion still requires actual upstream cancellation and socket continuation.
- Retirement condition: adopt a released upstream observer only when it preserves the finite scope, exact final request boundary, privacy and per-execution usage join.

### CPA-003 — retain Codex rotations and recover the rejected active partition

- Required behavior: a successful OAuth rotation remains usable by the manager when the final credential re-read, JSON decode, atomic write, or subsequent FileTokenStore persistence fails. Its existing immutable `CodexRefreshPatch` must survive the next disk reconciliation and carry only affected partitions into the next compare-and-merge write. A 401 for the matching active Codex token marks that partition rejected even with a known future expiry; recover it without unnecessarily refreshing a healthy inactive partition.
- Upstream origin: both regressions were introduced in `7b0319fb1c191cdae1e5936ecb81e812dcf0a0b2` (Sign in with ChatGPT), included in release `v8.0.27` (`bf9341629ef2300ac9d846a2e80fe2756f63a671`). Governing release guidance requires small changes, existing refresh ownership and secret-free logging. Checked 2026-10-13 UTC: latest released tag and upstream main still point to this release; no released equivalent covers these reproductions. Do not take unreleased development changes during a release-only sync.
- Related upstream work, not this fork's contributions: closed [#6197](https://github.com/router-for-me/CLIProxyAPI/issues/6197) discusses non-fatal refresh writes; open [#6295](https://github.com/router-for-me/CLIProxyAPI/pull/6295) hardens atomic storage but explicitly excludes refresh-manager propagation. Open [#4088](https://github.com/router-for-me/CLIProxyAPI/issues/4088) discusses 401 recovery before dual partitions; closed [#3783](https://github.com/router-for-me/CLIProxyAPI/issues/3783) concerns refresh concurrency. Open [#6165](https://github.com/router-for-me/CLIProxyAPI/pull/6165) caches completed grants for late callers, not pending-patch disk reconciliation. No exact public issue/PR association or public contribution is created by this patch.
- Source surfaces: `internal/runtime/executor/codex_executor_auth.go`, partition inspection on `internal/auth/codex/refresh_patch.go`, and Codex-only rejection recording in `sdk/cliproxy/auth/conductor_refresh.go`. Reuse FileTokenStore, runtime/storage merging, refresh epochs and partition due-state; add no global forced-refresh behavior or second retry owner.
- Durable regressions: `TestCodexManagerRefresh_RotatedTokensSurvivePersistenceFailure` covers real filesystem write/read permission failure and invalid final JSON, retained runtime rotations, subsequent refresh with the new grant, eventual persistence, concurrent operator edits and independently updated healthy SIWC credentials. `TestCodexManagerRefresh_UnauthorizedRefreshesRejectedPartition` runs both active choices with due and healthy inactive partitions through the real manager/executor/HTTP/FileTokenStore boundary. All credentials are synthetic and all provider traffic is stubbed, including the explicit execution round-tripper context. Permission cases skip only where Unix permissions cannot be enforced.
- Red/green evidence: the pre-fix PR head `fd7b8887be3d2d23761d947b0247eccf52960534` fails both original reproductions. A production-source overlay at that head fails all seven durable cases, including the healthy-inactive unnecessary-refresh checks. The fix passes all seven under a network-denying sandbox. Reuse existing credential patch/FileTokenStore tests for independent concurrent rotations and auth manager tests for superseded epochs and runtime-only state. Require full `go test ./...`, relevant package `-race`, `go vet ./...` and a server build before promotion; provider canaries belong to the separately authorized activation owner.
- Retirement condition: remove only after a released upstream implementation passes this complete manager/executor/file-store regression contract, including subsequent refresh recovery and healthy inactive partitions. Revert the focused fork change for source rollback; installation and runtime rollback remain separate operations.

Use stable IDs `CPA-004` and onward for future logical patches.
Upstream maintenance-contract issue/PR associations: none found when checked 2026-09-13.
Retain this contract while maintaining the fork. Retire behavioral patches only after a released upstream implementation passes their complete regression contract.

## Update

Fetch `origin` and `upstream` separately and record exact SHAs. Reconcile with the latest upstream RELEASE TAG, retaining justified fork changes and checking current upstream feedback for every active patch.
Deliver changes through task branches and PRs targeting `0xble/CLIProxyAPI:main`, never the upstream repository by default.
Use targeted tests and inspect the fork-only diff. Before declaring synchronization current, fetch the selected upstream RELEASE TAG again and require that tag is an ancestor of fork `main`.
Keep patch records current when behavior or upstream disposition changes. Do not create recurring jobs from this contract.
For source rollback, revert the identified fork change through a new reviewed PR. Preserve source recovery refs before history changes. Runtime rollback is a separate deployment operation.

## Verify

Follow `AGENTS.md`. For Go changes run `gofmt`, relevant regressions, `go test ./...`, and `go build -o /tmp/cliproxyapi-fork-check ./cmd/server`.
For routing changes include weighted selection, session affinity, and quota failover fixtures under `sdk/cliproxy/auth` and `test`.
Documentation-only changes require diff inspection and a compile check. Runtime changes require real Codex Responses/tool-result and Claude Messages canaries in addition to source tests.
After publication verify local `main` equals `origin/main`, and the selected upstream RELEASE TAG is an ancestor of fork `main`.
Report source, publication, installation, and active runtime state separately. A maintenance run succeeds only after the published fork revision is active and verified. Preserve the prior release for rollback.

Baseline: `go test ./...` passes fully with no accepted failing tests. `TestOpenAICompatExecutorToolResultContentByInputModalities` was an accepted inherited failure from 2026-09-13 through the 2026-09-15 sync; it passes as of upstream `b773607e3e7756dc6020a291825e4eb08899595a` and is retired. Any test failure now blocks promotion. If a future inherited failure appears, reproduce it on an untouched upstream checkout first, record the exact test name and upstream SHA here, and report it separately only while unchanged-source proof, routing/quota tests, build, and live Codex/Claude canaries pass.
