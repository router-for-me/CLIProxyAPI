# Changelog Process

## Purpose
This process keeps release notes consistent, searchable, and auditable.

## Rules
- Every user-visible change must add a bullet under `## [Unreleased]` in `CHANGELOG.md`.
- Use one of: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, `Security`.
- Keep bullets short and user-facing.

## Release Cut
1. Move `Unreleased` bullets into a new version heading: `## [X.Y.Z] - YYYY-MM-DD`.
2. Keep the category structure.
3. Recreate an empty `## [Unreleased]` section at the top.

## PR Gate
Run `task changelog:check` before push.
