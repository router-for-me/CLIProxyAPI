# CLI 代理 API

[English](README.md) | 中文

一个为 CLI 提供 OpenAI/Gemini/Claude/Codex 兼容 API 接口的代理服务器。

现已支持通过 OAuth 登录接入 OpenAI Codex（GPT 系列）和 Claude Code。

您可以使用本地或多账户的CLI方式，通过任何与 OpenAI（包括Responses）/Gemini/Claude 兼容的客户端和SDK进行访问。

## 赞助商

[![bigmodel.cn](https://assets.router-for.me/chinese.png)](https://www.bigmodel.cn/claude-code?ic=RRVJPB5SII)

本项目由 Z智谱 提供赞助, 他们通过 GLM CODING PLAN 对本项目提供技术支持。

GLM CODING PLAN 是专为AI编码打造的订阅套餐，每月最低仅需20元，即可在十余款主流AI编码工具如 Claude Code、Cline、Roo Code 中畅享智谱旗舰模型GLM-4.6，为开发者提供顶尖的编码体验。

智谱AI为本软件提供了特别优惠，使用以下链接购买可以享受九折优惠：https://www.bigmodel.cn/claude-code?ic=RRVJPB5SII

## 功能特性

- 为 CLI 模型提供 OpenAI/Gemini/Claude/Codex 兼容的 API 端点
- 新增 OpenAI Codex（GPT 系列）支持（OAuth 登录）
- 新增 Claude Code 支持（OAuth 登录）
- 新增 Qwen Code 支持（OAuth 登录）
- 新增 iFlow 支持（OAuth 登录）
- 支持流式与非流式响应
- 函数调用/工具支持
- 多模态输入（文本、图片）
- 多账户支持与轮询负载均衡（Gemini、OpenAI、Claude、Qwen 与 iFlow）
- 简单的 CLI 身份验证流程（Gemini、OpenAI、Claude、Qwen 与 iFlow）
- 支持 Gemini AIStudio API 密钥
- 支持 AI Studio Build 多账户轮询
- 支持 Gemini CLI 多账户轮询
- 支持 Claude Code 多账户轮询
- 支持 Qwen Code 多账户轮询
- 支持 iFlow 多账户轮询
- 支持 OpenAI Codex 多账户轮询
- 通过配置接入上游 OpenAI 兼容提供商（例如 OpenRouter）
- 可复用的 Go SDK（见 `docs/sdk-usage_CN.md`）

## 新手入门

CLIProxyAPI 用户手册： [https://help.router-for.me/](https://help.router-for.me/cn/)

## 管理 API 文档

请参见 [MANAGEMENT_API_CN.md](https://help.router-for.me/cn/management/api)

## Amp CLI 支持

CLIProxyAPI 已内置对 [Amp CLI](https://ampcode.com) 和 Amp IDE 扩展的支持，可让你使用自己的 Google/ChatGPT/Claude OAuth 订阅来配合 Amp 编码工具：

- 提供商路由别名，兼容 Amp 的 API 路径模式（`/api/provider/{provider}/v1...`）
- 管理代理，处理 OAuth 认证和账号功能
- 智能模型回退与自动路由
- 以安全为先的设计，管理端点仅限 localhost

**→ [Amp CLI 完整集成指南](docs/amp-cli-integration_CN.md)**

## SDK 文档

- 使用文档：[docs/sdk-usage_CN.md](docs/sdk-usage_CN.md)
- 高级（执行器与翻译器）：[docs/sdk-advanced_CN.md](docs/sdk-advanced_CN.md)
- 认证: [docs/sdk-access_CN.md](docs/sdk-access_CN.md)
- 凭据加载/更新: [docs/sdk-watcher_CN.md](docs/sdk-watcher_CN.md)
- 自定义 Provider 示例：`examples/custom-provider`

## 贡献

欢迎贡献！请随时提交 Pull Request。

1. Fork 仓库
2. 创建您的功能分支（`git checkout -b feature/amazing-feature`）
3. 提交您的更改（`git commit -m 'Add some amazing feature'`）
4. 推送到分支（`git push origin feature/amazing-feature`）
5. 打开 Pull Request

## 谁与我们在一起？

这些项目基于 CLIProxyAPI:

### [vibeproxy](https://github.com/automazeio/vibeproxy)

一个原生 macOS 菜单栏应用，让您可以使用 Claude Code & ChatGPT 订阅服务和 AI 编程工具，无需 API 密钥。

### [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator)

一款基于浏览器的 SRT 字幕翻译工具，可通过 CLI 代理 API 使用您的 Gemini 订阅。内置自动验证与错误修正功能，无需 API 密钥。

> [!NOTE]  
> 如果你开发了基于 CLIProxyAPI 的项目，请提交一个 PR（拉取请求）将其添加到此列表中。

## 许可证

此项目根据 MIT 许可证授权 - 有关详细信息，请参阅 [LICENSE](LICENSE) 文件。

## 写给所有中国网友的

QQ 群：188637136

或

Telegram 群：https://t.me/CLIProxyAPI
