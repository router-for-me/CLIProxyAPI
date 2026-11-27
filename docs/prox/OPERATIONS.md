# Operating prox

## Image

`prox-ci` builds `Dockerfile.prox` natively on amd64 and arm64 runners and
publishes a multi-arch image to GHCR on every push to `main` and on
`prox-v*` tags:

| Tag | When |
|---|---|
| `ghcr.io/kpatdev/prox:latest` | every push to `main` |
| `ghcr.io/kpatdev/prox:sha-<short>` | every published build (pin this in production) |
| `ghcr.io/kpatdev/prox:<version>` | tag `prox-v1.2.3` publishes `1.2.3` |

Pull requests run the Go and panel checks only; nothing is published from a PR.

```sh
docker run -d --name prox -p 8317:8317 \
  -e TZ=America/New_York \
  -v /srv/prox/config.yaml:/CLIProxyAPI/config.yaml \
  -v /srv/prox/auths:/CLIProxyAPI/auths \
  -v /srv/prox/prox-data:/CLIProxyAPI/prox-data \
  ghcr.io/kpatdev/prox:sha-abc1234
```

Credentials, `config.yaml`, `.env`, and `prox-data` are excluded from the
build context (`.dockerignore`) and must be mounted at runtime. The image
contains no secrets.

## Staying current with upstream

prox tracks two upstreams:

- **CLIProxyAPI** (`git remote upstream`), merged at release tags.
- **Management Center** (`web/panel`, vendored), synced with
  `scripts/prox/sync-panel.sh`; `web/panel/UPSTREAM` records the commit.

`prox-upstream-sync` runs weekly (and on demand from the Actions tab). It
merges the newest `v*` tag and the panel's `main` into a
`sync/upstream-...` branch, rebuilds the embedded panel, runs the tests, and
opens a PR. On a merge conflict it opens an issue listing the files instead.
Review and merge the PR yourself; that push to `main` publishes a new image.

By hand:

```sh
git fetch upstream --tags
git switch -c sync/upstream-v8.1.9 origin/main
git merge v8.1.9                     # conflicts only in files marked "prox:"
scripts/prox/sync-panel.sh           # panel changes, three-way
(cd web/panel && bun install && bun run build:prox)
go build ./... && go test ./internal/prox/... ./internal/api/... ./sdk/cliproxy/...
```

Use merges, not rebases: `main` is published, and merges keep each upstream
release visible in history. Upstream edits by prox are listed in
[README.md](README.md#how-it-plugs-in).

## Upstream workflows

The fork inherits CLIProxyAPI's workflows. They are meant for the upstream
repository and misbehave here, so disable them in the Actions tab (or with
`gh workflow disable`); disabling survives upstream merges and needs no file
edits:

| Workflow | Problem in prox |
|---|---|
| `auto-retarget-main-pr-to-dev` | retargets every PR to `main` onto a `dev` branch prox does not have |
| `agents-md-guard` | closes any PR that touches `AGENTS.md`, including upstream syncs |
| `translator-path-guard` | fails any PR touching `internal/translator`, including upstream syncs |
| `docker-image` | pushes to Docker Hub on `v*` tags (needs Docker Hub secrets) |
| `release` | GoReleaser on `v*` tags |
| `pr-test-build` | harmless; redundant with `prox-ci` |

New workflows that arrive with an upstream merge are enabled by default;
check `.github/` in every sync PR.
