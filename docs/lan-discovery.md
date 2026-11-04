# LAN AI gateway discovery

CPA advertises `_ai-gateway._tcp` using the profile in
[draft-su-ai-gateway-discovery-00](https://datatracker.ietf.org/doc/draft-su-ai-gateway-discovery/).
Discovery is opt-in. Management routes are never advertised; the deprecated
`advertise-management` configuration field is accepted but ignored.

## TXT records

Advertisements place `txtvers`, `path`, `tls`, and `auth` first. Optional keys
are `protocols`, `auth_type`, `product`, `id`, and `role`, in that order.
Each character-string is at most 255 bytes and the complete TXT payload,
including length prefixes, is at most 400 bytes. Optional values are omitted
whole when they do not fit. The capability list is derived from advertised
subtypes. Version 1 booleans use `0` and `1`.

Browsers compare ASCII keys case-insensitively without trimming whitespace.
The first occurrence wins, including bare keys. Unsupported `txtvers` or invalid
required attributes cannot fall back to the old CPA schema. Known draft values
must satisfy the profile's printable ASCII and token syntax. Unknown protocol
and authentication tokens remain in `raw_txt` but are not exposed as recognized
capabilities or assigned operation URLs.

Legacy CPA advertisements using `version=1`, `api_*`, and `auth_required` are
accepted separately. If their optional `protocols` list was omitted, only the
advertised API base paths are exposed; no operation support is inferred.

## Resolution and interface scope

The browser uses a bounded, per-interface DNS record cache rather than the
zeroconf dependency's single-packet browse implementation. PTR, SRV, TXT, A,
and AAAA may arrive in separate packets in any order. Missing records are
queried with exponential backoff, while known PTR lifetimes independently
trigger refreshes before expiry. Repeated queries include fresh known answers;
long lists use bounded, TC-marked continuation packets. TTL expiration,
cache-flush, and goodbye one-second grace periods apply. At cache capacity,
flush replacements take precedence over the old records being retired. Expired records do not remain in returned
results. Full subtype queries such as `_responses._sub._ai-gateway._tcp` resolve
the PTR target's main service records.

Only responses from UDP port 5353 on selected interfaces are accepted. Receive
interface and destination control messages are required; platforms unable to
provide them fail closed. Multicast responses must target the standard mDNS
group; unicast responses must target a local interface address and originate
on-link (RFC 6762 Section 11). Joining a multicast group alone is not treated
as receive isolation. Default interface selection follows CPA's physical LAN
filter and never falls back to all interfaces.

Loopback, unspecified, multicast, and limited-broadcast addresses are filtered.
IPv6 link-local results retain their receiving interface in `ipv6_zones`, keyed
by address string. `interface_index` distinguishes same-name instances on
different links. Consumers must use the zone when connecting to a link-local
address. CLI WebSocket URLs use `ws`/`wss`; scoped URI hosts escape the zone
separator as `%25`. Unscoped link-local addresses are not offered as usable
connection targets.

The cache admits at most 256 service instances, 17,408 resource records, and
4 MiB of accounted record storage. Address RRsets are capped at 64 combined
IPv4/IPv6 addresses per host/interface; TXT records have at most 64 strings.
Legacy TXT input is capped at 16 KiB; draft input remains capped at 400 bytes.
These bounds constrain retained data, not transient allocations made by the
DNS packet decoder or exact process heap usage.

`Browse` honors its caller's context without imposing an internal deadline.
Cancellation closes sockets, joins reader goroutines, and returns the currently
valid partial results. Reaching 256 resolved services may end the scan early.

## Trust boundary

mDNS is unauthenticated. Source/interface validation reduces accidental and
off-link candidate pollution; it does not authenticate an on-link advertiser.
Neither `product`, `id`, instance names, nor advertised hostnames establish trust.
The discover CLI displays candidates and never connects to them or sends keys.
Clients must obtain user approval or match preconfigured trust rules before
connecting, validate TLS identity independently when applicable, and never
forward credentials to a different origin automatically. Querying model
catalogs is optional; `auth=1` catalogs require credentials.

## Verification

Deterministic tests exercise all 120 orderings of the five resolution records,
TTL/cache-flush/goodbye behavior, per-interface separation, source acceptance,
subtype resolution, size boundaries, legacy compatibility, and scoped URLs.
The existing live multicast integration test additionally checks discovery of
CPA's advertiser on the local machine. This is not a complete RFC conformance
or cross-platform network test suite.
