# Claude Code auto mode through CLIProxyAPI

Anthropic can run the auto-mode safety classifier inside a session's normal model requests, at no additional charge. This depends on the upstream actually returning server-side verdicts. It is not a billing switch in the proxy.

For the native Claude Messages route, CLIProxyAPI preserves the `safeguards` request field, the `dangerous-tool-use-2026-09-03` beta, unknown beta values and body fields, and `safeguard_results` in JSON responses and streaming events. Native tool-use IDs remain unchanged. Explicit operator payload rules and additional gateways or plugins can still change the traffic.

Use a native Anthropic account/model route for this feature. Routing Claude Code to a non-Anthropic provider does not supply Anthropic's server-side classifier. An Anthropic-compatible API alone does not guarantee that the upstream provides it; account, model, and platform rollout also matter.

## Check a session

1. Use a current Claude Code release and start a new session in auto mode, routed to a native Anthropic upstream.
2. Leave `CLAUDE_CODE_AUTO_MODE_SERVER` unset. Setting it to `0` forces separate client classifier requests; it does not make them free. Also check whether `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS` disables server-side checks for your client version.
3. Run `/status` in that session. **Auto mode server: Enabled** confirms that server-side checks are deciding its actions. **Disabled** means it has fallen back.

A missing billing notice is not proof of eligibility: Pro, Max, and Team plans do not show that notice. The proxy must never synthesize a safety verdict when the upstream provides none, and should not relabel separately billed classifier usage as free.

The executor regression test `TestClaudeExecutor_AutoModeSafeguardsPassthrough` covers API-key and OAuth routes, recognized native Claude Code and generic callers, JSON and streaming responses, and both presence and absence of verdicts. Its opaque fixtures verify transport preservation, not upstream eligibility or classifier schema validation.

References:

- [Anthropic: Auto mode classifier request charges](https://code.claude.com/docs/en/auto-mode-classifier-billing)
- [Anthropic: Server-side classifier review](https://code.claude.com/docs/en/permission-modes#server-side-classifier-review)
- [Anthropic: Gateway compatibility](https://code.claude.com/docs/en/llm-gateway-protocol#feature-pass-through)
- [CLIProxyAPI issue #6009](https://github.com/router-for-me/CLIProxyAPI/issues/6009)
