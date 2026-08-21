# CLIProxyAPI outbound network audit

`audit_egress.py` inventories literal external destinations in production Go code and the active configuration, then samples the live CPA container's TCP connections.

It intentionally does **not** read or print request payloads, headers, API keys, OAuth tokens, or other secret values.

## Run

```bash
cd /home/francis_chiu/code/CLIProxyAPI
python3 scripts/security/test_audit_egress.py
python3 scripts/security/audit_egress.py \
  --watch-seconds 30 \
  --resolve-dns \
  --json-output /home/francis_chiu/cliproxyapi/reports/cpa-egress-audit.json \
  --markdown-output /home/francis_chiu/cliproxyapi/reports/cpa-egress-audit.md
```

For a meaningful runtime observation, start the audit first and make a normal CPA model request while the watch window is active.

## What the result means

- **Static destinations** show literal URL hosts that production code or active, uncommented configuration can reference.
- **Runtime observations** show TCP peers during the sampling window. The tool filters by init-process socket ownership when `/proc` permissions permit it; otherwise the observation is network-namespace scoped.
- Direction is inferred from the container's listening ports and TCP state. Entries marked `outbound-candidate` are evidence consistent with an outbound connection, not absolute proof of which process initiated it or what bytes were transferred.
- `--resolve-dns` performs PTR lookups and therefore creates its own DNS traffic; omit it when passive observation matters more than names.
- No unexpected connection in a short window is useful evidence, but not a mathematical proof that a dormant path can never connect later.

For stronger assurance, combine this report with an outbound firewall allowlist and longer packet/DNS logging at the Docker host or network gateway.
