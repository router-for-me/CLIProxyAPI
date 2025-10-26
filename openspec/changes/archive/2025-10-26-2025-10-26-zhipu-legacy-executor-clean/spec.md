# Spec: Zhipu via Python Agent Bridge (Mandatory) and Legacy Cleanup

## Summary
- Provider "zhipu" MUST route exclusively through the Python Agent Bridge (Claude Agent SDK for Python).
- REMOVE legacy fallback to OpenAICompatExecutor for zhipu (both streaming and non-streaming).
- Bridge URL selection priority: config.baseURL > env CLAUDE_AGENT_SDK_URL > ensureClaudePythonBridge() default.
- Security: Bridge URL MUST be local-only by default (127.0.0.1/localhost/::1). Allow remote only when CLAUDE_AGENT_SDK_ALLOW_REMOTE=true.

## Requirements
1. Endpoint compatibility remains OpenAI-compatible (/v1/chat/completions) with stream and non-stream paths.
2. When claude-agent-sdk-for-python.enabled=false → return a diagnostic error; DO NOT fallback to legacy.
3. Bridge URL validation:
   - scheme in {http, https}
   - host local-only by default; remote allowed only with CLAUDE_AGENT_SDK_ALLOW_REMOTE=true

## Migration Steps (Breaking)
1. Enable Bridge: set claude-agent-sdk-for-python.enabled=true.
2. Provide local baseURL (default http://127.0.0.1:35331) or set CLAUDE_AGENT_SDK_URL.
3. Configure Python side:
   - export ANTHROPIC_BASE_URL="https://open.bigmodel.cn/api/anthropic"
   - export ANTHROPIC_AUTH_TOKEN="<Zhipu API Key>"
4. If you must use a remote Bridge host, explicitly set CLAUDE_AGENT_SDK_ALLOW_REMOTE=true (understood as advanced usage).
5. Remove configs relying on legacy fallback; zhipu now errors when Bridge is disabled.

## Risks
- Bridge unavailability will surface as errors (previously hid behind legacy fallback).
- Remote Bridge exposure may introduce SSRF/snooping risk if CLAUDE_AGENT_SDK_ALLOW_REMOTE is enabled without proper network policy.

