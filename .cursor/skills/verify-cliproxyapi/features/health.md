# Health

Operators and load balancers poll `/healthz` to know the proxy is up; it needs no auth.

## Sub-features

- `health-ok` `GET /healthz` returns 200 without credentials.

## How to get to it (user POV)

- `curl http://<host>:<port>/healthz` (Docker healthchecks, k8s probes).

## Driving it with cpa-verify.sh

Preconditions: doctor OK.

- **Probe.** Run `scripts/cpa-verify.sh drive health`. `healthz.status` is `200`.

## Gotchas

- `/keep-alive` is only registered when keep-alive is configured; the harness records it (404 by default) but does not assert it.
- Launch readiness uses the same endpoint, so a failing health drive usually means the server crashed — read `$CPA_RUN/server.log`.
