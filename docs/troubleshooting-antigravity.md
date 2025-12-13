# Troubleshooting: Antigravity (Claude/Gemini) via CLIProxyAPI

This guide covers common failure modes when using Antigravity-backed models such as:

- `gemini-claude-opus-4-5-thinking`
- `gemini-claude-sonnet-4-5-thinking`

## Auth files: where to store them

CLIProxyAPI watches the configured `auth-dir` (default `~/.cli-proxy-api`) for auth JSON files.

- Recommended: keep Antigravity auths at the **root** of `auth-dir`:
  - `~/.cli-proxy-api/antigravity-<email>.json`
- Avoid moving them into ad-hoc subfolders unless the docs explicitly say so.

If you use a custom endpoint pin (recommended), set it on the auth file as a **top-level** field:

```json
{
  "type": "antigravity",
  "email": "you@gmail.com",
  "project_id": "your-project",
  "base_url": "https://daily-cloudcode-pa.sandbox.googleapis.com"
}
```

## Login does not open a browser

CLIProxyAPI login flows are provided as flags (not subcommands):

- Antigravity: `cli-proxy-api -antigravity-login`
- Show URL without opening browser: `cli-proxy-api -antigravity-login -no-browser`

If it prints `Waiting for ... callback...`, keep the process running until the OAuth redirect completes.

## 400 INVALID_ARGUMENT: Unknown name "$ref" (tools schema)

Symptoms:

- `400 Invalid JSON payload received. Unknown name "$ref" at request.tools[...]...`

Cause:

- Antigravity / Gemini Code Assist rejects JSON Schema documents that contain `$ref` (and some related meta keys) in function parameter schemas.

Fix:

- Remove `$ref` / `$defs` usage in tool schemas, or inline referenced definitions.
- If you control the proxy, consider sanitizing tool schemas before sending upstream.

## 400 invalid_request_error: tool_use without tool_result

Symptoms:

- `tool_use ids were found without tool_result blocks immediately after ...`

Cause:

- Some clients send `tool_use` messages but fail to send the required `tool_result` message right after.

Fix:

- Ensure your client always appends a `tool`/`tool_result` message immediately after each tool call.
- For OpenAI-style Chat Completions, this means:
  - assistant message with `tool_calls`
  - next message(s) with role `tool` using the same `tool_call_id`

## 403 PERMISSION_DENIED: SUBSCRIPTION_REQUIRED after a 429

Symptoms:

- First response: `429 RESOURCE_EXHAUSTED RATE_LIMIT_EXCEEDED`
- Immediately followed by: `403 PERMISSION_DENIED SUBSCRIPTION_REQUIRED`

Cause:

- The executor fell back to another Antigravity base URL/project that requires a Gemini Code Assist license.

Fix:

- Pin Antigravity to the intended base URL by setting `base_url` on the auth file (top-level).
- Prefer `https://daily-cloudcode-pa.sandbox.googleapis.com` if that is your working endpoint.

## 429 model_cooldown (all credentials cooling down)

Symptoms:

- `429` with message like: `All credentials for model ... are cooling down`
- `Retry-After: <seconds>`

Cause:

- Every available credential for that provider+model hit quota and is in cooldown.

Fix:

- Respect `Retry-After` (do not hammer the endpoint during cooldown).
- Reduce concurrency / bursts from the client.
- Add more accounts/projects (each valid credential increases total available quota).

## 400 INVALID_ARGUMENT: max_tokens too large

Symptoms:

- `max_tokens: 65536 > 64000, which is the maximum allowed ...`

Fix:

- Lower `max_tokens` to the model maximum allowed by the upstream.

