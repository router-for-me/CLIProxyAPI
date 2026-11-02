# Custom model context windows in Claude Code

Claude Code can assume a 200K context window for an unrecognized model ID,
even when the upstream model supports more. An auto-compaction window alone
does not increase the assumed model window: Claude caps it at that window.

For an unrecognized model ID that does not start with `claude-` and has no
`[1m]` suffix, declare the actual upstream capacity in the client environment.
For example, for a model verified to support 1,050,000 tokens:

```sh
export CLAUDE_CODE_MAX_CONTEXT_TOKENS=1050000
export CLAUDE_CODE_AUTO_COMPACT_WINDOW=900000
```

Restart/resume the client and inspect `/context`. Claude Code 2.1.316 reports
a 900K window with a 33K compaction buffer in this configuration, so automatic
compaction is expected near 867K. This is a client sizing check, not proof
that every provider/account accepts a request that large. Confirm capacity
for the chosen model and deployment before using these values.

These values are optional client settings, not gateway defaults. They do not
disable compaction or permission checks. Restore the previous environment
when changing to a model or backend with different capacity. Recognized
Claude IDs and `[1m]` IDs have different rules; consult the native docs:

https://code.claude.com/docs/en/model-config#correct-the-window-for-a-gateway-or-custom-model-id
