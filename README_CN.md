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

### 配置

编辑 `config.yaml` 配置 OAuth 账户、API 密钥、端口等。

详细配置说明请参考 [用户手册](https://help.router-for.me/cn/)

### Docker 运行

```bash
docker run -v ./config.yaml:/app/config.yaml -p 8080:8080 ghcr.io/router-for-me/cliproxyapi:latest
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
