# Release and GHCR Deployment Workflow

This fork is intended to use a pull-based production deployment flow:

1. Merge validated code into `main`
2. Create and push a Git tag like `fork/v7.10.43`
3. GitHub Actions builds release binaries and an amd64 Docker image
4. Production servers pull the published GHCR image
5. Restart the service without building on the production host

## Workflows

- `.github/workflows/docker-image.yml`
  - Triggered by pushing `fork/v*` tags
  - Publishes `linux/amd64` images to `ghcr.io/quqi1599/cliproxyapi`
  - Also supports manual reruns through `workflow_dispatch`
  - Manual reruns can optionally refresh the `latest` image tag
- `.github/workflows/release.yaml`
  - Triggered by the same `fork/v*` tags
  - Publishes GitHub Release artifacts for all supported platforms

## Published Image Tags

For a release tag like `fork/v7.10.43`, the Docker workflow publishes:

- `ghcr.io/quqi1599/cliproxyapi:fork-v7.10.43`
- `ghcr.io/quqi1599/cliproxyapi:v7.10.43`
- `ghcr.io/quqi1599/cliproxyapi:7.10.43`
- `ghcr.io/quqi1599/cliproxyapi:7.10`
- `ghcr.io/quqi1599/cliproxyapi:7`
- `ghcr.io/quqi1599/cliproxyapi:latest` on tag push

The safest production deployment uses the exact tag form:

```bash
export CLI_PROXY_IMAGE=ghcr.io/quqi1599/cliproxyapi:fork-v7.10.43
docker compose pull
docker compose up -d --remove-orphans --no-build
```

For repeatable deployments with readiness timestamps, use:

```bash
./scripts/deploy-ghcr-release.sh fork/v7.10.43
```

The script prints:

- `old_container_stop_at`
- `new_container_start_event_at`
- `port_8317_listen_at`
- `healthz_ok_at`
- `first_success_request_at`
- `first_failed_request_at`
- `last_failed_request_at`
- `connection_refused_request_count`

## Production Deployment

The current production host is `x86_64`, so the release workflow only builds `linux/amd64`.
If you later add ARM servers, you can reintroduce `linux/arm64` to the Docker workflow.

Use the exact release image on the server instead of building from source:

```bash
cd /opt/cliproxy
export CLI_PROXY_IMAGE=ghcr.io/quqi1599/cliproxyapi:fork-v7.10.43
docker compose pull
docker compose up -d --remove-orphans --no-build
docker compose ps
docker compose logs --tail 20 cli-proxy-api
curl http://127.0.0.1:8317/healthz
```

If the compose file already uses `${CLI_PROXY_IMAGE}`, you can replace the manual sequence with:

```bash
./scripts/deploy-ghcr-release.sh fork/v7.10.43
```

## Rollback

Rollback is the same process with an older image tag:

```bash
export CLI_PROXY_IMAGE=ghcr.io/quqi1599/cliproxyapi:fork-v7.10.42
docker compose pull
docker compose up -d --remove-orphans --no-build
```

The same deploy script accepts older tags:

```bash
./scripts/deploy-ghcr-release.sh fork/v7.10.42
```

## Debug Image And pprof

The regular release image stays stripped for normal production use. When you need
symbolized CPU or heap profiles, trigger the manual workflow:

- Workflow: `.github/workflows/docker-image-debug.yml`
- Input tag: `fork/v7.10.43`
- Published tag: `ghcr.io/quqi1599/cliproxyapi:fork-v7.10.43-debug`

Enable `pprof` in `config.yaml` and keep it bound to localhost:

```yaml
pprof:
  enable: true
  addr: 127.0.0.1:8316
```

Typical collection commands:

```bash
curl -fsS http://127.0.0.1:8316/debug/pprof/heap >/dev/null
go tool pprof http://127.0.0.1:8316/debug/pprof/profile?seconds=30
go tool pprof http://127.0.0.1:8316/debug/pprof/heap
```

## Local Developer Builds

Use local builds only for development verification:

```bash
./docker-build.sh
```

Then choose `2) Build from Source and Run (For Developers)`.

If you want to run a published release locally, keep the default option and set:

```bash
export CLI_PROXY_IMAGE=ghcr.io/quqi1599/cliproxyapi:fork-v7.10.43
./docker-build.sh
```
