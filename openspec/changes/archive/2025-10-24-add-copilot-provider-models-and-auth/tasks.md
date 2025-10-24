## 1. Specification & Validation
- [ ] 1.1 Update Copilot model inventory spec (delta): Copilot exposes only `gpt-5-mini`
- [ ] 1.2 Update auth spec (delta): device-code Accept header and token exchange required headers
- [ ] 1.3 Run `openspec validate add-copilot-provider-models-and-auth --strict` and fix issues

## 2. Implementation
- [ ] 2.1 CLI: Add `Accept: application/json` to `/login/device/code` request
- [ ] 2.2 CLI: Add required headers to `/copilot_internal/v2/token` request
- [ ] 2.3 Management API: Add `Accept: application/json` to device-code request
- [ ] 2.4 Management API: Add required headers to token exchange request
- [ ] 2.5 Registry: `GetCopilotModels()` returns only `gpt-5-mini`; remove `gpt-5-mini` from OpenAI list
- [ ] 2.6 Executor: Remove aliasing `gpt-5-mini` → minimal rewrite in CodexExecutor
- [ ] 2.7 Executor: Special-case Copilot → call `https://api.githubcopilot.com/chat/completions` with Bearer + required headers
- [ ] 2.8 Auth manager: ensure latest copilot auth is selected; document watcher reload procedure

## 3. Tests
- [ ] 3.1 CLI: device-code Accept header test (form-encoded fallback simulation)
- [ ] 3.2 CLI: token exchange headers test (403 on missing headers)
- [ ] 3.3 Management API: E2E fake device flow writes credential file
- [ ] 3.4 Management API: `/v0/management/models?provider=copilot` includes `gpt-5-mini`

## 4. Docs / Release Notes
- [ ] 4.1 Update change log: Copilot no longer mirrors OpenAI inventory
- [ ] 4.2 Document required headers for Copilot token exchange
- [ ] 4.3 Note rollback plan in proposal

## 5. Rollout
- [ ] 5.1 Build & smoke test `./cli-proxy-api --copilot-auth-login`
- [ ] 5.2 Verify `/v1/models` & management providers/models endpoints

