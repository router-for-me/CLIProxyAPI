# CLIProxyAPI monolith deployment

## Goal

The `monolith` flow deploys CLIProxyAPI to a remote Docker host as one long-lived
container, in three steps:

1. build the app-specific base image on the remote Docker host
2. start the base runtime container
3. build a release package and publish it into the running container

In this mode:

- the container keeps its own sshd (used as the release channel) and supervisord
- CLIProxyAPI state (`config.yaml`, `auths`, `plugins`, `logs`, `static`) lives on the
  persistent `/data` volume, never inside a release directory
- a release is a tarball holding the binary plus `bin/{start,stop,restart,status}.sh`;
  publishing extracts it to `/app/releases/<release>-<timestamp>` and flips `/app/current`
- the base image carries no database and no Redis: CLIProxyAPI is file-based

## Defaults

| Item | Value |
| --- | --- |
| Container name | `cliproxyapi-monolith` |
| Base image | `cliproxyapi-monolith-base:linux-amd64` |
| Host app dir | `/app/monolith-cliproxyapi` (mounted to `/app`) |
| Host data dir | `/data/monolith-cliproxyapi` (mounted to `/data`) |
| Container app dir | `/app/current` (releases under `/app/releases`) |
| Container data dir | `/data` |
| API / management port | host `8082` -> container `8317` |
| Release SSH port | host `58524` -> container `22` |
| OAuth callback ports | `1455`, `54545`, `51121`, `11451`, `8085` (host -> container, 1:1) |
| Container SSH users | `deploy` / `deploy123`, `root` / `root123` (override via `APP_USER`, `APP_PASSWORD`, `ROOT_PASSWORD`) |
| Example host | `172.16.0.133` |

Inside the container, `/data` holds:

```text
/data/config.yaml    runtime config, editable from the management panel
/data/auths          credentials (OAuth token JSON files, uploaded auth files)
/data/plugins        plugin store installs
/data/logs           application logs (WRITABLE_PATH=/data)
/data/static         management panel assets downloaded from GitHub
/data/home           HOME for the process, keeps any ~-based state on the volume
```

`bin/start.sh` exports `WRITABLE_PATH=/data` and `HOME=/data/home`, and starts the binary as
`cli-proxy-api --config /data/config.yaml`. Both `auth-dir` and `plugins.dir` are resolved
relative to the working directory when they are relative, so the deployed `config.yaml` must
keep them absolute (`/data/auths`, `/data/plugins`).

## 1. Build the remote base image

```bash
REMOTE_HOST=172.16.0.133 REMOTE_PORT=22 REMOTE_USER=root \
  bash ./scripts/monolith/build-monolith-base-remote.sh
```

Force a rebuild with `--rebuild`. The script uploads `deploy/monolith/base/*` to
`/tmp/cliproxyapi-monolith-build` on the host and runs `docker build` there.

## 2. Start the remote base container

```bash
REMOTE_HOST=172.16.0.133 REMOTE_PORT=22 REMOTE_USER=root \
HTTP_PORT=8082 SSH_PORT=58524 \
  bash ./scripts/monolith/start-monolith-remote.sh
```

Add `--recreate` to drop and re-create an existing container. The script creates the host
directories, publishes the ports above and runs the container with
`--restart unless-stopped`.

## 3. Prepare the runtime config

The config is user-specific and lives on the data volume, so it is **not** part of a release.
Install it once at `/data/monolith-cliproxyapi/config.yaml` (inside the container:
`/data/config.yaml`) and keep `port: 8317`, `host: ""`, `auth-dir: "/data/auths"` and
`plugins.dir: "/data/plugins"`.

A quick first cut: copy `config.example.yaml`, set the two absolute paths above plus your
`api-keys`, and let the management panel take over from there — the management API rewrites
`config.yaml` in place, so subsequent panel edits persist across releases.

## 4. Build and publish a release

Build on the host (no local Docker or Go required), then publish into the container over its
SSH endpoint:

```bash
# on the Docker host
cd /data/cliproxyapi
GOPROXY=https://goproxy.cn,direct GOTOOLCHAIN=go1.26.0 CGO_ENABLED=1 \
  bash scripts/monolith/build-monolith.sh --version 20260929-2eb05786
# -> build/monolith/cliproxyapi-<version>-linux-amd64.tar.gz

# publish from the Docker host (the container endpoint is reachable on 127.0.0.1)
SSH_OPTS="-i /root/.ssh/id_ed25519_cliproxyapi -o StrictHostKeyChecking=no" \
REMOTE_HOST=127.0.0.1 REMOTE_PORT=58524 REMOTE_USER=deploy \
  bash scripts/monolith/deploy-monolith.sh \
  --package build/monolith/cliproxyapi-<version>-linux-amd64.tar.gz \
  --container cliproxyapi-monolith
```

`deploy-monolith.sh` uploads the tarball, extracts it to
`/app/releases/<release>-<timestamp>`, switches `/app/current`, runs `./bin/restart.sh`
(`supervisorctl restart cliproxyapi`) and verifies `./bin/status.sh`, collecting logs on
failure.

### Host-to-container release key

Publishing authenticates to the container as `deploy`, so create a dedicated key once on the
Docker host and install it through the container's password login (shipped as
`deploy/deploy123`, override with `APP_PASSWORD`):

```bash
ssh-keygen -t ed25519 -N '' -C 'cliproxyapi-monolith-deploy' -f /root/.ssh/id_ed25519_cliproxyapi

export SSHPASS='deploy123'   # container APP_PASSWORD
PUB="$(cat /root/.ssh/id_ed25519_cliproxyapi.pub)"
sshpass -e ssh -o StrictHostKeyChecking=no -p 58524 deploy@127.0.0.1 \
  "mkdir -p ~/.ssh && chmod 700 ~/.ssh && \
   { grep -qxF '${PUB}' ~/.ssh/authorized_keys 2>/dev/null || echo '${PUB}' >> ~/.ssh/authorized_keys; }; \
   chmod 600 ~/.ssh/authorized_keys"

# verify
ssh -i /root/.ssh/id_ed25519_cliproxyapi -o StrictHostKeyChecking=no -p 58524 deploy@127.0.0.1 \
  'bash -lc "cd /app/current && ./bin/status.sh && cat VERSION"'
```

Keep `SSH_OPTS="-i /root/.ssh/id_ed25519_cliproxyapi -o StrictHostKeyChecking=no"` in the publish
command above. Without this key you can do the same with Docker only:

```bash
VERSION=20260929-2eb05786
PKG=/data/cliproxyapi/build/monolith/cliproxyapi-${VERSION}-linux-amd64.tar.gz
STAMP=$(date +%Y%m%d%H%M%S)
docker cp "$PKG" cliproxyapi-monolith:/app/releases/
docker exec cliproxyapi-monolith bash -lc "
  set -euo pipefail
  cd /app/releases
  rm -rf cliproxyapi-${VERSION}-linux-amd64-${STAMP}
  mkdir -p cliproxyapi-${VERSION}-linux-amd64-${STAMP}
  tar -xzf cliproxyapi-${VERSION}-linux-amd64.tar.gz -C cliproxyapi-${VERSION}-linux-amd64-${STAMP} --strip-components=1
  rm -rf /app/current
  ln -s /app/releases/cliproxyapi-${VERSION}-linux-amd64-${STAMP} /app/current
  cd /app/current && ./bin/restart.sh && ./bin/status.sh"
```

## 5. Verify

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://172.16.0.133:8082/healthz   # 200
curl -s http://172.16.0.133:8082/healthz                                     # {"status":"ok"}
docker exec cliproxyapi-monolith bash -lc 'cd /app/current && ./bin/status.sh && cat VERSION'
```

Management panel: `http://172.16.0.133:8082/management.html` (needs the management key).

## Logs

CLIProxyAPI logs to `/data/logs` (`logging-to-file: true`), and supervisord/sshd log to the
container stdout:

```bash
docker logs -f cliproxyapi-monolith
docker exec cliproxyapi-monolith tail -f /data/logs/main.log      # app log
docker exec cliproxyapi-monolith tail -n 100 /data/logs/supervisor/supervisord.log
```

## Upstream credentials (OAuth)

Provider callback URLs are `http://localhost:<port>`, resolved by the **browser**, not by the
server. The published callback ports therefore only help when the browser can reach them —
for example through an SSH tunnel from your workstation
(`ssh -L 1455:localhost:1455 -L 54545:localhost:54545 root@172.16.0.133`). The alternatives
that need no tunnel:

- device-code login inside the container:
  `docker exec -it cliproxyapi-monolith bash -lc 'cd /data && /app/current/cli-proxy-api --codex-device-login --config /data/config.yaml'`
- upload an existing auth JSON through the management panel, or drop the file into
  `/data/monolith-cliproxyapi/auths` on the host

Credentials land in `/data/auths` and survive every release.

## Notes and troubleshooting

- **GitHub is slow or unreachable from the Docker host**: `git clone` may crawl (tens of KB/s)
  or stall. Use a GitHub proxy for the checkout, for example
  `git clone --depth 1 https://gh-proxy.com/https://github.com/<owner>/<repo>.git`, or point the
  management panel download at a reachable mirror.
- **Go modules**: `proxy.golang.org` is not reachable from most CN hosts, so always build with
  `GOPROXY=https://goproxy.cn,direct`. `GOTOOLCHAIN=go1.26.0` picks the toolchain required by
  `go.mod` (it is fetched through `GOPROXY` and cached under `$GOMODCACHE`).
- **CGO**: the build defaults to `CGO_ENABLED=1`, matching the upstream `Dockerfile`, so Go
  plugins keep working. Set `CGO_ENABLED=0` for a plain static build.
- **Disk**: a full Go build needs a few GB of module and build cache under `/` or `$GOMODCACHE`;
  `go clean -cache` reclaims it.
- **Panel assets**: the management panel is downloaded from GitHub on first access and cached in
  `/data/static`; if the host cannot reach GitHub, mount or copy `management.html` there
  manually.
- **Config must be writable**: the management API rewrites `config.yaml` in place, which is why
  it lives on the mounted `/data` directory rather than inside `/app/current`.
