# CLIProxyAPI Development Guidelines

## 分支与合并策略
- **禁止直接 push 到 main 分支**
- 所有变更必须走 feature branch → PR → merge 流程
- 分支命名：`feat/<feature>`、`fix/<issue>`、`chore/<task>`
- PR merge 后删除远程 feature branch

## CI/CD 流程

### PR 阶段（verify）
- `pr-test-build.yml`：PR 触发 go build 验证
- `pr-path-guard.yml`：禁止 PR 修改 `internal/translator/`

### Release 阶段（tag push）
- 在 main 分支上打 `v*` tag 触发 `docker-image.yml`
- 构建多架构 Docker 镜像（amd64 + arm64）并推送到 DockerHub + GHCR
- 三个 job：`docker_amd64` → `docker_arm64` → `docker_manifest`

### Docker 镜像
- DockerHub：`chaosaiglobal/cli-proxy-api`
- GHCR：`ghcr.io/chaosaiglobal/cli-proxy-api`
- 需要 repo secrets：`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`

### 发布操作
```bash
# 1. 开发分支 → PR → merge 到 main
git checkout -b feat/xxx main
# ... 开发 ...
git push -u origin feat/xxx
gh pr create

# 2. PR merge 后，在 main 上打 tag
git checkout main && git pull
git tag -a v1.0.0 -m "release notes"
git push origin v1.0.0
```

## 构建

```bash
# 本地构建
go build -o cli-proxy-api ./cmd/server/

# Docker 构建
docker compose build

# 需要先刷新 models catalog
git fetch --depth 1 https://github.com/router-for-me/models.git main
git show FETCH_HEAD:models.json > internal/registry/models/models.json
```

## 部署
- 生产部署配置在 `deploy/` 目录
- 部署到 `cli.llmgate.io`，运维文档在 `/home/debian/projects/operation/services/cli-proxy-api/README.md`

## 代码约束
- `internal/translator/` 目录不接受外部 PR，需通过维护团队修改
- Go 版本：1.26.0（见 go.mod）
- 配置文件 `config.yaml` 在 .gitignore 中，不要提交实际配置
