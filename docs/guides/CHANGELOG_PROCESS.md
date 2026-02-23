# Changelog Process

## Purpose
Keep release notes consistent, user-facing, and easy to audit.

## Rules
- Every user-visible change must add a bullet under `## [Unreleased]` in `CHANGELOG.md`.
- Use one of: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, `Security`.
- Keep bullets concise and impact-focused.

## Release Workflow
1. Move all `Unreleased` bullets into a new version heading: `## [X.Y.Z] - YYYY-MM-DD`.
2. Preserve category structure.
3. Recreate an empty `## [Unreleased]` section at the top.

## PR Gate
Run `task changelog:check` before push.
