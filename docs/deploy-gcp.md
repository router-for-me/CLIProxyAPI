# GCP Cloud Run Deployment Guide

Production deployment of CLIProxyAPI on Google Cloud Run with zero-downtime rolling updates, custom domain via Cloudflare DNS, and external dashboard integration (e.g., N-leaderboard).

## Architecture

```
Cloudflare DNS (proxy.nomadamas.org)
  -> Global static IP (reserved, IPv4)
  -> Global External Application Load Balancer (HTTPS:443)
      -> Target HTTPS Proxy (Google-managed SSL cert)
        -> URL Map (default route)
          -> Backend Service (HTTP, EXTERNAL_MANAGED)
            -> Serverless NEG (asia-northeast3)
               -> Cloud Run service: cli-proxy-api
                * gen2 runtime
                * min=1, max=1, concurrency=200
                * 2 vCPU, 1Gi memory
                * --no-cpu-throttling (background goroutines stay alive)
                * OBJECTSTORE backend (GCS bucket via S3 HMAC interop)

GCS bucket (cli-proxy-api-prod-auths)
  ├── config/config.yaml   ← runtime configuration
  └── auths/               ← OAuth tokens, persistent across instance restarts

Secret Manager
  ├── objectstore-access-key   (HMAC accessId for the bucket)
  └── objectstore-secret-key   (HMAC secret for the bucket)
```

## Why max-instances=1

The usage queue and `api-key-usage` accounting structures are kept **in-process**.
With horizontal scaling enabled, each instance would maintain a separate, partial
view of usage data, causing N-leaderboard polls to fluctuate between instances
and miss aggregated totals.

`concurrency=200` absorbs traffic peaks on the single instance. For an I/O-bound
Go proxy (most time spent waiting on upstream LLM APIs), 200 concurrent requests
per vCPU is conservative.

If you exceed the single-instance ceiling later, the recommended evolution is:
1. Externalize the usage queue (e.g., Redis MemoryStore)
2. Make `api-key-usage` durable (Postgres or BigQuery sink)
3. Then raise `--max-instances` and add a Backend Service connection draining policy

## Configuration source of truth

Cloud Run reads `config.yaml` from `gs://cli-proxy-api-prod-auths/config/config.yaml`
via the `OBJECTSTORE_*` integration. To change the configuration:

```bash
# Edit a local copy
gcloud storage cat gs://cli-proxy-api-prod-auths/config/config.yaml > /tmp/config.yaml
$EDITOR /tmp/config.yaml

# Upload the new version (atomic replace)
gcloud storage cp /tmp/config.yaml gs://cli-proxy-api-prod-auths/config/config.yaml

# Restart the Cloud Run service to pick up the change immediately
gcloud run services update cli-proxy-api \
  --region=asia-northeast3 \
  --project=cli-proxy-api-prod \
  --update-env-vars="CONFIG_RELOAD_TRIGGER=$(date +%s)"
```

The proxy also watches the local mirrored file via fsnotify, but on Cloud Run
the mirror only refreshes on container startup, so a restart is the
fastest reliable refresh path.

For Cloud Run deployments, set `codex-ttft-timeout-seconds: 240` in
`config.yaml` so Codex requests fail before the platform's 900-second request
timeout when upstream produces no first response event.

## Building and deploying a new image

```bash
# 1. Build via Cloud Build (TDD gate runs go test ./scoped/... first)
gcloud builds submit \
  --config=cloudbuild.yaml \
  --region=asia-northeast3 \
  --project=cli-proxy-api-prod

# 2. Confirm new image present
gcloud artifacts docker images list \
  asia-northeast3-docker.pkg.dev/cli-proxy-api-prod/cli-proxy-api \
  --include-tags
```

## Zero-downtime rollout SOP

Cloud Run creates a new revision per deploy and only shifts traffic to it once
the new revision passes startup health checks. The old revision keeps serving
until traffic shifts.

### Canary pattern (recommended for risky changes)

```bash
# Deploy without taking traffic
gcloud run deploy cli-proxy-api \
  --image=asia-northeast3-docker.pkg.dev/cli-proxy-api-prod/cli-proxy-api/server:latest \
  --region=asia-northeast3 --project=cli-proxy-api-prod \
  --no-traffic --tag=canary

# Smoke-test the canary URL (gcloud prints it as canary--...run.app)
CANARY_URL=$(gcloud run services describe cli-proxy-api \
  --region=asia-northeast3 --project=cli-proxy-api-prod \
  --format='value(status.traffic[?tag==`canary`].url)')

curl -sS -H "Authorization: Bearer $MGMT_KEY" "$CANARY_URL/v0/management/api-keys"

# Gradual traffic shift (10% -> 50% -> 100%)
gcloud run services update-traffic cli-proxy-api \
  --to-revisions=<OLD_REVISION>=90,<NEW_REVISION>=10 \
  --region=asia-northeast3 --project=cli-proxy-api-prod
# ...wait, monitor errors, escalate ratio...
gcloud run services update-traffic cli-proxy-api --to-latest \
  --region=asia-northeast3 --project=cli-proxy-api-prod
```

### Rollback

```bash
# List recent revisions
gcloud run revisions list --service=cli-proxy-api \
  --region=asia-northeast3 --project=cli-proxy-api-prod

# Shift 100% traffic back to a known-good revision
gcloud run services update-traffic cli-proxy-api \
  --to-revisions=<KNOWN_GOOD_REVISION>=100 \
  --region=asia-northeast3 --project=cli-proxy-api-prod
```

## Operations cheatsheet

```bash
# View live logs
gcloud logging read \
  'resource.type="cloud_run_revision" AND resource.labels.service_name="cli-proxy-api"' \
  --project=cli-proxy-api-prod --limit=50 --freshness=5m \
  --format='value(timestamp,severity,textPayload)'

# Inspect deployed config
curl -H "Authorization: Bearer $MGMT_KEY" https://proxy.nomadamas.org/v0/management/config | jq .

# Live usage snapshot (aggregated, leaderboard-shaped)
curl -H "Authorization: Bearer $MGMT_KEY" https://proxy.nomadamas.org/v0/management/usage | jq .

# Drain queue (destructive POP, for external consumers)
curl -H "Authorization: Bearer $MGMT_KEY" "https://proxy.nomadamas.org/v0/management/usage-queue?count=100" | jq .
```

## External dashboard integration

CLIProxyAPI exposes `/v0/management/usage` as a cumulative, in-process aggregate
of token usage per api-key per model. The shape is:

```json
{
  "usage": {
    "apis": {
      "<api-key>": {
        "total_requests": 0,
        "total_tokens": 0,
        "models": {
          "<model-name>": {
            "details": [
              { "timestamp": "...", "tokens": { "input_tokens": 0, ... } }
            ]
          }
        }
      }
    }
  }
}
```

Authentication: `Authorization: Bearer <secret-key>` (or `X-Management-Key: ...`).

The data is held in-process and reset whenever the Cloud Run instance restarts.
Consumers should poll frequently (1-5s) and persist locally if they need history.

### N-leaderboard (HaD0Yun/N-leaderboard) wiring

```bash
export MGMT_KEY="<secret-key from secrets.env>"
export PROXY_URL="https://proxy.nomadamas.org"
N-leaderboard --live 3
```

## Adding a dashboard service later

When you want to host a separate dashboard frontend (e.g., your own React app)
on the same domain, add a path-based route to the existing URL map:

```bash
# 1. Deploy dashboard service
gcloud run deploy cli-proxy-dashboard \
  --image=... --region=asia-northeast3 --project=cli-proxy-api-prod \
  --port=3000 --allow-unauthenticated

# 2. Create NEG for it
gcloud compute network-endpoint-groups create cli-proxy-dashboard-neg \
  --region=asia-northeast3 \
  --network-endpoint-type=serverless \
  --cloud-run-service=cli-proxy-dashboard \
  --project=cli-proxy-api-prod

# 3. Create backend service
gcloud compute backend-services create cli-proxy-dashboard-backend \
  --global --load-balancing-scheme=EXTERNAL_MANAGED \
  --project=cli-proxy-api-prod
gcloud compute backend-services add-backend cli-proxy-dashboard-backend \
  --global \
  --network-endpoint-group=cli-proxy-dashboard-neg \
  --network-endpoint-group-region=asia-northeast3

# 4. Attach a path matcher to the existing URL map
gcloud compute url-maps add-path-matcher cli-proxy-api-urlmap \
  --path-matcher-name=dashboard-matcher \
  --default-service=cli-proxy-api-backend \
  --path-rules='/dashboard/*=cli-proxy-dashboard-backend' \
  --new-hosts=proxy.nomadamas.org
```

No DNS or SSL changes required; the new service piggybacks on the existing
forwarding rule, certificate, and IP.

## Resource inventory

| Resource | Name | Region/Scope |
|----------|------|--------------|
| GCP project | `cli-proxy-api-prod` | global |
| Cloud Run service | `cli-proxy-api` | asia-northeast3 |
| Artifact Registry repo | `cli-proxy-api` | asia-northeast3 |
| GCS bucket | `cli-proxy-api-prod-auths` | asia-northeast3, uniform access |
| Service account (runtime) | `cli-proxy-runtime@...` | project |
| Service account (interop) | `gcs-interop@...` | project |
| Secret: config (legacy, unused) | `cli-proxy-config` | asia-northeast3 |
| Secret: HMAC access | `objectstore-access-key` | asia-northeast3 |
| Secret: HMAC secret | `objectstore-secret-key` | asia-northeast3 |
| Global static IP | `cli-proxy-api-ip` | global |
| Serverless NEG | `cli-proxy-api-neg` | asia-northeast3 |
| Backend Service | `cli-proxy-api-backend` | global |
| URL Map | `cli-proxy-api-urlmap` | global |
| SSL certificate (managed) | `cli-proxy-api-cert` | global |
| HTTPS Proxy | `cli-proxy-api-https-proxy` | global |
| Forwarding Rule | `cli-proxy-api-https-fr` | global, :443 |

## Codex TTFT timeout

Cloud Run enforces a hard request timeout (`timeoutSeconds`, currently 900s).
Codex upstream can occasionally stall before producing the first response event,
causing the proxy to hold the HTTP response open without sending any bytes until
Cloud Run kills it with 504.

To mitigate this, set `codex-ttft-timeout-seconds` in `config.yaml`:

```yaml
codex-ttft-timeout-seconds: 240
```

When enabled, the proxy cancels the upstream request if no first response event
arrives within the configured duration and returns a 504 to the client
immediately, freeing the connection slot. The existing 5-minute WebSocket idle
timeout continues to apply after the first event is received.

Recommended value: `240` (4 minutes) for Cloud Run deployments with
`timeoutSeconds=900`.

## Known quirks

- **`/healthz` returns Cloud Run's default 404 page**, not the Gin handler. Cloud
  Run's Google Front End appears to intercept `/healthz` before it reaches the
  container. All other routes (including `/v0/management/*`, `/v1/*`, `/`) work
  normally. Use `/` or `/v0/management/api-keys` as your liveness probe instead.
- The `cli-proxy-config` Secret Manager secret was provisioned during initial
  bootstrap but is **not** wired into the running service - configuration is
  served from the GCS bucket. The secret is preserved for emergency restore.
- The aggregated `/v0/management/usage` endpoint is a custom addition (commit
  history under `internal/api/handlers/management/usage_aggregated.go`). It is
  not part of upstream `router-for-me/CLIProxyAPI`.

## Cost estimate (steady state, May 2026 pricing, asia-northeast3)

| Item | Monthly |
|------|---------|
| Cloud Run: 2 vCPU, 1Gi, min=1, always-on CPU | ~$50 |
| Global External Application LB (forwarding rule + first 5 rules free + data) | ~$18 |
| Cloud Storage (auths bucket, <1 GB, low ops) | <$1 |
| Artifact Registry storage (~500 MB images) | <$1 |
| Secret Manager (3 secrets, periodic access) | <$1 |
| Cloud Build (per-deploy, ~5min/build) | per-use |
| Cloud Logging/Monitoring (free tier) | $0 |
| **Total** | **~$73/mo before traffic** |

Add ~$5-10/mo for ~12k req/hour traffic (egress + LB data processing).
