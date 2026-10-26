# Kiro subscription credentials

Kiro is an OAuth subscription provider (`type: "kiro"`), not an OpenAI-compatible
API-key provider. The management center reuses its existing credential cards,
channel ownership, scheduling, model editor, quota refresh and usage log.

## Login and import

On the OAuth page, choose **Kiro subscription**, pick **AWS Builder ID**, **Google**
or **GitHub**, and complete authorization in your browser. In multi-user mode select
a channel first. Cancelling a pending authorization prevents the credential from
being saved. The command-line equivalent is:

```bash
go run ./cmd/server --config config.yaml --kiro-login --no-browser
go run ./cmd/server --config config.yaml --kiro-login --kiro-method github
```

Google/GitHub use the official IDE's PKCE flow on the desktop auth service
(`/login` then `/oauth/token`). Upstream only accepts the IDE's registered
redirect `http://localhost:<port>/oauth/callback?login_option=<google|github>`
on ports 3128, 4649, 6588, 8008, 9091, 49153, 50153, 51153, 52153 or 53153, so
the server listens on the first free one of these for up to ten minutes. When
the browser is on another machine, its final localhost page does not load; paste
that full URL into the callback field (the shared OAuth callback endpoint). The
saved credential has the same shape as a social import (`provider`,
`auth_method: social`, email from the ID token when present). The GitHub step
may show an AWS WAF human-verification page before GitHub.

The v8 authorization endpoint also accepts `region` and `start_url` for an IAM
Identity Center device authorization. The default is AWS Builder ID in
`us-east-1`. Organizations can disable device authorization; external identity
providers requiring browser-only authentication must use imported credentials.

Existing Kiro desktop credentials and account-manager exports can also be uploaded
through the credential page, one account per JSON file. Google/GitHub exports need
no additional `type` field:

```json
{
  "email": "user@example.com",
  "refreshToken": "<refresh token>",
  "provider": "Github"
}
```

`Google` and `Github` select social authentication. `BuilderId` and `Enterprise`
select OIDC and require the original client registration. Recognized exports are
registered as `type: "kiro"`; an explicit different credential type is never
reclassified. The email and identity provider are retained. Native camelCase
fields and canonical snake_case fields are accepted; explicit canonical values
win. A native credential can also identify Kiro directly:

```json
{
  "type": "kiro",
  "accessToken": "<access token>",
  "refreshToken": "<refresh token>",
  "expiresAt": "<RFC3339 expiry>",
  "profileArn": "<profile ARN, when provided by Kiro>",
  "region": "us-east-1",
  "authMethod": "social"
}
```

For Builder ID / Identity Center sessions use `authMethod: "idc"` and include the
original `clientId` and `clientSecret` from that session's client registration.
A refresh token alone cannot refresh an OIDC session without its registered
client. Do not replace those fields with a new registration. Social credentials
use the desktop refresh endpoint in `us-east-1`, independently of the inference
region. Refresh-only imports are accepted without making a network call during
upload. Before inference, model discovery or quota queries, the server obtains
an access token and resolves a missing profile ARN through ListAvailableProfiles.
Rotated refresh tokens and resolved profiles are persisted. Requests also prepare
tokens with unknown expiry or less than five minutes remaining. Preparation and
scheduled/manual refresh share a per-credential lock to avoid simultaneous token
rotation. Empty or failed profile lookups defer the next attempt for five minutes;
a manual credential refresh permits an immediate retry. External IdP refresh and
Kiro API keys are not implemented by this OAuth provider.

Runtime, quota, control-plane and social-refresh requests use the client headers
observed in the Kiro account-manager reference. These headers do not establish
that upstream will treat the proxy as an IDE or prevent account restrictions.
Runtime inference (`generateAssistantResponse`) sends the `aws-sdk-js/1.0.39` streaming
user agent with a `KiroIDE-<version>-<machineId>` suffix, `amz-sdk-invocation-id`,
`amz-sdk-request`, `x-amzn-codewhisperer-optout: true`, `x-amzn-kiro-agent-mode`
and `x-amzn-kiro-profile-arn`; quota (`getUsageLimits`) sends the
`aws-sdk-js/1.0.0` `codewhispererruntime` management identity; profile and model
lookups send the `kirocontrolplanebearer` control-plane identity; social refresh
sends the desktop auth client's pinned `KiroIDE-0.6.18-<machineId>` user agent.
Existing `machineId` / `machine_id` values are retained, with explicit canonical
fields taking precedence. Missing identities are derived as stable UUIDs from
the account provider and email (or token when email is absent), then persisted
with the credential. Reading an unchanged import repeatedly or constructing
concurrent request headers does not generate a different identity. The
`ide_version` credential metadata key overrides the default version `1.4.26`.
Management and hub quota requests replace the caller's user-agent headers
server-side. The inference profile header mirrors the final native `profileArn`,
including user payload overrides and filters.

## Models and compatible APIs

The initial model is `auto`. Kiro's official model page names models but does not
publish a complete stable model-ID catalog. The credential model editor can fetch
account models through `management.<region>.kiro.dev` using the AWS JSON target
`KiroControlPlaneBearerService.ListAvailableModels`, or accept exact IDs manually.
Discovery follows pagination, preserves upstream IDs (including slashes), and
reads context/output limits and supported input types. Account model discovery
failure is reported, not replaced by a guessed list. A fetched catalog is cached
for three hours; subsequent fetches replace it. Saved model-editor rows are not
overwritten by discovery.

Copy the full channel model ID from the marketplace, for example
`573-kiro#1/auto`. OpenAI Chat Completions, OpenAI Responses and Claude Messages
requests use the existing translators. Kiro-specific conversion supports system
text, multi-turn history, inline images, tool definitions, tool calls/results,
streaming and non-streaming responses. Images inside tool results are carried by
the same user message's native image list; tool IDs and text remain intact.
Unsupported tool-result content is rejected, not silently removed. Native
reasoning text is forwarded, not fabricated. Historical thinking/signature blocks are not replayed. Assistant
prefill, forced tool choice, remote image
URLs and server-side tools are rejected rather than silently discarded.
Token counting is a local estimate, not an upstream Kiro token-count endpoint.

Inference uses `/generateAssistantResponse` and validates AWS event-stream frame
lengths and checksums. Truncated frames, incomplete tool calls and upstream
exceptions fail the request rather than emitting a successful stream terminator.
Responses tool-argument translation failures also propagate as stream errors and
failed usage records, without recording the request as successful.
Upstream token metadata is used when present, including cache reads/writes;
credit metering is not mislabeled as token usage. Requests without token metadata
do not invent usage. User payload rules run once on the final native Kiro JSON
with protocol `kiro`, after message conversion. The official Kiro 1.1.571 runtime
request schema has no sampling or output-token-limit fields; client `max_tokens`,
`temperature` and `top_p` are not enforced by this subscription endpoint.

## Quota and endpoints

Quota requests use `management.<region>.kiro.dev/getUsageLimits` through the
existing API-call proxy, with `resourceType=AGENTIC_REQUEST` and
`isEmailRequired=true`. Region and profile ARN are resolved after credential
preparation, including for members viewing a mounted
credential without permission to download its tokens. Monthly, active trial and
bonus credits keep their own reset/expiry dates; the existing reset timeline and
expiring-quota page include these rows. Credit consumption is separate from the
per-request token accounting.

`region` selects authentication; `api_region` selects inference/control services.
Without `api_region`, a profile ARN's region takes precedence over `region`.
The optional credential metadata `base_url` and `control_url` override inference
and control endpoints, respectively. Credential and global proxy settings reuse
the existing proxy-aware transport. Inference streams have no application timeout.

## Verification boundary and references

Automated verification uses local mock OIDC, social token, refresh, model-list
and binary inference responses. On 2026-10-26 the real `/login` accepted the
generated Google and GitHub authorization URLs and redirected to Google sign-in
and the GitHub authorize step; a completed real Google/GitHub sign-in and code
exchange has not been run. Live checks on 2026-10-25, before the subsequent review
fixes, succeeded with an imported GitHub social credential: refresh, profile
resolution, model discovery, streaming and non-streaming inference with
`claude-haiku-4.5`, and quota queries. The reviewed identity and final-profile-header
fixes are covered by local regression tests, including source-file reloads,
canonical-field precedence, stable request identities, and payload filters and
overrides.

- [Kiro authentication](https://kiro.dev/docs/getting-started/authentication/)
- [Kiro models and plan availability](https://kiro.dev/docs/models/)
- [Official Amazon Q CLI authentication and Smithy clients](https://github.com/aws/amazon-q-developer-cli)
- [Kiro account manager protocol and import reference](https://github.com/hj01857655/kiro-account-manager/tree/c30d6015149ef605fa4896693709a08313a12d50)
- [Kiro gateway protocol reference](https://github.com/jwadow/kiro-gateway)
- [Bundled Kiro icon source](https://kiro.dev/icon.svg)
