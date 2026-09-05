# CodeBuddy Code TLS Fingerprint

This document records the TLS ClientHello profile captured from the official
`@tencent-ai/codebuddy-code` CLI and explains how CLIProxyAPI reproduces it for
CodeBuddy CN inference requests.

## Capture environment

- CodeBuddy Code: `2.137.1`
- Runtime: Node.js `v24.15.0`
- OpenSSL: `3.5.5`
- Target protocol: HTTPS to the CodeBuddy chat endpoint
- Capture method: override the CLI product endpoint through
  `ACC_PRODUCT_CONFIG_PATH`, direct the real CLI process to a local raw
  ClientHello collector, and bypass the desktop HTTP proxy for the experiment

The endpoint used a DNS name resolving to loopback so that the captured
ClientHello included the SNI extension, matching a connection to
`copilot.tencent.com`.

## Captured fingerprint

### JA3 string

```text
771,4866-4867-4865-49199-49195-49200-49196-158-49191-103-49192-107-163-159-52393-52392-52394-49325-49311-49245-49249-49239-49235-162-49324-49310-49244-49248-49238-49234-49188-106-49187-64-49162-49172-57-56-49161-49171-51-50-157-49309-49233-156-49308-49232-61-60-53-47,65281-0-11-10-35-22-23-13-43-45-51,4588-29-23-30-24-25-256-257,0-1-2
```

### JA3 MD5

```text
944d1e1858cd278718f8a46b65d3212f
```

### Observed JA4 prefix

```text
t13dd11_652810111035222313434551_
```

The JA4 value above is retained as an observational artifact from the capture
utility. The JA3 fields and raw extension ordering are the implementation's
primary compatibility target.

## ClientHello characteristics

### TLS extensions, in wire order

| ID | Extension |
|---:|---|
| 65281 | `renegotiation_info` |
| 0 | `server_name` (SNI) |
| 11 | `ec_point_formats` |
| 10 | `supported_groups` |
| 35 | `session_ticket` |
| 22 | `encrypt_then_mac` |
| 23 | `extended_master_secret` |
| 13 | `signature_algorithms` |
| 43 | `supported_versions` |
| 45 | `psk_key_exchange_modes` |
| 51 | `key_share` |

The official CLI does **not** advertise ALPN. Consequently, the transport uses
HTTP/1.1 without sending extension 16. Adding `h2` or `http/1.1` through ALPN
would change the fingerprint.

### Supported groups

```text
4588, 29, 23, 30, 24, 25, 256, 257
```

These correspond to:

- `X25519MLKEM768` (4588)
- `X25519` (29)
- P-256, P-384, P-521
- X448
- FFDHE 2048 and 3072

Group 4588 is an important Node 24/OpenSSL 3.5 characteristic and is supported
by the repository's existing `utls v1.8.2` dependency.

### EC point formats

```text
0, 1, 2
```

## CLIProxyAPI implementation

CodeBuddy CN inference requests use a provider-specific `utls.HelloCustom`
transport that:

1. preserves the captured cipher-suite ordering;
2. emits extensions in the captured order;
3. advertises the captured supported groups and point formats;
4. includes SNI but deliberately omits ALPN;
5. uses HTTP/1.1;
6. respects per-auth and global proxy configuration; and
7. keeps the profile scoped to CodeBuddy CN so other providers are unaffected.

TLS fingerprinting is only one possible server-side signal. The CodeBuddy CN
executor also reproduces the official HTTP headers and request-shape behavior,
including forced streaming and CodeBuddy-compatible reasoning parameters.

## Maintenance

The fingerprint is tied to a specific official CLI and Node/OpenSSL release.
Re-capture and update the transport when the advertised CodeBuddy CLI version or
its bundled Node runtime changes. A version change can alter cipher suites,
supported groups, signature algorithms, extension ordering, or ALPN behavior.
