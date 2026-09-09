# GitHub Copilot

GitHub Copilot accounts can serve models through the existing proxy APIs. This lets people use their Copilot account alongside other providers and see its remaining allowance in the same management interface.

## Sign in

Run the server's device login command:

```sh
cli-proxy-api --github-copilot-login --config config.yaml
```

Open the GitHub URL printed in the terminal, enter the device code, and approve access. Add `--no-browser` on a remote machine. The TUI also lists GitHub Copilot in OAuth Login.

The companion management center adds a GitHub Copilot card under OAuth Login. It displays the device code and waits for GitHub approval; no callback URL needs to be pasted. The updated management center must be installed to use this card and its quota display.

Credentials are saved in the configured auth directory. Signing into the same GitHub account updates its existing file. Different accounts participate in the existing credential selection, cooldown, proxy, and refresh mechanisms.

## Models and requests

The provider key is `github-copilot`. Model discovery uses the account's Copilot model catalog instead of a hard-coded list. Only chat models with a supported HTTP endpoint are registered. The executor chooses Chat Completions, Responses, or Messages according to the model's advertised endpoints, then reuses the existing protocol translators and usage accounting.

Existing OAuth model aliases, excluded models, and account prefixes also apply. Use `/v1/models` to find the models available to the connected account. Image generation, Responses compaction, and models available only over WebSocket are not advertised as supported.

GitHub OAuth credentials stay on the server. Inference uses a separate short-lived Copilot token, cached per account and proxy. Concurrent exchanges share one request, and the short-lived token is not written into the credential file.

## Allowance and usage

Quota Management and Auth Files show GitHub's reported allowance, amount used, percentage remaining, unlimited quotas, reset date, and paid-overage status when provided. Quotas are fetched on demand through the authenticated `/v0/management/github-copilot-quota?auth_index=...` endpoint.

Request token usage is recorded through the existing usage system. Copilot allowance is reported in GitHub's own units; it is not converted into an estimated dollar balance. Missing values remain unknown. Exhausted allowances use the existing account cooldown and failover path.

## Compatibility

GitHub's device authorization flow is documented, but the Copilot token, catalog, and quota endpoints are internal interfaces used by its clients. They may change. Available models and allowances depend on the signed-in account and organization policy.

Protocol references: [GitHub device authorization](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow), [Copilot token exchange](https://github.com/microsoft/vscode-copilot-chat/blob/main/src/platform/authentication/node/copilotTokenManager.ts), and [model catalog](https://github.com/microsoft/vscode-copilot-chat/blob/main/src/platform/endpoint/common/endpointProvider.ts).
