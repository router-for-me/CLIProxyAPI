# Migration Guide: Zhipu Legacy Cleanup

## What Changed
- Zhipu (`provider=zhipu`) no longer supports legacy fallback; the Python Agent Bridge is mandatory.
- Bridge URL must be local-only by default. Remote is opt-in via `CLAUDE_AGENT_SDK_ALLOW_REMOTE=true`.

## Steps
1. Enable Bridge in config:
   ```yaml
   claude-agent-sdk-for-python:
     enabled: true
     baseURL: "http://127.0.0.1:35331"
   ```
2. Or via environment:
   ```bash
   export CLAUDE_AGENT_SDK_URL="http://127.0.0.1:35331"
   # Optional (advanced): allow remote bridge
   export CLAUDE_AGENT_SDK_ALLOW_REMOTE=true
   ```
3. Configure Python service:
   ```bash
   export ANTHROPIC_BASE_URL="https://open.bigmodel.cn/api/anthropic"
   export ANTHROPIC_AUTH_TOKEN="<Zhipu API Key>"
   ```
4. Invoke with OpenAI-compatible payloads using `glm-4.6` or `glm-4.5`.

## Troubleshooting
- Error: "python agent bridge disabled...": enable the Bridge in config.
- Error contains "local-only": use a localhost URL or set `CLAUDE_AGENT_SDK_ALLOW_REMOTE=true` only in controlled environments.

