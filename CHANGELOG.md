# Changelog

## 2025-10-26
### Breaking Changes
- Zhipu provider now requires the Python Agent Bridge; legacy fallback removed.
- Bridge URL is validated: only http/https schemes; host must be local (127.0.0.1/localhost/::1) by default.
- To allow remote bridge in controlled environments, set `CLAUDE_AGENT_SDK_ALLOW_REMOTE=true`.
- GLM models (glm-*) route exclusively to provider `zhipu`.

### Added
- URL validation with diagnostic guidance.
- Tests for provider routing, bridge validation, and header masking for Authorization.

