# Usage Dashboard — Provenance

This implementation is a **clean-room reimplementation** written from the
documented behavior in `docs/cleanroom-design.md`. It contains no source code,
structure, or expression derived from any third-party project.

The CLIProxyAPI usage dashboard concept (collect usage from the management
queue, store in SQLite, serve a local dashboard) is implemented independently
here using only the Python standard library.

## No upstream code retained

Earlier revisions of this directory vendored code from an upstream project that
did not ship a license. That code has been removed and replaced wholesale. If
you find any artifact that appears derivative, treat it as a bug and file an
issue so it can be replaced.

## SQLite schema compatibility

The schema (columns, indexes, `schema_meta` version table) is intentionally
compatible with databases created by prior revisions of this tool, so existing
deployments upgrade in place. The schema is documented in
`docs/cleanroom-design.md`; it is not derived from any upstream schema.