# Response attribution

CLIProxyAPI can return `X-CPA-TRACE-ID` on proxy responses after it selects a
credential. The header is also listed in `Access-Control-Expose-Headers`, so
browser clients can read it.

Support was added in v7.2.84.

## Read the header

```js
const response = await fetch(`${baseUrl}/v1/chat/completions`, request);
const traceId = response.headers.get("x-cpa-trace-id");
```

The current serialized value has this shape:

```text
<14-digit selection time>-<auth_index>-<8-hex request ID>
```

- The first field is the credential-selection time formatted as
  `YYYYMMDDHHmmss`. It uses the server process's local timezone and carries no
  offset; do not interpret it as UTC.
- Locally generated `auth_index` values are currently 16 hexadecimal
  characters. Existing or control-plane-supplied indexes can have another
  shape and can contain hyphens.
- The final field is CLIProxyAPI's 8-character hexadecimal request identifier.

Do not parse this value with an unbounded split on `-`. A consumer that needs
the current local `auth_index` can first validate the 14-digit prefix and
8-hex-character suffix, then treat everything between their delimiters as the
index. Treat every field as opaque, and tolerate a missing header or unknown
future shape instead of rejecting an otherwise valid response.

An `auth_index` is stable only while the identity used to derive or supply it
stays unchanged. Moving a credential file or changing its provider, base URL,
API key, or control-plane assignment can change the index. Do not use it as a
permanent account identifier.

## Correlate a credential

In a standard non-Home deployment with management routes enabled, the
management API's `GET /v0/management/auth-files` response includes an
`auth_index` for each returned file-backed or runtime-only credential. An
authorized integration can match the trace's current index to the selected
entry's value.

Configuration-backed API keys are not returned by that endpoint. Their
provider-specific management responses expose the value as `auth-index`.
Management responses can contain credential configuration and must be
restricted to trusted, authorized integrations.

Home mode disables the local management API and can use a control-plane-supplied
index. Do not assume that a Home trace can be resolved through the local
`/v0/management/auth-files` endpoint.

Use the index only for attribution, health reporting, or diagnostics. It is not
an API or management key.

## Presence and retries

The header reflects the most recent credential selection reported before
downstream response headers are committed. This includes streaming responses,
whose headers are committed before the stream body.

Ordinary HTTP responses, including SSE streams, can carry the header. WebSocket
attribution is route-specific: a handshake completed before credential
selection, including `/v1/responses`, cannot carry per-message credential
attribution.

The header can be absent when:

- authentication or validation fails before credential selection;
- the endpoint does not select a provider credential, such as a management
  endpoint; or
- a response is committed before selection metadata is available.

Retries and failover can select a different credential. Consumers must
attribute the response to the header they actually receive, not to a prior
attempt or a previously cached account.

## Not a routing control

`X-CPA-TRACE-ID` reports what CLIProxyAPI selected. It does not pin a credential
and does not create session affinity.

Session affinity is a separate routing feature. When enabled, clients can
supply a supported session identifier such as `X-Session-Affinity`. Affinity is
a sticky routing hint, not an exact credential selector. Replaying a trace or
sending an observed `auth_index` cannot select that credential.

## Privacy

CLIProxyAPI's locally generated `auth_index` contains no raw credential, but
existing and control-plane-supplied indexes are preserved. Operators and
control planes must never put credential material in an index.

Even without raw credentials, a locally generated index is a pseudonymous
fingerprint that stays stable while its identity seed is unchanged. For
configuration-backed API keys, it is derived from credential identity that
includes the key, so it can confirm guesses of weak credential values.

Log the trace only where request-to-account attribution is appropriate. Do not
attempt to reverse an index or expose credential or management-account
metadata. Apply appropriate access controls and retention. If downstream
clients must not receive the trace, strip the header at a trusted reverse proxy.
