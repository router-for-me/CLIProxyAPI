# Self-Heal Runbook

This runbook defines how to operate self-healing for a local CLIProxyAPI deployment under OpenClaw.

## Goal

Self-heal should reduce trivial downtime without hiding real incidents.

## Decision policy

### Auto-heal is appropriate when

Use automatic recovery for low-risk, reversible failures such as:
- service process/container is stopped
- health endpoint temporarily fails
- compose-managed service failed after restart/reboot
- transient local runtime issues where restarting the service is the normal first response

Typical action:
1. health check fails
2. run self-heal entrypoint
3. re-check health
4. if recovered, emit recovery status

### Alert-only is preferred when

Do **not** auto-restart immediately if the symptom suggests the service is up but behavior is degraded in a way that restart may hide the signal, for example:
- repeated flapping after multiple restarts
- suspected quota/provider upstream outage
- persistent auth/account failures
- config/schema mismatch after upgrade
- storage/corruption suspicion

Typical action:
1. report failing symptom
2. preserve evidence/logs
3. send alert
4. wait for operator decision

### Human intervention is mandatory when

Require operator review before further mutation if any of these are true:
- config change is required
- credentials/tokens may need rotation or re-login
- data migration / schema repair is involved
- repeated self-heal attempts failed
- failure may be caused by upstream breaking changes
- recovery would require deleting cache/data/auth artifacts

## Suggested escalation ladder

1. `curl /health`
2. `heartbeat_example.sh`
3. one self-heal attempt
4. second health check
5. if still failing: alert + stop automation loop

Avoid infinite restart loops.

## Guardrails

- Keep healthy output quiet: `HEARTBEAT_OK`
- Notify on recovery/failure/needs-attention only
- Limit auto-heal to a small bounded number of retries
- Preserve logs before destructive recovery steps
- Do not silently rewrite config as part of generic self-heal
- Do not hardcode machine-private paths into shared templates

## Evidence to capture on failure

Capture at least:
- `docker compose ps`
- recent service logs
- failing health URL
- last recovery action attempted
- whether the service recovered after self-heal

## Minimal verification after recovery

After self-heal claims success, verify all of:
1. `docker compose ps`
2. `curl -fsS http://127.0.0.1:8317/health`
3. a heartbeat run returns `HEARTBEAT_OK`
4. optional: one real API smoke request if your environment supports it

## False-success prevention

A restart that only makes the process exist again is **not** enough.

Treat recovery as successful only when:
- the health endpoint responds successfully
- heartbeat returns healthy status
- no immediate repeat failure occurs in the next scheduled check

## OpenClaw integration

Recommended pairing:
- `scripts/heartbeat_example.sh`
- `scripts/self_heal_example.sh`
- `openclaw/CRON_TEMPLATE.md`
- `openclaw/PROJECT_BOOTSTRAP.md`

## Operator note

Self-heal is a first-response tool, not a substitute for diagnosis.
