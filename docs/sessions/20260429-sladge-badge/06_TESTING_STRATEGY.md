# Testing Strategy

## Validation

- Verify the README has exactly one `sladge.net` reference.
- Review `git diff --stat` to confirm the change is documentation-only.
- Check worktree status before committing.

## Commands

```bash
rg -n "sladge.net" README.md
git diff --stat
git status --short --untracked-files=all
```
