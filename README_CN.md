# CLI Proxy API

[English](README.md) | 简体中文 | [日本語](README_JA.md)

CLIProxyAPI 是一个可自托管的代理服务，可将 CLI 订阅、OAuth 账号、API Key 和兼容上游统一转换为 OpenAI、Anthropic 与 Gemini 兼容接口。项目支持多凭证轮询、流式响应、工具调用、多模态输入、网页管理和有容量上限的用量统计。

## 主要功能

- OpenAI 兼容的 `/v1/models`、Chat Completions、Responses、图像、视频和 Realtime 路由
- Anthropic 兼容的 `/v1/messages` 与 Token 计数路由
- Gemini 兼容的 `/v1beta/models`、`generateContent` 与 Interactions 路由
- Codex、Claude、Antigravity、Kimi 和 xAI OAuth 登录
- Vertex 服务账号导入，以及可配置的 OpenAI 兼容上游
- 多账号轮询、失败重试和冷却调度
- 在上游支持时提供流式、非流式、WebSocket、工具调用和文本/图片输入
- 从磁盘加载的管理网页，修改 HTML、CSS、JavaScript 后不需要重新编译 EXE
- 客户端 Key 别名、启停、详细用量、导出和费用估算
- 可嵌入的 Go SDK 与可选动态插件

## 支持的凭证

| 提供商或来源 | 凭证方式 | 配置入口 |
|---|---|---|
| OpenAI Codex | OAuth 或设备码 | `--codex-login` / `--codex-device-login` |
| Anthropic Claude | OAuth | `--claude-login` |
| Google Antigravity | OAuth | `--antigravity-login` |
| Kimi | OAuth 设备码 | `--kimi-login` |
| xAI / Grok | OAuth 设备码 | `--xai-login` |
| Google Vertex AI | 服务账号 JSON | `--vertex-import FILE` |
| Gemini API Key、兼容上游 | YAML 配置 | `config.yaml` |

OAuth 凭证保存在 `auth-dir` 中，默认目录是当前运行用户的 `~/.cli-proxy-api`。

## 启动前准备

任选一种方式：

- 从 [GitHub Releases](https://github.com/router-for-me/CLIProxyAPI/releases) 下载对应平台压缩包；
- 安装 Go 1.26 或更高版本后从源码编译；
- 安装 Docker Engine 与 Docker Compose v2。

Release 文件名：

| 平台 | 文件 |
|---|---|
| Windows x64 | `CLIProxyAPI_<version>_windows_amd64.zip` |
| Windows ARM64 | `CLIProxyAPI_<version>_windows_aarch64.zip` |
| macOS Intel | `CLIProxyAPI_<version>_darwin_amd64.tar.gz` |
| macOS Apple 芯片 | `CLIProxyAPI_<version>_darwin_aarch64.tar.gz` |
| Linux x64 | `CLIProxyAPI_<version>_linux_amd64.tar.gz` |
| Linux ARM64 | `CLIProxyAPI_<version>_linux_aarch64.tar.gz` |
| musl Linux / OpenWrt | 在 `.tar.gz` 前增加 `_no-plugin` |
| FreeBSD ARM64 | `CLIProxyAPI_<version>_freebsd_aarch64_no-plugin.tar.gz` |

普通 Linux 包支持动态插件，要求 GLIBC 2.17 或更高版本。`_no-plugin` 是不支持动态插件的便携静态版本。

## 第一次配置

启动前先复制示例配置：

```powershell
# Windows PowerShell
Copy-Item .\config.example.yaml .\config.yaml
```

```bash
# Linux 与 macOS
cp config.example.yaml config.yaml
```

至少要替换示例客户端 Key。保留 `your-api-key-1` 之类的模板值会触发安全模式，使代理 API 返回 HTTP 403。

```yaml
host: "127.0.0.1"
port: 8317

remote-management:
  allow-remote: false
  secret-key: "替换为高强度管理密码"
  web-directory: "web/management"

api-keys:
  - "替换为客户端调用Key"

usage-statistics-enabled: true
```

仅在本机使用时建议设置 `host: "127.0.0.1"`。示例配置中的 `host: ""` 会监听全部 IPv4 和 IPv6 网卡。

## Windows 启动方式

解压 Windows 压缩包，在该目录打开 PowerShell，创建并修改 `config.yaml` 后运行：

```powershell
.\cli-proxy-api.exe --config .\config.yaml
```

按 `Ctrl+C` 停止。程序是控制台应用，不会自动安装为 Windows 服务；如需开机自启，可使用任务计划程序、NSSM 或其他进程守护工具。

## Linux 启动方式

```bash
mkdir -p cliproxyapi
tar -xzf CLIProxyAPI_<version>_linux_amd64.tar.gz -C cliproxyapi
cd cliproxyapi
cp config.example.yaml config.yaml
chmod +x ./cli-proxy-api
./cli-proxy-api --config ./config.yaml
```

ARM 设备请选择 ARM64 压缩包。musl 发行版、OpenWrt 或无法运行 GLIBC 版本的旧系统请选择 `_no-plugin` 压缩包。

需要长期运行时，可将同一条启动命令交给 systemd、OpenRC、supervisord 或现有进程守护器。仓库当前不包含现成的 service unit。

## macOS 启动方式

```bash
mkdir -p cliproxyapi
tar -xzf CLIProxyAPI_<version>_darwin_aarch64.tar.gz -C cliproxyapi
cd cliproxyapi
cp config.example.yaml config.yaml
chmod +x ./cli-proxy-api
./cli-proxy-api --config ./config.yaml
```

Intel Mac 使用 `darwin_amd64`，Apple 芯片使用 `darwin_aarch64`。当前发布文件没有经过 Apple 公证；如果 Gatekeeper 拦截，请先核对下载来源和 SHA-256，再在系统设置中允许该程序。

## Docker 启动方式

在源码目录中先创建 `config.yaml`，再启动 Compose。否则缺失的绑定挂载源可能被 Docker 创建成目录。

```bash
cp config.example.yaml config.yaml
docker compose up -d --remove-orphans --no-build
docker compose logs -f cli-proxy-api
```

Windows PowerShell 请将 `cp` 换成 `Copy-Item`。

常用命令：

```bash
docker compose restart cli-proxy-api
docker compose down
```

如需从当前源码构建镜像，Linux/macOS 运行 `./docker-build.sh`，Windows PowerShell 运行 `.\docker-build.ps1`，然后选择 2。脚本会避免 Compose 的拉取策略覆盖本地镜像。

Compose 默认持久化：

- `config.yaml` → `/CLIProxyAPI/config.yaml`
- `auths/` → `/root/.cli-proxy-api`
- `logs/` → `/CLIProxyAPI/logs`
- `plugins/` → `/CLIProxyAPI/plugins`

Docker 仅允许本机访问时，可把端口映射从 `8317:8317` 改为 `127.0.0.1:8317:8317`。

宿主机 `.env` 会被 Compose 用于 `${...}` 变量替换，但当前 `env_file` 仍为注释状态，因此 `.env` 不会自动传入容器。使用 `PGSTORE_*`、`GITSTORE_*`、`OBJECTSTORE_*` 或 `MANAGEMENT_PASSWORD` 时，需要启用 `env_file` 或显式添加环境变量。

## 从源码编译

Linux 与 macOS：

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI
cp config.example.yaml config.yaml
go build -o cli-proxy-api ./cmd/server
./cli-proxy-api --config ./config.yaml
```

Windows PowerShell：

```powershell
git clone https://github.com/router-for-me/CLIProxyAPI.git
Set-Location CLIProxyAPI
Copy-Item .\config.example.yaml .\config.yaml
go build -o cli-proxy-api.exe ./cmd/server
.\cli-proxy-api.exe --config .\config.yaml
```

普通源码构建会使用 CGO 以支持动态插件，因此需要本机 C 工具链。如果不需要动态插件，可设置 `CGO_ENABLED=0` 构建便携版本。

## 登录上游账号

每次只执行一个登录命令，并使用与服务相同的配置文件：

```bash
./cli-proxy-api --config ./config.yaml --codex-login
./cli-proxy-api --config ./config.yaml --codex-device-login
./cli-proxy-api --config ./config.yaml --claude-login
./cli-proxy-api --config ./config.yaml --antigravity-login
./cli-proxy-api --config ./config.yaml --kimi-login
./cli-proxy-api --config ./config.yaml --xai-login
```

登录完成后程序会退出，再单独执行正常启动命令。无桌面环境时增加 `--no-browser`；需要浏览器回调的流程还可指定 `--oauth-callback-port PORT`。导入 Vertex 凭证：

```bash
./cli-proxy-api --config ./config.yaml --vertex-import ./service-account.json
```

如果服务已经在 Docker 中运行，可使用：

```bash
docker compose exec cli-proxy-api ./CLIProxyAPI \
  --config /CLIProxyAPI/config.yaml \
  --codex-login --no-browser
```

按需替换登录参数。登录结果会写入已经挂载的 `auths/`。如果交互登录与后台服务使用不同的系统用户，请在配置中为 `auth-dir` 使用绝对路径，避免两者读取不同的凭证目录。

## 检查并调用 API

健康检查不需要认证：

```bash
curl http://127.0.0.1:8317/healthz
```

使用 `api-keys` 中的客户端 Key 查询模型：

```bash
curl http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_API_KEY"
```

使用模型列表实际返回的模型 ID 发起请求：

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"MODEL_ID","messages":[{"role":"user","content":"你好"}]}'
```

常用地址：

| 客户端协议 | Base URL 或路由 |
|---|---|
| OpenAI 兼容 SDK | `http://127.0.0.1:8317/v1` |
| Anthropic 客户端 Base URL | `http://127.0.0.1:8317`，客户端请求 `/v1/messages` |
| Gemini 客户端 Base URL | `http://127.0.0.1:8317`，API 版本路径为 `/v1beta` |
| Codex `chatgpt_base_url` | `http://127.0.0.1:8317/backend-api/codex` |

OpenAI 兼容客户端可设置：

```powershell
# Windows PowerShell
$env:OPENAI_BASE_URL = "http://127.0.0.1:8317/v1"
$env:OPENAI_API_KEY = "YOUR_CLIENT_API_KEY"
```

```bash
# Linux 与 macOS
export OPENAI_BASE_URL="http://127.0.0.1:8317/v1"
export OPENAI_API_KEY="YOUR_CLIENT_API_KEY"
```

建议使用请求头传递 Key，不要放在 URL 查询参数中，避免凭证进入 URL 或代理日志。

## 网页管理

访问 [http://127.0.0.1:8317/management.html](http://127.0.0.1:8317/management.html)。只有设置了 `remote-management.secret-key` 或环境变量 `MANAGEMENT_PASSWORD`，管理 API 才会启用；两者都为空时 `/v0/management/*` 返回 404。`allow-remote: false` 会将管理功能限制在本机。

源码目录中的新版管理前端位于 `web/management`。服务会在每次请求时从磁盘读取文件，因此修改前端后刷新浏览器即可，不需要重新编译 EXE。相对 `web-directory` 以当前配置文件所在目录为基准。

当前 Release 压缩包和 Docker 镜像没有包含 `web/management`。目录缺失时，`/management.html` 会回退到经典单文件管理页。Release 二进制如需使用源码中的新版页面，请将 `web/management` 复制到 `config.yaml` 同级目录；Docker 还需要增加挂载：

```yaml
volumes:
  - ./web/management:/CLIProxyAPI/web/management:ro
```

经典管理页也可以通过 `/management/` 或 `/management/legacy` 访问。

## 使用量统计

开启 `usage-statistics-enabled`，用 `api-key-metadata` 为 Key 设置稳定 ID、别名和启停状态，再按需配置 `usage-pricing` 估算价格。网页端提供每个 Key 的请求、输入/缓存/输出 Token、筛选、JSON/CSV 导出和启停管理。

两组指标含义不同：

- `attempts`、`success`、`failed`：每个外部客户端请求只记录一次最终结果，适合用户统计和费用估算。
- `upstream_attempts`、`upstream_failed_attempts`：每次凭证、模型或提供商尝试都会记录，适合运维和重试分析。

快照保留天数由 `usage-statistics-retention-days` 控制。快照不会保存原始客户端 Key 或请求正文。费用只是估算，不等同于支付、余额、发票或权威结算。指标拆分前的旧快照无法重新推导历史重试关系。

## 终端管理界面

```bash
./cli-proxy-api --config ./config.yaml --tui
```

增加 `--standalone` 可在 TUI 模式中同时启动内嵌本地服务。

## SDK 与进阶文档

- [SDK 使用](docs/sdk-usage_CN.md)
- [执行器与转换器](docs/sdk-advanced_CN.md)
- [认证与访问](docs/sdk-access_CN.md)
- [凭证加载与监听](docs/sdk-watcher_CN.md)
- [自定义 Provider 示例](examples/custom-provider)
- [插件示例](examples/plugin/README_CN.md)
- [管理 API 参考](https://help.router-for.me/management/api)

## 安全建议

- 对外提供服务前，替换 `api-keys` 中的全部模板值。
- 只在本机使用时设置 `host: "127.0.0.1"`。
- 除非确实需要远程管理，否则保持 `remote-management.allow-remote: false`。
- 使用高强度管理密码；通过不可信网络访问时必须配置 HTTPS。
- 不要提交 `config.yaml`、`.env`、`auths/`、用量快照或提供商凭证。
- 远程登录账号时，限制 OAuth 回调端口的访问范围。

## 常见问题

| 现象 | 检查项 |
|---|---|
| 代理接口返回 403 | 替换 `api-keys` 中 `your-api-key-1` 等模板值 |
| 管理接口返回 404 | 设置 `remote-management.secret-key` 或 `MANAGEMENT_PASSWORD` |
| 没有显示新版管理页 | 确认 `web/management/index.html` 相对 `config.yaml` 存在，然后强制刷新浏览器 |
| OAuth 在错误的设备打开 | 增加 `--no-browser`，按终端输出完成网页或设备码流程 |
| Linux 二进制无法启动 | 改用 `_no-plugin`，或安装兼容的 GLIBC 运行时 |
| Docker 没有读取存储环境变量 | 启用 `env_file` 或显式将变量传入容器 |

## 贡献

1. Fork 仓库并创建功能分支。
2. 完成修改和必要测试。
3. Go 代码需运行 `gofmt -w .`、`go test ./...` 和一次干净构建。
4. 提交并推送分支。
5. 创建 Pull Request，并写明修改内容和验证结果。

## 许可证

本项目采用 [MIT License](LICENSE)。
