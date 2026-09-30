# Curated release notes

Each fork release uses a checked-in note at `docs/releases/<tag>.md`. The file
is the source of truth for the GitHub Release body; the workflow validates it,
appends the asset guide and full commit comparison link, then publishes the
draft only after artifact verification.

## Create a release note

1. Copy [RELEASE_TEMPLATE.md](RELEASE_TEMPLATE.md) to
   `docs/releases/<tag>.md`.
2. Replace every placeholder with release-specific, user-facing facts.
3. Read [the intake ledger](../../ops/upstream-intake/absorbed.md) and state the
   exact aligned upstream release, previous baseline, ported work and skipped
   work. Use `none (fork-only release)` and `none` when no upstream intake is
   included.
4. Validate locally:

   ```bash
   python3 ops/release-notes/validate.py validate v1.2.3 docs/releases/v1.2.3.md
   ```

5. Commit the note with the release changes before creating the release tag.

The validator rejects missing sections, template placeholders, ambiguous
upstream-alignment fields, empty sections, and malformed or unstable tags.
Automated commit summaries are not used as the release body.
