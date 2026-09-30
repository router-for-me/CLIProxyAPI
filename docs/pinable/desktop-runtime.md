# Pinable Desktop runtime distribution

## Scope and Desktop contract

`Pinable desktop runtime` builds CLIProxyAPI component runtimes, not Desktop
installers. The workflow and stdlib Python tools are fork-owned and separate from
upstream release workflows. Dependencies, model catalogs, release credentials and
Desktop's Go build orchestrator are not changed.

The supplied Desktop build/release document requires pinned source revisions and
SHA256 values, generated assets outside Git, native Windows/macOS/Linux packages,
and fail-fast component validation before Wails. It supports an absolute binary
path through `CLIPROXYAPI_COMPONENT_ASSET`.

That document does not define the Desktop `component-lock.json` schema or the
exact `CLIPROXYAPI_COMPONENT_ASSET_DIR` filenames. No Desktop source or lock schema
was retrieved through the connected repositories during this change. Therefore
`runtime-index.json` is a distribution manifest, NOT a drop-in replacement for
`pinable-desktop/internal/assets/cliproxyapi/component-lock.json`. These tools do
not invent, rewrite or relax that lock. End-to-end Wails packaging is not claimed.

## Targets and portable profile

| Target | Native runner | Archive |
| --- | --- | --- |
| windows/amd64 | windows-2025 | ZIP |
| windows/arm64 | windows-11-arm | ZIP |
| darwin/amd64 | macos-15-intel | tar.gz |
| darwin/arm64 | macos-15 | tar.gz |
| linux/amd64 | ubuntu-22.04 | tar.gz |
| linux/arm64 | ubuntu-22.04-arm | tar.gz |

All use `desktop-portable`: CGO=0, baseline CPU settings, `-trimpath`,
`-buildvcs=false`, exact go.mod toolchain, and explicit version/full source SHA/
source commit timestamp in the executable. The builder requires clean source and
does not fetch source, refresh model catalogs, or modify source files. Normal Go
dependency downloads retain checksum verification. Linux binaries are checked for
absence of a dynamic interpreter.

Dynamic-library support is platform-specific. CGO=0 removes that loader on
macOS/Linux; Windows retains the existing syscall-based DLL loader. Manifests
record this difference and `dynamic_library_plugins_tested=false`: this pipeline
certifies the core runtime, not third-party plugins. No plugins are bundled.

macOS thin amd64/arm64 executables are ad-hoc signed and verified before hashing.
They are not Universal 2, Developer ID signed or notarized. Windows executables
are unsigned. Final application signing/notarization remains the Desktop release
operator's responsibility. No signing secrets are required or changed here.

## Workflow, downloads and optional publication

Actions workflow: **Pinable desktop runtime** (`.github/workflows/pinable-runtime.yml`).

- Every main push builds current source, including merged weekly upstream syncs.
- Packaging-related PRs produce native-tested candidate downloads.
- Run workflow builds the selected branch; `version` is optional.
- Only explicit `publish_release=true` on main publishes a dedicated
  `pinable-runtime-<version>` prerelease after all gates pass. Default: false.

Individual `pinable-runtime-<os>-<arch>` Actions artifacts are retained 30 days;
`pinable-runtime-all` is retained 90 days. Download URLs appear in the run summary.
Actions downloads require GitHub access. For durable public downloads use the
explicit prerelease path; published release assets are not temporary Actions files.

Publication validates the complete set again, requires main still at the tested
SHA, refuses existing tags, creates a draft, uploads without clobbering, and only
then publishes it. Failed uploads leave a draft for inspection. It never replaces
the upstream latest release or deploys. The standard Actions token is used.
The new workflow has no tag trigger. Do not manually push `pinable-runtime-*`
tags: the independent upstream release workflow has an all-tags trigger; only the
manual publication job should create these runtime releases.

## Artifact contract

Each archive is named `CLIProxyAPI_pinable_<version>_<goos>_<goarch>.zip` on
Windows or `.tar.gz` on Unix. Each has `<archive-name>.manifest.json` alongside it.
Inside are exactly three regular files:

```text
cli-proxy-api[.exe]
LICENSE
runtime.json
```

No config, .env, credentials, auth directory, plugins, web management assets or
Desktop managed launcher are included. Archives have deterministic timestamps,
member order, ownership and modes; license line endings are normalized to LF.
Build time is the source commit timestamp, not packaging wall-clock time. Archive
determinism for identical input binaries is tested; bit-identical output across
different compiler/signing hosts is not claimed.

Manifest fields cover schema/profile, version, target, source repository/full SHA/
time, Go version, build/signing policy, host contract version 1, capabilities,
license checksum, binary size/hash and archive size/hash. Embedded runtime.json
contains the same record without the recursive archive checksum. The unpacked
binary hash is distinct from the archive hash and Actions ZIP digest.

The complete artifact contains six archives, six manifests, runtime-index.json,
SHA256SUMS and validation.json. Individual artifacts also contain smoke-report.json;
the complete set embeds all six reports in validation.json.

## Desktop handoff

Approve the source SHA and **unpacked binary SHA256** against Desktop's lock before
using a candidate. Obtain manifest and archive from the same trusted run/release.
Never accept a download solely because an untrusted accompanying checksum matches.

From a CLIProxyAPI checkout with Python 3.10+:

```bash
python scripts/pinable/runtime.py verify \
  --manifest /absolute/downloads/<archive-name>.manifest.json \
  --expected-commit <approved-full-source-sha> \
  --expected-sha256 <approved-unpacked-binary-sha256>

python scripts/pinable/runtime.py stage \
  --manifest /absolute/downloads/<archive-name>.manifest.json \
  --expected-commit <approved-full-source-sha> \
  --expected-sha256 <approved-unpacked-binary-sha256> \
  --output /absolute/component-staging/cli-proxy-api
```

Use cli-proxy-api.exe on Windows. Staging requires a new absolute destination,
never replaces a running/existing binary, rejects unsafe archive members, validates
PE/Mach-O/ELF architecture, and checks metadata and both hashes before writing.

Then use Desktop's existing single-target override in the **Desktop repository**:

```powershell
$env:CLIPROXYAPI_COMPONENT_ASSET = 'C:\components\windows-amd64\cli-proxy-api.exe'
go run ./pinable-scripts/cmd/pinable-build doctor --package
go run ./pinable-scripts/cmd/pinable-build package --platform windows
```

On Unix, export the absolute CLIPROXYAPI_COMPONENT_ASSET path and use
`--platform macos` or `--platform linux`. CodeGraph remains separately required.
An older source revision or different binary hash in Desktop's lock must still
fail. Approving this profile may require a reviewed lock/tooling change there.
Its old `components --update-lock` recipe is not guaranteed to reproduce these
hashes. This pipeline does not bypass that mismatch.

## Quality and native execution gates

The normal CGO-profile full Go suite and existing eight sync-script regressions
run on Linux. Distribution and publication-policy tests use isolated fixtures and
mocks; they do not count as actual runtime or GitHub-release execution.

Each of six native architectures runs portable host regressions, builds the
server, verifies its archive and exercises the exact packaged executable:

- both version flags without config or file creation;
- new and legacy discovery output as valid JSON;
- authenticated contract version, full SHA, timestamp, PID and runtime version;
- public/management credential separation and rejection of unauthenticated shutdown;
- runtime key surviving a proven atomic config-key rotation without persistence;
- authenticated loopback shutdown and monitored parent exit;
- macOS signature validity after extraction.

The complete download is only emitted after quality and all six native jobs pass.
No live provider accounts/API keys are used; local tests are not live API validation.
Existing repository CI remains enabled on the actual PR merge result.

## Atomic config replacement fix

Native smoke checks exposed a pre-existing Windows/Linux issue: atomically replacing
config.yaml detached the file watch. The watcher now also monitors its containing
directory, retaining the original file watch for missing-file errors and direct
symlink-target writes. Existing path filters ignore unrelated siblings.

A real-filesystem regression requires two consecutive replacements and a subsequent
in-place write to finish reloading, using observable hash/port completion rather
than guessed sleeps. A second test checks unrelated siblings are ignored. The
packaged-binary smoke continues using atomic replacement: no reload assertion was
removed or weakened. This small watcher fix is the only production-code change.

## Local commands

```bash
python -m unittest discover -s scripts/pinable -p 'test_*runtime.py' -v
python scripts/pinable/runtime.py build --target linux/amd64 --output /tmp/new-runtime-output
python scripts/pinable/smoke_runtime.py --artifact-dir /tmp/new-runtime-output
```

Use exactly the Go version declared in go.mod. Native macOS is required for signing.
The optional safe `--version` label defaults to source timestamp plus SHA. No pip
modules, Node, Docker, GoReleaser or zip/unzip executables are required by these
packaging tools. Do not commit generated runtimes.

## Upstream v8 compatibility (2026-09-30)

The synchronized source uses `github.com/router-for-me/CLIProxyAPI/v8` and Go
1.26.0. This is an SDK import-path change, not a Desktop host-contract bump:
`--version`, `--parent-pid`, the runtime-only API key and the authenticated
`/v0/management/runtime-info` / `runtime-shutdown` endpoints retain contract `1`.
Upstream now deprecates general v0 management development in favor of
`/v8/management`; the existing v0 routes remain available for compatibility.

Legacy configuration files can still be read without rewriting their layout.
A successful v8 configuration write migrates the file to the new layout; the
client-key field is `/v8/management/config/access/api-keys`. Do not confuse it
with the upstream provider groups under `/v8/management/config/api-keys`.
The native smoke suite verifies both the legacy atomic key rotation and a v8
management key rotation, including rejection of the replaced key, survival of
the ephemeral key without persistence, and continued v0 runtime control.

A source sync generates Actions artifacts through the existing workflow. It
does not by itself publish a new Release, replace Latest, or update Desktop's
component lock. Approve the new source and binary hashes before changing that
lock; retain the signing and plugin limitations described above.
