# ADR 0002: SQLModel + hand-written v4 migrations

**Status**: Accepted
**Date**: 2026-07-27

## Context

The dashboard persists usage events in SQLite. The current schema is at
version 4 (`usage_events` + `key_aliases` + `collector_state` + `schema_meta`),
managed by forward-only Python migration functions applied one-per-transaction
(`storage.run_migrations`). Production databases exist at v4 and must be
read without data migration.

The rewrite moves to FastAPI. The team selected SQLModel (FastAPI-author
maintained, Pydantic + SQLAlchemy 2.0 in one model). The question is how
schema evolution is managed.

## Decision

- **ORM**: SQLModel for table definitions and for Pydantic request/response
  schemas. One class per table; read/query endpoints accept/return these
  classes directly.
- **Migrations**: keep the existing `schema_meta` + per-version `_migrate_vN`
  approach. The migration runner stays hand-written Python; each migration
  is one `BEGIN/COMMIT` transaction and idempotent (`CREATE TABLE IF NOT
  EXISTS`, `_exec_ddl`).
- **Alembic**: not adopted. The schema is small (3 tables), changes are
  infrequent, and the existing runner already enforces the invariants
  (atomic per version, abort on failure, idempotent re-run).
- **SQLModel table definitions** are **declarative only**; they are not
  used to auto-create or auto-migrate schema. `init_schema()` still calls
  `run_migrations()`. SQLModel metadata is kept consistent with the
  migration output by a test that asserts `SQLModel.metadata` matches the
  applied schema (column names + types) on a fresh in-memory DB.

## Alternatives considered

- **SQLAlchemy 2.0 + Alembic.** Rejected: schema is too small to justify
  Alembic's setup, and SQLModel already wraps SQLAlchemy 2.0. Two model
  layers (ORM + Pydantic) for one concept is duplication.
- **Peewee / Tortoise.** Rejected: ecosystem and FastAPI integration are
  weaker than SQLModel.
- **Raw `sqlite3` queries, no ORM.** Rejected: loses the Pydantic
  validation win that motivated the rewrite, and forces manual dict ↔
  row mapping that SQLModel does for free.

## Consequences

**Positive**

- Existing v4 databases are read directly; zero data migration.
- One class per concept (table + schema), no ORM/schema duplication.
- Migration runner stays auditable: every schema change is a Git diff of
  one `_migrate_vN` function.
- A schema-consistency test catches drift between SQLModel metadata and
  the migration output.

**Negative**

- A new consistency test must be written and maintained; without it, the
  SQLModel metadata can drift from the actual schema.
- Future contributors expecting "FastAPI = Alembic" will be surprised;
  the ADR is the answer to that surprise.

**Neutral**

- Migration functions now construct DDL by string; SQLModel table classes
  are used for queries, not for DDL. This is a deliberate split.
