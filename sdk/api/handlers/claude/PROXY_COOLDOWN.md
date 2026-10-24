# Signed proxy cooldown metadata for Claude clients

A Claude client can opt in to a machine-readable prefix on proxy-generated
rate-limit errors by sending:

```http
X-CLIProxyAPI-Cooldown-Metadata: signed-v1
```

Without that exact header value, Claude error responses retain their existing
format. Signing is supported only with the built-in `config-inline` frontend access
provider and an authenticated principal present in the handler's configured
`api-keys`. The built-in provider's principal is the validated client key.
Frontend plugins, unknown/missing provider provenance, absent authentication,
and unconfigured principals suppress the marker, even when an inbound
Authorization header contains a token. Arbitrary provider principals may be
public account identifiers and are not signing keys. This feature does not
introduce another key, expose upstream credentials, or change retry policy.
The header name is case-insensitive; the value is case-sensitive.

## Meaning and eligibility

The metadata describes the earliest next reevaluation of the proxy's existing
credential availability state. It is **not** a subscription-window reset or a
promise that capacity will be available then.

Metadata is produced only when a final HTTP 429 is accompanied by a local
summary proving every eligible credential has a known future quota cooldown.
The summary includes previously attempted credentials and applies the request's
provider, registered executor, model, credential-kind/policy, pinned credential,
and selector weight restrictions. It considers every priority tier without
calling the selector or changing affinity, rotation, quota state, or deadlines.
An available credential, an unknown recovery time, a non-quota block, or an
empty eligible set suppresses metadata. Home, active plugin, and custom
schedulers are excluded because their availability cannot be established by
this local summary.

Initial exhaustion and subsequent cached selection failures are covered for
normal execution and stream bootstrap. Original errors remain in the error
chain, with their status, human detail, and header passthrough rules preserved.
The opted-in Claude JSON error and terminal SSE error serializers prepend the
marker. Direct-response passthrough remains untouched.

## Wire format

The error message starts with exactly:

```text
[[CPA_PROXY_COOLDOWN_V1:<epoch>:<mac>]] <original human message>
```

`epoch` is the mathematical ceiling of the absolute next reevaluation time in
Unix seconds, encoded as canonical positive decimal digits without leading
zeros. It is at most `253402300799`. Rounding the absolute time avoids rebuilding
a deadline from an already rounded duration.

`mac` is 64 lowercase hexadecimal characters: HMAC-SHA256 keyed by the UTF-8
bytes of the **existing authenticated downstream client key**. The exact signed
UTF-8 input includes the final newline:

```text
CLIProxyAPI/T3/proxy_cooldown/v1
next_reevaluation_at=<epoch>
```

The domain string is pinned for compatibility with the original consumer.
Clients must validate the canonical bounded epoch and MAC with a constant-time
comparison before consuming the deadline, and schedule only future deadlines.
Do not infer trust by searching arbitrary upstream text for a marker. The
signature authenticates a proxy deadline, without binding it to a model,
session, subscription window, or guarantee of future availability.

The prefix is used because Claude CLI/SDK paths can discard response headers
and unknown error-body fields while preserving the error message. A consumer
can verify and remove it at its SDK event boundary. This repository does not
install a launcher or automatically resume any client.

## Reproducible test vector

For the synthetic key `synthetic-downstream-token` and epoch `2000000000`, the
marker below is followed by one ASCII space before the human message:

```text
[[CPA_PROXY_COOLDOWN_V1:2000000000:d90b2648b13943391177836c85584a1900594412306f81a96aff937ee08b066b]]
```
