# Follow the Claude Code session model

Opt in with `requests.claude-auto-mode-follow-session: true` in a v8 config
and `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1` in Claude Code's environment.

The gateway records the model of requests marked `main` for each authenticated
client and Claude session ID. Recognized permission-classifier requests
(`auxiliary`, one user transcript message, no tools) use that model. Main model
changes take effect on the next main request. Subagents and unrelated auxiliary
requests cannot change the selection. Aliases still resolve through normal routing.

The option changes only model routing, not classifier evidence, permission rules,
or reasoning effort. Existing effort payload rules still apply. It defaults off.
There is no cross-session or cross-client fallback. After a gateway restart,
before the first main request, or without an authenticated identity or native
hint headers, the original classifier model is retained. The cache holds up to
4096 entries for 24 hours, with model names capped at 256 bytes.

This does not repair consent provenance lost when relaying instructions to a
subagent, and does not guarantee identical permission decisions across models.
