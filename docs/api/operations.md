# Operations API

Operations endpoints are used for liveness checks, routing visibility, and incident triage.

## Audience Guidance

- SRE/ops: integrate these routes into health checks and dashboards.
- Developers: use them when debugging routing/performance behavior.

## Core Endpoints

- `GET /health` for liveness/readiness style checks.
- `GET /v1/metrics/providers` for rolling provider-level performance/usage stats.

## Monitoring Examples

Basic liveness check:

```bash
curl -sS -f http://localhost:8317/health
```

Provider metrics snapshot:

```bash
curl -sS http://localhost:8317/v1/metrics/providers | jq
```

Prometheus-friendly probe command:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://localhost:8317/health
```

## Suggested Operational Playbook

1. Check `/health` first.
2. Inspect `/v1/metrics/providers` for latency/error concentration.
3. Correlate with request logs and model-level failures.
4. Shift traffic (prefix/model/provider) when a provider degrades.

## Failure Modes

- Health endpoint flaps: resource saturation or startup race.
- Provider metrics stale/empty: no recent traffic or exporter initialization issues.
- High error ratio on one provider: auth expiry, upstream outage, or rate-limit pressure.

## Related Docs

- [Routing and Models Reference](/routing-reference)
- [Troubleshooting](/troubleshooting)
- [Management API](./management.md)
