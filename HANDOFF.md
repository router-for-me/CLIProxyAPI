# Handoff

## Goal

Deploy `CLIProxyAPI` on a UGREEN `DH4300 Plus` NAS as a Docker service, with primary upstreams:

- Codex
- Gemini

The desired deployment style is a NAS-friendly, persistent-volume-based setup instead of the upstream's generic local-machine compose defaults.

## Repository State

- Current branch: `main`
- Current HEAD: `7cc2816cdf440984890cf75fc84402d1b7a8582f`
- Current commit summary: `Add NAS deployment templates and setup guide`
- Remote main at time of handoff: `f81acd076087732a941627eda7ffae23120f10f1`
- Local branch status: `main` is ahead of `origin/main` by 1 commit
- Working tree status: clean

Important implication:

- Another machine will not see the latest NAS deployment work unless this commit is pushed or transferred manually.

## What Was Added

NAS deployment assets were added under `deploy/nas/`:

- `deploy/nas/docker-compose.yml`
- `deploy/nas/.env.example`
- `deploy/nas/config.yaml.example`
- `deploy/nas/README_CN.md`

These files are intended for UGREEN/other NAS environments.

## Deployment Decisions Already Made

1. Only the main API port `8317` is published by default.
2. OAuth callback ports are commented out by default and should only be exposed temporarily if OAuth must be completed inside the container.
3. Config, auth data, logs, and storage are persisted via mounted host paths.
4. For UGREEN `DH4300 Plus`, host paths should be real shared-folder absolute paths, not `./data/...` relative paths.
5. Recommended OAuth workflow is:
   - complete login on an interactive computer first
   - copy resulting auth files into NAS `data/auths/`
   - restart container

## Key Files

- [deploy/nas/docker-compose.yml](C:\Users\admin\Desktop\cli2api\deploy\nas\docker-compose.yml)
- [deploy/nas/.env.example](C:\Users\admin\Desktop\cli2api\deploy\nas\.env.example)
- [deploy/nas/config.yaml.example](C:\Users\admin\Desktop\cli2api\deploy\nas\config.yaml.example)
- [deploy/nas/README_CN.md](C:\Users\admin\Desktop\cli2api\deploy\nas\README_CN.md)
- [config.example.yaml](C:\Users\admin\Desktop\cli2api\config.example.yaml)

## UGREEN Notes

For `DH4300 Plus`, use a shared folder layout similar to:

```text
<shared-folder>/docker/cliproxyapi/
├─ docker-compose.yml
├─ .env
└─ data/
   ├─ config.yaml
   ├─ auths/
   ├─ logs/
   └─ store/
```

Suggested `.env` values on NAS:

```dotenv
CONFIG_FILE=/your-shared-folder/docker/cliproxyapi/data/config.yaml
AUTH_DIR=/your-shared-folder/docker/cliproxyapi/data/auths
LOG_DIR=/your-shared-folder/docker/cliproxyapi/data/logs
STORE_DIR=/your-shared-folder/docker/cliproxyapi/data/store
API_PORT=8317
TZ=Asia/Shanghai
```

## Model Routing Rules Already Clarified

In this project:

- "source" is controlled mainly by `prefix`
- "model type" is controlled by `models.name` and `models.alias`
- strict routing should use `force-model-prefix: true`

Recommended config pattern for Codex + Gemini:

```yaml
force-model-prefix: true

routing:
  strategy: "fill-first"

codex-api-key:
  - api-key: "YOUR_CODEX_KEY"
    prefix: "codex"
    models:
      - name: "gpt-5"
        alias: "gpt5"
      - name: "gpt-5-mini"
        alias: "gpt5-mini"

gemini-api-key:
  - api-key: "YOUR_GEMINI_KEY"
    prefix: "gemini"
    models:
      - name: "gemini-2.5-pro"
        alias: "g2p"
      - name: "gemini-2.5-flash"
        alias: "g2f"
```

Then requests should target models like:

- `codex/gpt5`
- `gemini/g2p`

## Remaining Work

1. Produce or update a concrete `data/config.yaml` for the user's actual Codex and Gemini setup.
2. If needed, tailor the config for:
   - API-key-based upstreams
   - OAuth/file-backed upstreams
3. Provide a final "copy into UGREEN container UI" set of values if the user wants a GUI-only deployment path.
4. If the user asks, provide validation commands for:
   - `docker compose config`
   - `docker compose up -d`
   - `curl http://NAS_IP:8317/v1/models -H "Authorization: Bearer ..."`

## Constraints

- Do not assume relative mount paths are valid on the NAS UI.
- Do not expose OAuth callback ports by default.
- Prefer small, reviewable deployment changes; avoid unrelated refactors.
- No secrets should be written into repository files.

## Suggested Prompt For The Next Session

Use this prompt in the next Codex session:

```text
继续这个仓库的交接工作。先读取 HANDOFF.md，然后基于当前代码继续帮我完成绿联 DH4300 Plus 上的 CLIProxyAPI 部署配置。重点是 Codex 和 Gemini 的 data/config.yaml 落地方案，以及如何在 NAS 上验证模型路由是否正确。
```
