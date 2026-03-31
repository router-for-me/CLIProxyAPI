# CLIProxyAPI Development Guidelines

## 分支与合并策略
- **禁止直接 push 到 main 分支**
- 所有变更必须走 feature branch → PR → merge 流程
- 分支命名：`feat/<feature>`、`fix/<issue>`、`chore/<task>`
- PR merge 后删除远程 feature branch

## CI/CD 流程

### PR 阶段
- `pr-test-build.yml`：PR 触发构建验证（go build）
- `pr-path-guard.yml`：禁止 PR 修改 `internal/translator/`
- `security-scan.yml`：push/PR/每周一执行 govulncheck

### Release 阶段
- tag push (`v*`) 触发 release 流程
- `release.yaml`：GoReleaser 发布二进制 + Docker 镜像（DockerHub + GHCR）
- `docker-image.yml`：多架构 Docker 镜像构建（amd64 + arm64）
- 支持 Simple Release（仅 x86_64 GHCR），通过 repo variable `SIMPLE_RELEASE=true` 或手动 dispatch

### Docker 镜像
- DockerHub：`chaosaiglobal/cli-proxy-api`（需配置 `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN` secrets）
- GHCR：`ghcr.io/chaosaiglobal/cli-proxy-api`
- GoReleaser 使用 `Dockerfile.goreleaser`（轻量运行时镜像）
- 本地构建使用 `Dockerfile`（多阶段编译）

## 构建

```bash
# 本地构建
go build -o cli-proxy-api ./cmd/server/

# Docker 构建
docker compose -f deploy/docker-compose.yml build

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
