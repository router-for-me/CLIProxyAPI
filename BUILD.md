# Cyan Fork 构建指南

## 版本信息

- **当前版本**: `1.1.0-cyan`
- **镜像名称**: `cyan-proxy:latest`
- **平台**: `linux/amd64`
- **上游仓库**: `https://github.com/router-for-me/CLIProxyAPIPlus`
- **Fork 仓库**: `https://github.com/CyanTachyon/CLIProxyAPIPlus`

## Docker 构建

### 构建镜像

```bash
docker build --platform linux/amd64 \
  --build-arg VERSION="1.1.0-cyan" \
  --build-arg COMMIT="$(git rev-parse --short HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t cyan-proxy:latest .
```

### 导出镜像为 tar.gz

```bash
docker save cyan-proxy:latest | gzip > dist/cyan-proxy-1.1.0-cyan-amd64.tar.gz
```

### 在服务器上加载镜像

```bash
docker load < cyan-proxy-1.1.0-cyan-amd64.tar.gz
```

## 本次 Fork 的修改内容

### Bug 修复：Copilot Provider 下 Claude 模型的 Thinking/reasoning_effort 被错误 strip

**问题描述**：通过 Copilot Provider 使用 Claude 模型时，请求中的 `reasoning_effort` 参数会被主动删除，导致无法开启思考模式。

**根因**：

1. `FetchGitHubCopilotModels()` 动态获取的 Claude 模型没有 `Thinking` 配置信息（`Thinking == nil`）
2. `ApplyThinking()` 在 `modelInfo.Thinking == nil` 时，会调用 `StripThinkingConfig()` 主动删除 `reasoning_effort`
3. OpenAI applier 在 `modelInfo.Thinking == nil` 时直接 passthrough，不会将 suffix 解析出的配置写入 body

**修复**（涉及 3 个文件）：

| 文件 | 修改 |
|------|------|
| `internal/runtime/executor/github_copilot_executor.go` | `FetchGitHubCopilotModels()` 中为动态获取的模型继承静态定义的 Thinking 配置 |
| `internal/thinking/apply.go` | `Thinking == nil` 时不再 strip，改为走 `applyUserDefinedModel` 路径 |
| `internal/thinking/provider/openai/apply.go` | `Thinking == nil` 时走 `applyCompatibleOpenAI` 路径，正确设置 `reasoning_effort` |

### 功能：模型名后缀语法

支持在模型名后添加括号后缀来控制 thinking 级别，例如：

- `claude-opus-4-6(high)` → 等价于 `reasoning_effort: "high"`
- `claude-opus-4-6(low)` → 等价于 `reasoning_effort: "low"`

此功能依赖上述 bug 修复才能正常工作。`ParseSuffix` 和 `normalizeModel` 本身已支持该语法。

### 版本号修改

`cmd/server/main.go` 中的 `Version` 从 `"dev"` 改为 `"1.0.0-cyan-plus2"`。

## 同步上游

```bash
git remote add upstream https://github.com/router-for-me/CLIProxyAPIPlus.git
git fetch upstream
git merge upstream/main
```
