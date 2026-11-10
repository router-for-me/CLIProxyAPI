# Qoder OAuth subscriptions

CLIProxyAPI supports separate Qoder International and Qoder China credentials:

| Edition | Provider key | Login flag | Authorization site |
| --- | --- | --- | --- |
| International | `qoder` | `--qoder-login` | `https://qoder.com` |
| China | `qoder-cn` | `--qoder-cn-login` | `https://qoder.cn` |

The management OAuth page exposes both entries. The shared v8 API accepts
`GET /v8/management/oauth/auth-url?provider=qoder` and the corresponding
`qoder-cn` value. The two regions have independent OpenAPI and inference hosts;
their credentials are not interchanged.

## Flow and storage

Qoder CLI uses a PKCE device-authorization flow. CLIProxyAPI creates the
challenge locally, opens the regional authorization page, polls
`/api/v1/deviceToken/poll`, and resolves the account through `/api/v1/userinfo`.
Tokens rotate through `/api/v1/deviceToken/refresh` before expiry. The persisted
JSON includes the region, account identity, machine identity, tokens, and expiry
fields needed after a restart.

The protocol baseline is the official CLI 1.1.85 packages
`@qoder-ai/qodercli` and `@qodercn-ai/qoderclicn`. Qoder's public SDK documents
PAT, service-account, and local-CLI authentication separately; this provider uses
the CLI subscription device flow, not Cloud Agents PAT/SAT authentication.

Official references:

- https://docs.qoder.com/cli/quick-start
- https://docs.qoder.com/cli/sdk/authentication
- https://www.npmjs.com/package/@qoder-ai/qodercli
- https://www.npmjs.com/package/@qodercn-ai/qoderclicn

## Quota

The panel reads the official CLI's account endpoints with the credential's
bearer token: `GET /api/v2/quota/usage` (plan, add-on and organization
credit buckets, plus the cycle `expiresAt`) and `GET /api/v2/user/plan` (plan
name). Both editions share one quota adapter; the backend pins the OpenAPI host
to the credential's region (`openapi.qoder.sh` or `openapi.qoder.com.cn`) and
refreshes an expiring token before the call.

## Models and requests

The model list is account-bound. CLIProxyAPI fetches `/algo/api/v2/model/list`
for each credential, keeps the upstream model IDs unchanged, and preserves the
published context, output, visual, and reasoning capabilities. No static mapping
is substituted when discovery fails.

COSY signing requires the AES key to be printable ASCII (`uuid[:16]`, as the
official client does). Random key bytes make both gateways answer
`{"code":"101","message":"Signature invalid"}`, which broke model discovery and
chat. A failed refresh keeps the last account catalog and returns the upstream
reason to the panel.

Chat requests use Qoder's COSY-signed, encoded SSE protocol. OpenAI, Claude, and
Gemini inputs are translated to the Qoder business payload; payload rules run
once on that final payload before transport encoding and signing. Streaming,
non-streaming responses, tool calls, reasoning content, and token usage use the
existing shared translators and accounting pipeline.
