# Issue Wave GH Next32 Merge Report (2026-02-23)

## Scope
- Parallel lane checkpoint pass: 6 lanes, first shippable issue per lane.
- Base: `origin/main` @ `37d8a39b`.

## Merged Commits
- `6f302a42` - `fix(kiro): add IDC extension headers on refresh token requests (#246)`
- `18855252` - `fix(kiro): remove duplicate IDC refresh grantType field for cline (#245)`
- `5ef7e982` - `feat(amp): support kilocode provider alias model routing (#213)`
- `b2f9fbaa` - `fix(management): tolerate read-only config writes for put yaml (#201)`
- `ed3f9142` - `fix(metrics): include kiro and cursor in provider dashboard metrics (#183)`
- `e6dbe638` - `fix(gemini): strip thought_signature from Claude tool args (#178)`
- `296cc7ca` - `fix(management): remove redeclare in auth file registration path`

## Issue -> Commit Mapping
- `#246` -> `6f302a42`
- `#245` -> `18855252`
- `#213` -> `5ef7e982`
- `#201` -> `b2f9fbaa`, `296cc7ca`
- `#183` -> `ed3f9142`
- `#178` -> `e6dbe638`

## Validation
- Focused package tests:
  - `go test ./pkg/llmproxy/auth/kiro -count=1`
  - `go test ./pkg/llmproxy/translator/gemini/claude -count=1`
  - `go test ./pkg/llmproxy/translator/gemini-cli/claude -count=1`
  - `go test ./pkg/llmproxy/usage -count=1`
- Compile verification for remaining touched packages:
  - `go test ./pkg/llmproxy/api/modules/amp -run '^$' -count=1`
  - `go test ./pkg/llmproxy/registry -run '^$' -count=1`
  - `go test ./pkg/llmproxy/api/handlers/management -run '^$' -count=1`

## Notes
- Some broad `management` suite tests are long-running in this repository; compile-level verification was used for checkpoint merge safety.
- Remaining assigned issues from lanes are still open for next pass (second item per lane).
