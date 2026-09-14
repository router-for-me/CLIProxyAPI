# Design: PostgreSQL-First NixLLM Control Plane

**Date:** 2026-09-14
**Status:** Validated design
**Reference:** [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

## Goal

Make PostgreSQL the single source of truth for NixLLM configuration and operational data, remove runtime dependence on `config.yaml`, and establish the first boundary that lets NixLLM evolve independently from the CLIProxyAPI core. Existing YAML is accepted only through an explicit one-time import or an export for portability and backup.

The migration is intentionally incremental. During the first phase, a private ephemeral file bridge supplies the file-shaped configuration and OAuth artifacts that the current core execution engine still requires. The bridge is an implementation detail, not a second persistence backend: operators do not edit it, the runtime never falls back to it, and it can always be regenerated from PostgreSQL.

## Decisions

1. **Migration mode:** PostgreSQL-first with one-time YAML import. Runtime file, object-store, and git-store fallbacks are removed from the main server path once this migration is enabled.
2. **Bootstrap boundary:** Environment or an external secret manager supplies only database bootstrap values: `PGSTORE_DSN`, schema/pool settings, and the encryption key. Runtime settings are loaded from PostgreSQL after the connection is ready.
3. **Compatibility bridge:** An ephemeral `0700` runtime directory contains generated config/auth artifacts for the current core. It is never an operator-editable source.
4. **Configuration model:** A singleton JSONB row stores scalar/runtime settings. Existing normalized PostgreSQL tables remain authoritative for domain resources.
5. **Migration safety:** Import is idempotent, transactional, checksum-recorded, and refused when an active configuration already exists unless `--replace` is explicit.
6. **Dashboard:** Dashboard-first control plane with structured CRUD, explicit Save, inline server validation, optimistic concurrency, revision history, and import/export snapshot flows.
7. **Raw snapshot:** The existing raw-config surface becomes a read-only PostgreSQL-rendered YAML snapshot with Import and Export actions. It no longer writes a file directly.
8. **Rollback:** Rollback creates a new active revision from an earlier snapshot and reloads the runtime. It does not mutate database rows manually.
9. **Scope of phase one:** Do not rewrite every CLIProxyAPI executor. Replace persistence and configuration ownership first, then reduce the bridge surface in later phases.

## Context and current state

NixLLM already has substantial PostgreSQL support. The current store creates and migrates tables for API keys, policies, usage events/errors/windows, model catalogs and pricing, internal users, management tokens/audit, upstream providers and child rows, proxy pools, model groups, auto routers/profiles, model health, alerts, and LiteLLM compatibility data. The upstream-provider rows are already normalized and are used as the source of truth before being rendered into runtime artifacts.

The remaining coupling is structural:

- `cmd/server/main.go` loads a YAML path after PostgreSQL bootstrap.
- `PostgresStore.Bootstrap` mirrors database content to `pgstore/config/config.yaml` and `pgstore/auths/`.
- Management mutations call `config.SaveConfigPreserveComments`.
- The watcher is built around `fsnotify` events for the config and auth directories.
- `RawConfigTab` reads and writes `/v0/management/config.yaml` as a disk file.
- Some Core APIs still receive a config path and consume file-shaped auth/config data.

The new design changes the ownership boundary without making the first release depend on a risky executor rewrite.

## Architecture

```text
bootstrap env/secrets
        |
        v
PostgreSQL connection + migrations
        |
        +--> runtime_config (singleton JSONB, revision)
        +--> normalized resource tables
        +--> config_revisions / config_imports / audit
        |
        v
Config repository: load, merge, validate, compile
        |
        +--> in-memory config.Config for runtime services
        +--> ephemeral compatibility bridge (generated YAML/auth files)
        |
        v
CLIProxyAPI core execution engine
```

### Control-plane packages

Add a focused configuration repository/service rather than making every handler know the `config_store` schema:

- `internal/configstore/` owns the repository interface, transactional persistence, revision records, import/export metadata, and optimistic concurrency.
- `internal/configstore/pg/` implements the PostgreSQL repository and migrations.
- `internal/runtimebridge/` compiles a validated configuration/resource snapshot into an ephemeral directory and removes it during shutdown.
- `internal/configsnapshot/` converts between the canonical in-memory configuration and a deterministic YAML representation used only for import/export and inspection.
- `internal/configvalidation/` exposes the shared validation pipeline to startup, dashboard mutations, CLI import, and snapshot import.

Names may be adjusted to match existing package conventions during implementation, but the responsibilities must remain separate. The repository should not call Core internals, and the bridge should not write directly to database tables.

### Canonical configuration assembly

`ConfigRepository.Load(ctx)` returns a complete immutable snapshot containing:

- scalar settings from `runtime_config.settings`;
- normalized resource projections from provider, key, policy, model, routing, proxy, plugin, and other tables;
- the active revision and update metadata.

The assembly process applies defaults, validates cross-resource references, and produces `config.Config` only after the complete candidate is valid. The runtime receives a cloned snapshot. Handlers do not mutate the live config pointer while a save is in progress.

Provider and OAuth data remain normalized in their existing tables. The repository may project them into the canonical config for the bridge, but must not duplicate them into the singleton JSONB row. Unknown or not-yet-modeled scalar/plugin fields use a versioned `extra` JSONB object until a dedicated schema is introduced.

## PostgreSQL schema

### `runtime_config`

A singleton row stores values that currently live as top-level YAML settings and do not have their own domain table:

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `INTEGER PRIMARY KEY` | Always `1` |
| `settings` | `JSONB NOT NULL` | Canonical scalar/runtime settings, excluding secrets that have dedicated sealed columns |
| `extra` | `JSONB NOT NULL DEFAULT '{}'` | Forward-compatible settings not yet normalized |
| `revision` | `BIGINT NOT NULL` | Monotonically increasing active revision |
| `updated_at` | `TIMESTAMPTZ NOT NULL` | Database update time |
| `updated_by` | `TEXT` | Management token/user or `system-import` |
| `updated_source` | `TEXT NOT NULL` | `dashboard`, `cli-import`, `rollback`, or `system` |

The row is created only after a validated candidate is ready. A missing row means the database has not been imported or initialized, not that the server should silently use a file.

### `config_revisions`

Append-only revision metadata supports audit, diff, and rollback:

| Column | Type | Notes |
| --- | --- | --- |
| `revision` | `BIGINT PRIMARY KEY` | Revision identifier |
| `settings` | `JSONB NOT NULL` | Complete scalar settings snapshot |
| `resource_snapshot` | `JSONB NOT NULL` | Stable references and values for normalized resources |
| `checksum` | `TEXT NOT NULL` | SHA-256 of canonical snapshot |
| `created_at` | `TIMESTAMPTZ NOT NULL` | Creation time |
| `created_by` | `TEXT` | Actor or import command |
| `reason` | `TEXT NOT NULL` | Human-readable change reason |

Sensitive values are sealed according to the existing `PGSTORE_ENCRYPTION_KEY` policy. Export and revision APIs redact secrets by default. The revision record must not become a new plaintext secret archive.

### `config_imports`

Each explicit or automatic import records:

- source name/path, but not an absolute path if it would reveal host details;
- source checksum and canonical checksum;
- validation status (`dry_run`, `committed`, `rejected`, `replaced`);
- active revision created, if any;
- resource counts by category;
- error summary and timestamps;
- actor and import mode.

All new tables use idempotent migrations and are included in PostgreSQL backup/restore bundles.

### Transactions and concurrency

Every mutation uses one PostgreSQL transaction:

1. read the current active revision with `FOR UPDATE`;
2. build a candidate from the request and the current snapshot;
3. validate the full candidate, including cross-resource references;
4. write normalized resource changes and/or `runtime_config`;
5. increment `revision` and append `config_revisions`;
6. insert an audit/import record when applicable;
7. issue a PostgreSQL `NOTIFY nixllm_config_changed` after the write is committed.

The API accepts `expected_revision`. A mismatch returns `409 Conflict` with the current revision and a safe summary of changed sections. No last-write-wins behavior is allowed for structured configuration writes.

`NOTIFY` is an acceleration path, not the source of correctness. A reconnecting instance runs a bounded revision check and reloads if it missed a notification.

## Startup and ephemeral bridge

### Startup sequence

1. Parse only bootstrap flags/environment: DSN, schema, encryption key, local operational flags that must exist before the database is opened, and import command options.
2. Connect to PostgreSQL with the existing credential-acquisition timeout and run `EnsureSchema` plus `Migrate`.
3. If `runtime_config` is empty, inspect only the explicitly configured legacy import source. Do not scan arbitrary paths and do not silently import `config.yaml`.
4. Validate and transactionally import the legacy YAML if an import source is present. If no source is present, fail with an actionable message explaining how to run `nixllm import-config`.
5. Load the active PostgreSQL snapshot and compile it into `config.Config`.
6. Create a unique ephemeral bridge directory under the process runtime directory with mode `0700`.
7. Render the minimal config/auth artifacts required by Core. Record the bridge path only in memory and logs without secrets.
8. Start services using the generated path. Register the database-backed token store and configuration event listener.
9. On shutdown, stop listeners/services and remove the bridge directory. Removal failure is logged but does not mask the original shutdown error.

The generated YAML is not watched for changes. Any file event originating inside the bridge is ignored by the persistence layer. Core reloads happen through an explicit compiled-snapshot callback after a committed PostgreSQL revision.

### Bridge contract

`RuntimeBridge` exposes:

- `Build(ctx, snapshot) (Artifact, error)`;
- `ConfigPath() string`;
- `AuthDir() string`;
- `Reload(ctx, snapshot) error`;
- `Close() error`.

Build writes files atomically inside the bridge directory, verifies that every generated artifact came from the validated snapshot, and applies `0600` to auth/config files. It never accepts operator file paths as output targets. Reload builds a new generation beside the old one, switches the Core callback only after all files are complete, then removes the old generation. This avoids a partially rendered reload.

The bridge is deliberately narrow. Resource CRUD should converge on direct in-memory adapters over time; until then, generated artifacts preserve behavior while the persistence boundary is removed from the Core.

## One-time YAML import and CLI

Commands:

```text
nixllm import-config --dsn "$PGSTORE_DSN" [--schema public] [--dry-run] [--replace] config.yaml
nixllm export-config --dsn "$PGSTORE_DSN" [--schema public] [--include-secrets=false] config.yaml
nixllm verify-config --dsn "$PGSTORE_DSN" [--schema public]
```

The command may use environment-based connection settings, but must not require a running proxy server. It uses the same parse/default/validation and resource-mapping code as the server.

### Import behavior

- Parse YAML with the existing parser and reject malformed input before opening a write transaction.
- Normalize line endings and calculate a source checksum.
- Produce a dry-run report: scalar sections, resources to create/update, skipped legacy fields, warnings, and validation errors.
- Refuse to overwrite an active `runtime_config` row unless `--replace` is supplied.
- With `--replace`, retain the prior revision and create a new revision. Never delete prior history as part of import.
- Map provider blocks to `upstream_providers` and child tables, auth metadata to the PostgreSQL token store, and API key/policy sections to the existing normalized tables.
- Preserve unknown fields in versioned `extra` JSONB and report them clearly.
- Commit all mapped changes atomically. A mapping or database failure rolls back the whole import.
- Return a summary suitable for both CLI output and automation, including the committed revision and checksum.

### Export behavior

Export is a deterministic projection from PostgreSQL, not a read of the bridge file. It includes comments only where the snapshot generator has stable documentation for a field. Secrets are redacted by default, and `--include-secrets` requires an explicit confirmation mechanism appropriate to the CLI environment. Export writes to a temporary sibling and renames atomically, with restrictive permissions when secrets are included.

### Verification behavior

`verify-config` loads the active snapshot, runs full validation, verifies normalized foreign-key references and checksum consistency, and reports stale/missing resource projections without changing data. It exits non-zero on errors.

## Dashboard UX

The dashboard is the primary operator control plane. It follows OmniRoute's useful patterns without copying its product-specific routing model: one control surface, explicit persistence, fast feedback, transparent decisions, and no hidden file edits.

### Shared page shell

Each resource page uses:

1. a compact header with resource title, current PG revision, and one primary `New` action;
2. search/filter/sort controls;
3. a data table with status and safe redaction of credentials;
4. a detail drawer or dedicated editor page for complex resources;
5. explicit `Save changes`, `Cancel`, and dirty-state protection;
6. inline validation plus a page-level system error summary;
7. reload/retry and empty states that explain how to create the first resource.

The existing dedicated provider editor pattern remains the preferred form for complex upstream providers. API keys, internal users, proxy pools, model groups, auto routers, routing, error messages, pricing sources, and plugin settings use the same save/concurrency contract.

### Save and conflict flow

- Forms keep a local draft and show `unsaved changes` when it differs from the loaded revision.
- Save sends `expected_revision` and a structured patch.
- The server validates the full candidate before committing.
- Success returns the new revision and changed sections. The UI updates its baseline only after the response succeeds.
- A `409` does not discard the draft. The UI offers `View diff`, `Keep mine`, and `Use current` after the operator has reviewed the latest state.
- Leaving a dirty form requires confirmation. Browser refresh uses the existing before-unload guard only while a draft is dirty.
- Toasts are reserved for transient confirmation; persistent state such as revision and reload status appears inline.

### Snapshot page

`Config -> Snapshot` replaces the current raw editor behavior:

- `View` renders deterministic YAML from PostgreSQL and is read-only.
- `Export` downloads a redacted snapshot, with an explicit secret-inclusion option if supported.
- `Import` accepts a YAML file or pasted content, parses it server-side, and shows a section/resource diff before confirmation.
- `History` lists revisions with actor, reason, checksum prefix, and reload result.
- `Rollback` creates a new revision from a selected prior snapshot and requires confirmation.

The old PUT behavior that writes directly to `h.configFilePath` is removed from the production route. Compatibility responses may remain for one deprecation window, but PUT must return a clear migration error rather than mutate a file.

### Visual and interaction language

- Use the existing dashboard component language and a neutral, high-contrast palette with one primary accent.
- Keep data density appropriate for an operator cockpit: compact rows, monospace values for identifiers/numbers, and meaningful status colors only where status is semantic.
- Use skeleton rows for loading, contextual banners for errors, and inline field errors for validation.
- Use motion only for state transitions and feedback, with `prefers-reduced-motion` support. No scroll hijacking or decorative animation.
- Maintain WCAG AA contrast, visible focus states, keyboard navigation, and a command-palette path to high-frequency resources.
- Do not expose full credentials in tables, snapshots, diffs, logs, or error messages.

## Reload and event handling

The file watcher is no longer the configuration authority. Introduce a `ConfigRuntimeCoordinator` that owns the active snapshot and serializes reload generations:

1. receive a committed revision from a local mutation or PostgreSQL `LISTEN` connection;
2. coalesce duplicate notifications and ignore revisions already applied;
3. load the revision from PostgreSQL;
4. validate and compile it again before activation;
5. build the next bridge generation;
6. apply the snapshot to Core/services through the existing reload callback;
7. mark the generation applied and publish reload status for the dashboard.

If reload fails after commit, the database remains authoritative and the previous runtime generation continues serving. The coordinator reports `committed but pending reload` with the failed revision and error. A retry is safe because bridge builds are generation-based and idempotent. The system must not roll back database state automatically on a transient runtime reload failure.

A later phase can replace the remaining file watcher with direct resource event adapters. During phase one, the watcher may continue to exist for test fixtures or non-runtime compatibility code, but it must not persist bridge changes or trigger database writes.

## Error handling and security

- Missing DSN: fail at startup with an actionable message; never silently fall back to YAML, object storage, git storage, or local files.
- Database unavailable: fail startup or report a clearly degraded control-plane state according to the command mode; do not start with stale configuration pretending it is current.
- Empty database without explicit import source: fail with the import command and expected source path in the message.
- Invalid import: return structured field/section errors; no partial rows are committed.
- Revision conflict: return `409` with current revision metadata, never overwrite.
- Reload failure: retain prior active runtime generation, expose the committed revision as pending, and retry.
- Bridge write failure: leave the currently active generation untouched and clean up the incomplete next generation.
- Secret handling: use sealed columns/JSONB where available, redact logs and exports by default, avoid path/credential disclosure, and keep bridge permissions restrictive.
- Shutdown cleanup: best-effort removal with a warning. The next startup uses a unique directory and never trusts stale files.
- Backup/restore: extend existing PostgreSQL backup resources to include runtime configuration and revision/import tables. Restoring a snapshot must pass verification before activation.

## Testing strategy

### Unit tests

- Canonical config assembly merges scalar settings and normalized resources correctly.
- Defaults and unknown `extra` fields round-trip without loss.
- Full-candidate validation catches cross-resource references and invalid provider/policy combinations.
- Revision increments, expected-revision conflicts, and rollback semantics are deterministic.
- Import mapping handles every currently supported YAML section, duplicate identities, empty sections, and unsupported fields.
- Redacted export never emits secret values; deterministic export has stable ordering/checksum.
- Bridge generation uses restrictive permissions, atomic writes, unique generations, and cleanup on failure.
- Reload coordinator coalesces notifications, ignores stale revisions, and preserves the active generation on build/apply errors.

### PostgreSQL integration tests

Run with `PGSTORE_TEST_DSN` following existing store tests:

- migrations are idempotent;
- empty database import creates exactly one active runtime revision;
- concurrent writes produce one success and one `409`-equivalent conflict;
- failed imports leave all tables unchanged;
- replace/import retains prior revision history;
- export/import round-trip preserves canonical non-secret configuration;
- backup/restore includes the new tables.

### HTTP and dashboard tests

- structured mutations send `expected_revision` and handle success/conflict/error states;
- snapshot View is read-only;
- Import displays diff before commit and preserves draft on rejection;
- Export redacts credentials;
- dirty forms prevent accidental navigation;
- empty, loading, retry, and PG-unavailable states render correctly;
- resource editor mutations update the revision and trigger reload status.

### Required verification

After implementation, run:

```bash
gofmt -w <changed Go files>
go test ./...
go build -o test-output ./cmd/server && rm test-output
cd web/dashboard && npm test
cd web/dashboard && npm run build
make dash-embed
```

Also perform a manual smoke test against PostgreSQL: fresh import, restart without YAML, structured dashboard edit, revision conflict from two sessions, redacted export, rollback, and shutdown/startup bridge cleanup.

## Implementation phases

### Phase 1: Persistence boundary and migration

- Add schema and repository for `runtime_config`, revisions, and imports.
- Add canonical snapshot assembly and shared validation.
- Add `import-config`, `export-config`, and `verify-config` command modes.
- Add migration tests and dry-run reporting.

### Phase 2: Startup and bridge

- Make PG runtime mode explicit and fail closed without DSN.
- Stop `PostgresStore.Bootstrap` from making a database-to-file sync the source of startup configuration.
- Introduce the ephemeral bridge and lifecycle cleanup.
- Register the coordinator and reload callback.
- Keep the legacy file store code behind a deprecated compatibility path only where tests/SDK consumers require it.

### Phase 3: Management API contract

- Add snapshot/revision endpoints and `expected_revision` to structured mutations.
- Replace `SaveConfigPreserveComments` calls in management handlers with repository transactions.
- Convert config YAML GET/PUT to snapshot View and import/export semantics.
- Add audit/reload status responses.

### Phase 4: Dashboard control plane

- Update API client and shared revision/conflict handling.
- Update resource pages incrementally, starting with runtime settings and upstream providers.
- Add snapshot history, diff, import preview, export redaction, and rollback.
- Keep complex provider editing on dedicated pages rather than reintroducing large modals.

### Phase 5: Remove remaining runtime file coupling

- Replace Core file reads with direct adapters as they become available.
- Remove runtime use of `configFilePath`, `pgstore/config/config.yaml`, and durable auth mirrors.
- Retain only explicit import/export artifacts and test fixtures.
- Deprecate and then remove object-store/git-store/local runtime fallback flags from the server binary after one documented release window.

## Non-goals and follow-ups

- This design does not rewrite provider executors or translator packages.
- Usage event storage, model catalog, proxy pools, auto-router analytics, and alerts remain in their existing normalized tables.
- Runtime cooldown/breaker state remains in-memory unless a separate operational-state design is approved.
- No multi-node distributed lock is introduced beyond PostgreSQL row locking and revision checks.
- No automatic secret rotation or external vault integration is required for the first phase.
- Full removal of the compatibility bridge is a follow-up after the Core adapter boundary is independently implemented and tested.

## Acceptance criteria

The design is considered implemented when all of the following are true:

1. A fresh PG-backed server starts from an imported database snapshot without a user-provided `config.yaml`.
2. A missing DSN or empty database fails closed with an actionable import message.
3. No runtime path treats `config.yaml`, `pgstore/config/config.yaml`, or auth JSON files as a fallback source of truth.
4. Every structured dashboard mutation validates and commits atomically with a revision.
5. Concurrent dashboard edits cannot silently overwrite one another.
6. Snapshot View is read-only, Export is redacted by default, and Import shows a diff before commit.
7. A committed revision can be rolled back by creating a new revision and reloading the runtime.
8. The Core compatibility bridge is ephemeral, private, generation-safe, and removed on shutdown.
9. Existing PG-backed domain resources and backup/restore behavior remain compatible.
10. Required Go, dashboard, build, embed, and PostgreSQL smoke tests pass.
