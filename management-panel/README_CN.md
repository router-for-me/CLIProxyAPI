# CLI Proxy API 管理中心

用于管理与排障 **CLI Proxy API** 的单文件 WebUI（React + TypeScript），通过 **Management API** 完成配置、凭据、日志与统计等运维工作。

[English](README.md)

**主项目**: https://github.com/router-for-me/CLIProxyAPI  
**示例地址**: https://remote.router-for.me/  
**最低版本要求**: ≥ 6.3.0（推荐 ≥ 6.5.0）

Since version 6.0.19, the WebUI ships with the main program; access it via `/management.html` on the API port once the service is running.

## 这是什么（以及不是什么）

- 本仓库只包含 Web 管理界面本身，通过 CLI Proxy API 的 **Management API**（`/v0/management`）读取/修改配置、上传凭据、查看日志与使用统计。
- 它 **不是** 代理本体，不参与流量转发。

## 快速开始

### 方式 A：使用 CLIProxyAPI 自带的 WebUI（推荐）

1. 启动 CLI Proxy API 服务。
2. 打开：`http://<host>:<api_port>/management.html`
3. 输入 **管理密钥** 并连接。

页面会根据当前地址自动推断 API 地址，也支持手动修改。

### 方式 B：开发调试

```bash
npm install
npm run dev
```

打开 `http://localhost:5173`，然后连接到你的 CLI Proxy API 实例。

### 方式 C：构建单文件 HTML

```bash
npm install
npm run build
```

- 构建产物：`dist/index.html`（资源已全部内联）。
- 在 CLIProxyAPI 的发布流程里会重命名为 `management.html`。
- 本地预览：`npm run preview`

提示：直接用 `file://` 打开 `dist/index.html` 可能遇到浏览器 CORS 限制；更稳妥的方式是用预览/静态服务器打开。

## 连接说明

### API 地址怎么填

以下格式均可，WebUI 会自动归一化：

- `localhost:8317`
- `http://192.168.1.10:8317`
- `https://example.com:8317`
- `http://example.com:8317/v0/management`（也可填写，后缀会被自动去除）

### 管理密钥（注意：不是 API Keys）

管理密钥会以如下方式随请求发送：

- `Authorization: Bearer <MANAGEMENT_KEY>`（默认）

这与 WebUI 中“API Keys”页面管理的 `api-keys` 不同：后者是代理对外接口（如 OpenAI 兼容接口）给客户端使用的鉴权 key。

### 远程管理

当你从非 localhost 的浏览器访问时，服务端通常需要开启远程管理（例如 `allow-remote-management: true`）。  
完整鉴权规则、限制与边界情况请查看 `api.md`。

## 功能一览（按页面对应）

- **仪表盘**：连接状态、服务版本/构建时间、关键数量概览、可用模型概览。
- **基础设置**：调试开关、代理 URL、请求重试、配额回退（切项目/切预览模型）、使用统计、请求日志、文件日志、WebSocket 鉴权。
- **API Keys**：管理代理 `api-keys`（增/改/删）。
- **AI 提供商**：
  - Gemini/Codex/Claude 配置（Base URL、Headers、代理、模型别名、排除模型、Prefix）。
  - OpenAI 兼容提供商（多 Key、Header、自助从 `/v1/models` 拉取并导入模型别名、可选浏览器侧 `chat/completions` 测试）。
  - Ampcode 集成（上游地址/密钥、强制映射、模型映射表）。
- **认证文件**：上传/下载/删除 JSON 凭据，筛选/搜索/分页，标记 runtime-only；查看单个凭据可用模型（依赖后端支持）；管理 OAuth 排除模型（支持 `*` 通配符）。
- **OAuth**：对支持的提供商发起 OAuth/设备码流程，轮询状态；可选提交回调 `redirect_url`；包含 iFlow Cookie 导入。
- **使用统计**：按小时/天图表、按 API 与按模型统计、缓存/推理 Token 拆分、RPM/TPM 时间窗、可选本地保存的模型价格用于费用估算。
- **配置文件**：浏览器内编辑 `/config.yaml`（YAML 高亮 + 搜索），保存/重载。
- **日志**：增量拉取日志、自动刷新、搜索、隐藏管理端流量、清空日志；下载请求错误日志文件。
- **系统信息**：快捷链接 + 拉取 `/v1/models` 并分组展示（需要至少一个代理 API Key 才能查询模型）。

## 构建与发布说明

- 使用 Vite 输出 **单文件 HTML**（`dist/index.html`），资源全部内联（`vite-plugin-singlefile`）。
- 打 `vX.Y.Z` 标签会触发 `.github/workflows/release.yml`，发布 `dist/management.html`。
- 页脚显示的 UI 版本在构建期注入（优先使用环境变量 `VERSION`，否则使用 git tag / `package.json`）。

## 安全提示

- 管理密钥会存入浏览器 `localStorage`，并使用轻量混淆格式（`enc::v1::...`）避免明文；仍应视为敏感信息。
- 建议使用独立浏览器配置/设备进行管理；开启远程管理时请谨慎评估暴露面。

## 常见问题

- **无法连接 / 401**：确认 API 地址与管理密钥；远程访问可能需要服务端开启远程管理。
- **反复输错密钥**：服务端可能对远程 IP 进行临时封禁。
- **日志页面不显示**：需要在“基础设置”里开启“写入日志文件”，导航项才会出现。
- **功能提示不支持**：多为后端版本较旧或接口未启用/不存在（如：认证文件模型列表、排除模型、日志相关接口）。
- **OpenAI 提供商测试失败**：测试在浏览器侧执行，会受网络与 CORS 影响；这里失败不一定代表服务端不可用。

## 开发命令

```bash
npm run dev        # 启动开发服务器
npm run build      # tsc + Vite 构建
npm run preview    # 本地预览 dist
npm run lint       # ESLint（warnings 视为失败）
npm run format     # Prettier
npm run type-check # tsc --noEmit
```

## 贡献

欢迎提 Issue 与 PR。建议附上：

- 复现步骤（服务端版本 + UI 版本）
- UI 改动截图
- 验证记录（`npm run lint`、`npm run type-check`）

## 许可证

MIT
