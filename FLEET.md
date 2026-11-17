# Fleet fork

Ryan's fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI),
run on a Synology DS218+ as the Claude and Codex account pool behind
`https://nas.tail92f75e.ts.net`.

## What this fork changes

- **`routing.strategy: earliest-reset`** (`sdk/cliproxy/auth/selector_earliest_reset.go`):
  - New sessions go to the credential whose weekly quota resets soonest, so capacity
    that is about to reset is used before it's lost.
  - A credential with any window at or above `routing.quota-threshold` (default
    `0.98`) is skipped.
  - Use it together with upstream's `session-affinity: true`.
- **Where quota comes from:**
  - The rate-limit headers upstream already records on every Claude and Codex response:
    5-hour, 7-day and the Fable `7d_oi` window.
  - A background reader (`sdk/cliproxy/quota_usage_reader.go`) reads the usage
    endpoint only for credentials with no reading newer than 30 minutes, one call per
    minute at most, and backs off on 429.
  - A failed or throttled read never takes a credential out of rotation. Only
    upstream's own cooldown after a real 429 does.
- **Quota history** (`sdk/cliproxy/quota_history_recorder.go`, `sdk/cliproxy/auth/quota_history.go`):
  - Every credential's weekly quota is recorded every minute to
    `<auth-dir>/quota-history.jsonl` and kept for 30 days.
  - It is served at `GET /v8/management/credentials/quota-history`.
  - It is not recorded in Home mode, and it is durable only with the file store.
- **Console:** `web/` is the prox console (from CLI Proxy API Console v1.25.6), with pooled
  Quota Management.
  - It reads `quota_windows` from `/v8/management/credentials`, so it never calls
    Anthropic or OpenAI.
  - The image builds it into `/CLIProxyAPI/static/management.html`, and
    `MANAGEMENT_PANEL_BAKED=1` stops the server from replacing it with upstream's
    release.
- **Image:** `ghcr.io/ryanzigler/cliproxyapi`, `linux/amd64`, published by
  `.github/workflows/docker-image.yml` for tags like `v8.0.23-fleet.1`.

## Release

```bash
git tag v8.0.23-fleet.2 && git push origin v8.0.23-fleet.2
```

The build publishes the image under that tag and as `latest`. The NAS runs `latest`,
so nothing on the NAS needs editing:

- **Automatic:** Watchtower on the NAS checks every night at 02:00 (America/Chicago),
  recreates `fleet-controller` on the new image and removes the old one.
- **Right away:** run this on the NAS once the build has finished.

  ```bash
  cd /volume1/docker/projects/fleet-controller && sudo docker compose pull && sudo docker compose up -d && sudo docker image prune -f
  ```

- **Roll back:** set a version tag such as `v8.0.23-fleet.1` in
  `/volume1/docker/projects/fleet-controller/compose.yaml` and run
  `sudo docker compose up -d` there. Watchtower leaves a pinned tag alone; put `latest`
  back to resume updates.

## Upstream updates

```bash
git fetch upstream --tags && git merge v8.0.24
```

Resolve conflicts, run the tests below, push `main` and tag `v8.0.24-fleet.1`.

## Tests

```bash
go test ./sdk/cliproxy/... ./internal/managementasset/ ./internal/api/handlers/management/
cd web && bun install && bun run lint && bun test
```

## NAS

- **Files:** `deploy/nas/compose.yaml` is the Container Manager project file. The NAS
  runs its own copy in the project folder, so a change to this file has to be copied there.
- **Folders:**
  - Data: `/volume1/docker/fleet-controller`, holding `config`, `auths`, `logs` and `keys` (the client key; not mounted).
  - Project: `/volume1/docker/projects/fleet-controller`.
- **First move:** `deploy/nas/migrate.sh` moves the old `/volume1/docker/cli-proxy-api`
  install over once.
