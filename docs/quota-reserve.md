# Codex main/backup quota reserve

Credential priority controls which account receives requests. A higher-priority
main account is used before a lower-priority backup; round-robin does not split
requests between different priority tiers. A session-affinity binding may keep a
session on its backup after the main account recovers.

To stop sending new requests before an account exhausts its allowance, add
`"quota_reserve_percent": 5` to each Codex credential's JSON metadata. This setting
is opt-in and accepts a number greater than zero and less than 100. Missing,
zero, or invalid values leave existing routing unchanged.

For an existing file-backed credential, use the v8 management API:

```http
PATCH /v8/management/credentials/fields
Authorization: Bearer <management-key>
Content-Type: application/json

{"name":"codex-main.json","quota_reserve_percent":5}
```

Apply the same reserve to the backup credential. Keep the main account's priority
higher than the backup's.

With a 5% reserve, an account becomes ineligible at **95% used or higher** in
either of the primary or secondary quota windows. The policy uses the account's
latest upstream response headers, not a pool average or a forecast. Other
model-specific limit namespaces do not block unrelated models. The management
cooldown view reports the reason as `quota_reserve`.

The account becomes eligible again when every window at the cutoff has reset,
or a newer response snapshot reports usage below the cutoff. Absolute reset
timestamps take precedence; relative reset durations are anchored to the
observation time, not extended on every routing decision. A usage reading
without a valid reset time cannot establish a reserve hold. The policy does not
change credential disablement or overwrite upstream cooldown state.

If both accounts reach their reserve, the proxy returns its existing cooldown
response rather than spending the buffer. Active requests are not cancelled or
moved to another account. The reserve reduces the risk of hitting the actual
limit, but delayed upstream readings and requests already in flight can still
consume quota after the cutoff is observed. After a restart, routing needs a
new quota-bearing response before this response-header-based policy can act.
