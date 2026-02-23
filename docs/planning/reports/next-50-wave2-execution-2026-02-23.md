# Next 50 Wave 2 Execution (Items 11-20)

- Source batch: `docs/planning/reports/next-50-work-items-2026-02-23.md`
- Board updated: `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- Scope: `CP2K-0031`, `CP2K-0034`, `CP2K-0036`, `CP2K-0037`, `CP2K-0039`, `CP2K-0040`, `CP2K-0045`, `CP2K-0047`, `CP2K-0048`, `CP2K-0050`

## Status Summary

- `implemented`: 7
- `in_progress`: 3 (`CP2K-0039`, `CP2K-0040`, `CP2K-0047`)

## Evidence Notes

- `CP2K-0031` (`#158`): OAuth upstream URL support validated via config tests and wave reports.
- `CP2K-0034` (`#147`): quickstart/doc handling evidenced in lane reports.
- `CP2K-0036` (`#145`): OpenAI-compatible Claude mode docs/test evidence present; translator tests pass.
- `CP2K-0037` (`#142`): parity-test coverage references present in CPB lane reports.
- `CP2K-0039` (`#136`): IDC refresh hardening evidenced in reports; test slice currently blocked by unrelated auth/kiro test compile issue.
- `CP2K-0040` (`#134`): explicit non-stream `output_tokens=0` standardization evidence still needed.
- `CP2K-0045` (`#125`): 403 UX hardening verified via antigravity 403 hint tests.
- `CP2K-0047` (`#118`): enterprise Kiro stability parity evidence not yet isolated.
- `CP2K-0048` (`#115`): Kiro AWS ban/suspension handling evidenced in wave reports.
- `CP2K-0050` (`#111`): antigravity auth-failure handling evidenced in reports/tests.

## Commands Run

- `go test ./pkg/llmproxy/config -run 'TestSanitizeOAuthUpstream_NormalizesKeysAndValues|TestOAuthUpstreamURL_LowercasesChannelLookup' -count=1` (pass)
- `go test ./pkg/llmproxy/executor -run 'TestAntigravityErrorMessage_AddsLicenseHintForKnown403|TestAntigravityErrorMessage_NoHintForNon403' -count=1` (pass)
- `go test ./pkg/llmproxy/translator/claude/openai/chat-completions -count=1` (pass)
- `go test ./pkg/llmproxy/auth/kiro -run 'TestRefreshToken|TestRefreshTokenWithRegion|TestRefreshToken_PreservesOriginalRefreshToken' -count=1` (blocked: `sso_oidc_test.go` references undefined `roundTripperFunc`)
