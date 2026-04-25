# Specifications

## Acceptance Target

PR `#942` should fail only on real repo or external-service issues, not because the branch carries unresolved conflicts or dead workflow references.

## Guardrails

- Keep `security/snyk (kooshapari)` outside the code-regression bucket because it is a quota/billing issue.
- Do not force branch cleanup with destructive git operations.
- Keep repo-local custom Semgrep rules tracked, but avoid turning them into a hard gate while they still produce repo-wide false positives.
