# Changelog Process

## Rules
- Record user-visible changes under `## [Unreleased]` in `CHANGELOG.md`.
- Use sections: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, `Security`.

## Release Cut
1. Move unreleased bullets into `## [X.Y.Z] - YYYY-MM-DD`.
2. Keep category grouping.
3. Recreate empty `## [Unreleased]`.

## Check
Run `task changelog:check` before push.
