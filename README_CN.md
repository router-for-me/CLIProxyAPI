# CLI 代理 API

[English](README.md) | 中文 | [日本語](README_JA.md)

一个为 CLI 提供 OpenAI/Gemini/Claude/Codex 兼容 API 接口的代理服务器，支持 OAuth 认证和多种 AI 提供商。

## 功能特性

### 核心功能
- 为 CLI 模型提供 OpenAI/Gemini/Claude/Codex 兼容的 API 端点
- 支持流式与非流式响应
- 函数调用/工具支持
- 多模态输入（文本、图片）

### OAuth 认证
- OpenAI Codex 支持（OAuth 登录）
- Claude Code 支持（OAuth 登录）
- Qwen Code 支持（OAuth 登录）
- iFlow 支持（OAuth 登录）

### 多账户管理
- 多账户支持与轮询负载均衡
- Gemini 多账户（AI Studio Build、Gemini CLI）
- OpenAI Codex 多账户
- Claude Code 多账户
- Qwen Code 多账户
- iFlow 多账户

### 高级功能
- 通过配置接入上游 OpenAI 兼容提供商（如 OpenRouter）
- 模型别名与智能路由
- 熔断器支持（针对 OpenAI 兼容提供商）
- Responses API 能力自动检测（原生 /responses vs chat 回退模式）
- previous_response_id 支持（chat 回退模式下增量工具调用）
- 加权提供商轮询，实现更公平的调度
- 加权提供商轮询，实现更公平的调度
- Anthropic 兼容 API 密钥认证
- 请求级 404 错误处理优化

### 集成支持
- Amp CLI 和 IDE 扩展完整支持
- 提供商路由别名（`/api/provider/{provider}/v1...`）
- 管理代理，处理 OAuth 认证和账号功能
- 智能模型回退与自动路由

## 快速开始

### 安装

```bash
# 克隆仓库
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI

# 复制配置
cp config.example.yaml config.yaml

# 运行
./cli-proxy-new
```

### 配置文件说明

项目使用**两类配置文件**：业务配置使用 YAML，运行时状态持久化使用独立 INI。

| 文件 | 用途 | `auth-dir` |
|------|------|------------|
| `config.yaml` | 本地开发 | `./auths`（本地相对路径） |
| `config-277.yaml` | 生产部署（227 服务器） | `/opt/cliproxy/auths` |

运行时状态持久化配置：

| 文件 | 用途 | MongoDB 地址 |
|------|------|--------------|
| `state-store.local.ini` | 本地开发（不入库） | `192.168.15.227` |
| `state-store.277.ini` | 生产部署（不入库） | `127.0.0.1` |

- 仓库仅提供 `state-store.example.ini` 模板，真实状态配置文件需要在本地和生产环境分别维护。
- 解析规则：`config.yaml` 读取 `state-store.local.ini`，`config-277.yaml` 读取 `state-store.277.ini`。
- 该 Mongo 配置同时用于熔断强一致失败状态：`circuit_breaker_failure_states` 和 `circuit_breaker_failure_events`。

- **本地开发（推荐）**：`./bin/air`（由 `.air.toml` 管理，等价于使用 `-config config.yaml` 启动）
- **本地回退启动**：`go run ./cmd/server`
- **生产部署**：`./cli-proxy-new -config config-277.yaml`

详细配置说明请参考 [用户手册](https://help.router-for.me/cn/)

### Docker 运行

```bash
docker run -v ./config-277.yaml:/app/config-277.yaml -v ./state-store.277.ini:/app/state-store.277.ini -p 8080:8080 ghcr.io/router-for-me/cliproxyapi:latest ./CLIProxyAPI -config /app/config-277.yaml
```

## 项目结构

```
cmd/               # 入口点
internal/          # 核心业务代码
  api/             # HTTP API 服务
  runtime/         # 运行时和执行器
  translator/      # 协议转换
  auth/            # 认证模块
sdk/               # 可复用 SDK
test/              # 集成测试
docs/              # 文档
examples/          # 示例代码
```

## 开发指南

详见 [AGENTS.md](AGENTS.md) - 包含构建测试命令和代码风格指南。

### 热启动（Air）

安装 Air（首次执行）：

```bash
GOBIN="$(pwd)/bin" go install github.com/air-verse/air@latest
```

本地开发热启动：

```bash
../bin/air
```

行为约定：
- Go 源码变更（`*.go`、`go.mod`、`go.sum`）：Air 自动重建并重启服务。
- `config.yaml` 和 `auths/*` 变更：Air 不重启，依赖进程内 watcher 热加载。
- 生产流程不变：继续构建二进制并指定生产配置运行。

常见问题：
- `./bin/air: no such file or directory`：确认在仓库根目录执行安装命令。
- 改代码后未自动重启：确认在仓库根目录执行 `./bin/air`，并成功加载 `.air.toml`。
- 改配置触发整进程重启：检查 `.air.toml` 中是否正确排除了 YAML 与 `auths` 路径。

### 构建

```bash
go build -o cli-proxy-new ./cmd/server
```

### 测试

```bash
# 运行所有测试
go test ./...

# 运行单个测试
go test -v -run TestFunctionName ./package/
```

## SDK 文档

- 使用文档：[docs/sdk-usage_CN.md](docs/sdk-usage_CN.md)
- 高级（执行器与翻译器）：[docs/sdk-advanced_CN.md](docs/sdk-advanced_CN.md)
- 认证：[docs/sdk-access_CN.md](docs/sdk-access_CN.md)
- 凭据加载/更新：[docs/sdk-watcher_CN.md](docs/sdk-watcher_CN.md)
- 渠道接入与协议转换专题：[docs/technical/provider-client-routing-and-translation.md](docs/technical/provider-client-routing-and-translation.md)
- 227 发布手册：[docs/technical/deploy-ssh-227.md](docs/technical/deploy-ssh-227.md)
- 发布指导文档：[发布指导文档.md](发布指导文档.md)

## 贡献

欢迎贡献！请随时提交 Pull Request。

1. Fork 仓库
2. 创建功能分支（`git checkout -b feature/amazing-feature`）
3. 提交更改（`git commit -m 'Add some amazing feature'`）
4. 推送到分支（`git push origin feature/amazing-feature`）
5. 打开 Pull Request

## 基于本项目的第三方项目

- [vibeproxy](https://github.com/automazeio/vibeproxy) - macOS 菜单栏应用
- [CCS](https://github.com/kaitranntt/ccs) - Claude 账户切换 CLI 工具
- [Quotio](https://github.com/nguyenphutrong/quotio) - macOS 菜单栏统一管理应用
- [CodMate](https://github.com/loocor/CodMate) - macOS SwiftUI 管理应用
- [ProxyPilot](https://github.com/Finesssee/ProxyPilot) - Windows CLI 版本
- [霖君](https://github.com/wangdabaoqq/LinJun) - 跨平台桌面应用
- [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard) - Web 管理面板

> 如果你开发了基于 CLIProxyAPI 的项目，请提交 PR 将其添加到此列表。

## 许可证

MIT 许可证 - 详见 [LICENSE](LICENSE) 文件。
