# 写给所有中国网友的

对于项目前期的确有很多用户使用上遇到各种各样的奇怪问题，大部分是因为配置或我说明文档不全导致的。

对说明文档我已经尽可能的修补，有些重要的地方我甚至已经写到了打包的配置文件里。

已经写在 README 中的功能，都是**可用**的，经过**验证**的，并且我自己**每天**都在使用的。

可能在某些场景中使用上效果并不是很出色，但那基本上是模型和工具的原因，比如用 Claude Code 的时候，有的模型就无法正确使用工具，比如 Gemini，就在 Claude Code 和 Codex 的下使用的相当扭捏，有时能完成大部分工作，但有时候却只说不做。

目前来说 Claude 和 GPT-5 是目前使用各种第三方CLI工具运用的最好的模型，我自己也是多个账号做均衡负载使用。

实事求是的说，最初的几个版本我根本就没有中文文档，我至今所有文档也都是使用英文更新让后让 Gemini 翻译成中文的。但是无论如何都不会出现中文文档无法理解的问题。因为所有的中英文文档我都是再三校对，并且发现未及时更改的更新的地方都快速更新掉了。

最后，烦请在发 Issue 之前请认真阅读这篇文档。

另外中文需要交流的用户可以加 QQ 群：188637136

或 Telegram 群：https://t.me/CLIProxyAPI

# CLI 代理 API

[English](README.md) | 中文

一个为 CLI 提供 OpenAI/Gemini/Claude/Codex 兼容 API 接口的代理服务器。

现已支持通过 OAuth 登录接入 OpenAI Codex（GPT 系列）和 Claude Code。

您可以使用本地或多账户的CLI方式，通过任何与 OpenAI（包括Responses）/Gemini/Claude 兼容的客户端和SDK进行访问。

现已新增国内提供商：[Qwen Code](https://github.com/QwenLM/qwen-code)、[iFlow](https://iflow.cn/)。

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
- 支持 Gemini CLI 多账户轮询
- 支持 Claude Code 多账户轮询
- 支持 Qwen Code 多账户轮询
- 支持 iFlow 多账户轮询
- 支持 OpenAI Codex 多账户轮询
- 通过配置接入上游 OpenAI 兼容提供商（例如 OpenRouter）
- 可复用的 Go SDK（见 `docs/sdk-usage_CN.md`）

## 安装

### 前置要求

- Go 1.24 或更高版本
- 有权访问 Gemini CLI 模型的 Google 账户（可选）
- 有权访问 OpenAI Codex/GPT 的 OpenAI 账户（可选）
- 有权访问 Claude Code 的 Anthropic 账户（可选）
- 有权访问 Qwen Code 的 Qwen Chat 账户（可选）
- 有权访问 iFlow 的 iFlow 账户（可选）

### 从源码构建

1. 克隆仓库：
   ```bash
   git clone https://github.com/luispater/CLIProxyAPI.git
   cd CLIProxyAPI
   ```

2. 构建应用程序：
   ```bash
   go build -o cli-proxy-api ./cmd/server
   ```

### 通过 Homebrew 安装

```bash
brew install cliproxyapi
brew services start cliproxyapi
```

## 使用方法

### 图形客户端与官方 WebUI

#### [EasyCLI](https://github.com/router-for-me/EasyCLI)

CLIProxyAPI 的跨平台桌面图形客户端。

#### [Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center)

CLIProxyAPI 的基于 Web 的管理中心。

如果希望自行托管管理页面，可在配置中将 `remote-management.disable-control-panel` 设为 `true`，服务器将停止下载 `management.html`，并让 `/management.html` 返回 404。

可以通过设置环境变量 `MANAGEMENT_STATIC_PATH` 来指定 `management.html` 的存储目录。

### 身份验证

您可以分别为 Gemini、OpenAI、Claude、Qwen 和 iFlow 进行身份验证，它们可同时存在于同一个 `auth-dir` 中并参与负载均衡。

- Gemini（Google）：
  ```bash
  ./cli-proxy-api --login
  ```
  如果您是现有的 Gemini Code 用户，可能需要指定一个项目ID：
  ```bash
  ./cli-proxy-api --login --project_id <your_project_id>
  ```
  本地 OAuth 回调端口为 `8085`。

  选项：加上 `--no-browser` 可打印登录地址而不自动打开浏览器。本地 OAuth 回调端口为 `8085`。

- OpenAI（Codex/GPT，OAuth）：
  ```bash
  ./cli-proxy-api --codex-login
  ```
  选项：加上 `--no-browser` 可打印登录地址而不自动打开浏览器。本地 OAuth 回调端口为 `1455`。

- Claude（Anthropic，OAuth）：
  ```bash
  ./cli-proxy-api --claude-login
  ```
  选项：加上 `--no-browser` 可打印登录地址而不自动打开浏览器。本地 OAuth 回调端口为 `54545`。

- Qwen（Qwen Chat，OAuth）：
  ```bash
  ./cli-proxy-api --qwen-login
  ```
  选项：加上 `--no-browser` 可打印登录地址而不自动打开浏览器。使用 Qwen Chat 的 OAuth 设备登录流程。

- iFlow（iFlow，OAuth）：
  ```bash
  ./cli-proxy-api --iflow-login
  ```
  选项：加上 `--no-browser` 可打印登录地址而不自动打开浏览器。本地 OAuth 回调端口为 `11451`。

### 启动服务器

身份验证完成后，启动服务器：

```bash
./cli-proxy-api
```

默认情况下，服务器在端口 8317 上运行。

### API 端点

#### 列出模型

```
GET http://localhost:8317/v1/models
```

#### 聊天补全

```
POST http://localhost:8317/v1/chat/completions
```

请求体示例：

```json
{
  "model": "gemini-2.5-pro",
  "messages": [
    {
      "role": "user",
      "content": "你好，你好吗？"
    }
  ],
  "stream": true
}
```

说明：
- 使用 "gemini-*" 模型（例如 "gemini-2.5-pro"）来调用 Gemini，使用 "gpt-*" 模型（例如 "gpt-5"）来调用 OpenAI，使用 "claude-*" 模型（例如 "claude-3-5-sonnet-20241022"）来调用 Claude，使用 "qwen-*" 模型（例如 "qwen3-coder-plus"）来调用 Qwen，或者使用 iFlow 支持的模型（例如 "tstars2.0"、"deepseek-v3.1"、"kimi-k2" 等）来调用 iFlow。代理服务会自动将请求路由到相应的提供商。

#### Claude 消息（SSE 兼容）

```
POST http://localhost:8317/v1/messages
```

### 与 OpenAI 库一起使用

您可以通过将基础 URL 设置为本地服务器来将此代理与任何 OpenAI 兼容的库一起使用：

#### Python（使用 OpenAI 库）

```python
from openai import OpenAI

client = OpenAI(
    api_key="dummy",  # 不使用但必需
    base_url="http://localhost:8317/v1"
)

# Gemini 示例
gemini = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "你好，你好吗？"}]
)

# Codex/GPT 示例
gpt = client.chat.completions.create(
    model="gpt-5",
    messages=[{"role": "user", "content": "用一句话总结这个项目"}]
)

# Claude 示例（使用 messages 端点）
import requests
claude_response = requests.post(
    "http://localhost:8317/v1/messages",
    json={
        "model": "claude-3-5-sonnet-20241022",
        "messages": [{"role": "user", "content": "用一句话总结这个项目"}],
        "max_tokens": 1000
    }
)

print(gemini.choices[0].message.content)
print(gpt.choices[0].message.content)
print(claude_response.json())
```

#### JavaScript/TypeScript

```javascript
import OpenAI from 'openai';

const openai = new OpenAI({
  apiKey: 'dummy', // 不使用但必需
  baseURL: 'http://localhost:8317/v1',
});

// Gemini
const gemini = await openai.chat.completions.create({
  model: 'gemini-2.5-pro',
  messages: [{ role: 'user', content: '你好，你好吗？' }],
});

// Codex/GPT
const gpt = await openai.chat.completions.create({
  model: 'gpt-5',
  messages: [{ role: 'user', content: '用一句话总结这个项目' }],
});

// Claude 示例（使用 messages 端点）
const claudeResponse = await fetch('http://localhost:8317/v1/messages', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    model: 'claude-3-5-sonnet-20241022',
    messages: [{ role: 'user', content: '用一句话总结这个项目' }],
    max_tokens: 1000
  })
});

console.log(gemini.choices[0].message.content);
console.log(gpt.choices[0].message.content);
console.log(await claudeResponse.json());
```

## 支持的模型

- gemini-2.5-pro
- gemini-2.5-flash
- gemini-2.5-flash-lite
- gemini-2.5-flash-image
- gemini-2.5-flash-image-preview
- gpt-5
- gpt-5-codex
- claude-opus-4-1-20250805
- claude-opus-4-20250514
- claude-sonnet-4-20250514
- claude-sonnet-4-5-20250929
- claude-3-7-sonnet-20250219
- claude-3-5-haiku-20241022
- qwen3-coder-plus
- qwen3-coder-flash
- qwen3-max
- qwen3-vl-plus
- deepseek-v3.2
- deepseek-v3.1
- deepseek-r1
- deepseek-v3
- kimi-k2
- glm-4.5
- glm-4.6
- tstars2.0
- 以及其他 iFlow 支持的模型
- Gemini 模型在需要时自动切换到对应的 preview 版本

## 配置

服务器默认使用位于项目根目录的 YAML 配置文件（`config.yaml`）。您可以使用 `--config` 标志指定不同的配置文件路径：

```bash
  ./cli-proxy-api --config /path/to/your/config.yaml
```

### 配置选项

| 参数                                      | 类型       | 默认值                | 描述                                                                  |
|-----------------------------------------|----------|--------------------|---------------------------------------------------------------------|
| `port`                                  | integer  | 8317               | 服务器将监听的端口号。                                                         |
| `auth-dir`                              | string   | "~/.cli-proxy-api" | 存储身份验证令牌的目录。支持使用 `~` 来表示主目录。如果你使用Windows，建议设置成`C:/cli-proxy-api/`。  |
| `proxy-url`                             | string   | ""                 | 代理URL。支持socks5/http/https协议。例如：socks5://user:pass@192.168.1.1:1080/ |
| `request-retry`                         | integer  | 0                  | 请求重试次数。如果HTTP响应码为403、408、500、502、503或504，将会触发重试。                    |
| `remote-management.allow-remote`        | boolean  | false              | 是否允许远程（非localhost）访问管理接口。为false时仅允许本地访问；本地访问同样需要管理密钥。               |
| `remote-management.secret-key`          | string   | ""                 | 管理密钥。若配置为明文，启动时会自动进行bcrypt加密并写回配置文件。若为空，管理接口整体不可用（404）。             |
| `remote-management.disable-control-panel` | boolean  | false              | 当为 true 时，不再下载 `management.html`，且 `/management.html` 会返回 404，从而禁用内置管理界面。             |
| `quota-exceeded`                        | object   | {}                 | 用于处理配额超限的配置。                                                        |
| `quota-exceeded.switch-project`         | boolean  | true               | 当配额超限时，是否自动切换到另一个项目。                                                |
| `quota-exceeded.switch-preview-model`   | boolean  | true               | 当配额超限时，是否自动切换到预览模型。                                                 |
| `debug`                                 | boolean  | false              | 启用调试模式以获取详细日志。                                                      |
| `logging-to-file`                       | boolean  | true               | 是否将应用日志写入滚动文件；设为 false 时输出到 stdout/stderr。                           |
| `usage-statistics-enabled`              | boolean  | true               | 是否启用内存中的使用统计；设为 false 时直接丢弃所有统计数据。                               |
| `api-keys`                              | string[] | []                 | 兼容旧配置的简写，会自动同步到默认 `config-api-key` 提供方。                     |
| `generative-language-api-key`           | string[] | []                 | 生成式语言API密钥列表。                                                       |
| `codex-api-key`                                       | object   | {}                 | Codex API密钥列表。                                                      |
| `codex-api-key.api-key`                               | string   | ""                 | Codex API密钥。                                                        |
| `codex-api-key.base-url`                              | string   | ""                 | 自定义的Codex API端点                                                     |
| `codex-api-key.proxy-url`                             | string   | ""                 | 针对该API密钥的代理URL。会覆盖全局proxy-url设置。支持socks5/http/https协议。                 |
| `claude-api-key`                                      | object   | {}                 | Claude API密钥列表。                                                     |
| `claude-api-key.api-key`                              | string   | ""                 | Claude API密钥。                                                       |
| `claude-api-key.base-url`                             | string   | ""                 | 自定义的Claude API端点，如果您使用第三方的API端点。                                    |
| `claude-api-key.proxy-url`                            | string   | ""                 | 针对该API密钥的代理URL。会覆盖全局proxy-url设置。支持socks5/http/https协议。                 |
| `openai-compatibility`                                | object[] | []                 | 上游OpenAI兼容提供商的配置（名称、基础URL、API密钥、模型）。                                |
| `openai-compatibility.*.name`                         | string   | ""                 | 提供商的名称。它将被用于用户代理（User Agent）和其他地方。                                  |
| `openai-compatibility.*.base-url`                     | string   | ""                 | 提供商的基础URL。                                                          |
| `openai-compatibility.*.api-keys`                     | string[] | []                 | (已弃用) 提供商的API密钥。建议改用api-key-entries以获得每密钥代理支持。                       |
| `openai-compatibility.*.api-key-entries`              | object[] | []                 | API密钥条目，支持可选的每密钥代理配置。优先于api-keys。                                   |
| `openai-compatibility.*.api-key-entries.*.api-key`    | string   | ""                 | 该条目的API密钥。                                                          |
| `openai-compatibility.*.api-key-entries.*.proxy-url`  | string   | ""                 | 针对该API密钥的代理URL。会覆盖全局proxy-url设置。支持socks5/http/https协议。                 |
| `openai-compatibility.*.models`                       | object[] | []                 | 实际的模型名称。                                                            |
| `openai-compatibility.*.models.*.name`                | string   | ""                 | 提供商支持的模型。                                                           |
| `openai-compatibility.*.models.*.alias`               | string   | ""                 | 在API中使用的别名。                                                         |

### 配置文件示例

```yaml
# 服务器端口
port: 8317

# 管理 API 设置
remote-management:
  # 是否允许远程（非localhost）访问管理接口。为false时仅允许本地访问（但本地访问同样需要管理密钥）。
  allow-remote: false

  # 管理密钥。若配置为明文，启动时会自动进行bcrypt加密并写回配置文件。
  # 所有管理请求（包括本地）都需要该密钥。
  # 若为空，/v0/management 整体处于 404（禁用）。
  secret-key: ""

  # 当设为 true 时，不下载管理面板文件，/management.html 将直接返回 404。
  disable-control-panel: false

# 身份验证目录（支持 ~ 表示主目录）。如果你使用Windows，建议设置成`C:/cli-proxy-api/`。
auth-dir: "~/.cli-proxy-api"

# 请求认证使用的API密钥
api-keys:
  - "your-api-key-1"
  - "your-api-key-2"

# 启用调试日志
debug: false

# 为 true 时将应用日志写入滚动文件而不是 stdout
logging-to-file: true

# 为 false 时禁用内存中的使用统计并直接丢弃所有数据
usage-statistics-enabled: true

# 代理URL。支持socks5/http/https协议。例如：socks5://user:pass@192.168.1.1:1080/
proxy-url: ""

# 请求重试次数。如果HTTP响应码为403、408、500、502、503或504，将会触发重试。
request-retry: 3


# 配额超限行为
quota-exceeded:
   switch-project: true # 当配额超限时是否自动切换到另一个项目
   switch-preview-model: true # 当配额超限时是否自动切换到预览模型

# AIStduio Gemini API 的 API 密钥
generative-language-api-key:
  - "AIzaSy...01"
  - "AIzaSy...02"
  - "AIzaSy...03"
  - "AIzaSy...04"

# Codex API 密钥
codex-api-key:
  - api-key: "sk-atSM..."
    base-url: "https://www.example.com" # 第三方 Codex API 中转服务端点
    proxy-url: "socks5://proxy.example.com:1080" # 可选:针对该密钥的代理设置

# Claude API 密钥
claude-api-key:
  - api-key: "sk-atSM..." # 如果使用官方 Claude API,无需设置 base-url
  - api-key: "sk-atSM..."
    base-url: "https://www.example.com" # 第三方 Claude API 中转服务端点
    proxy-url: "socks5://proxy.example.com:1080" # 可选:针对该密钥的代理设置

# OpenAI 兼容提供商
openai-compatibility:
  - name: "openrouter" # 提供商的名称；它将被用于用户代理和其它地方。
    base-url: "https://openrouter.ai/api/v1" # 提供商的基础URL。
    # 新格式：支持每密钥代理配置(推荐):
    api-key-entries:
      - api-key: "sk-or-v1-...b780"
        proxy-url: "socks5://proxy.example.com:1080" # 可选:针对该密钥的代理设置
      - api-key: "sk-or-v1-...b781" # 不进行额外代理设置
    # 旧格式(仍支持，但无法为每个密钥指定代理):
    # api-keys:
    #   - "sk-or-v1-...b780"
    #   - "sk-or-v1-...b781"
    models: # 提供商支持的模型。
      - name: "moonshotai/kimi-k2:free" # 实际的模型名称。
        alias: "kimi-k2" # 在API中使用的别名。
```

### Git 支持的配置与令牌存储

应用程序可配置为使用 Git 仓库作为后端，用于存储 `config.yaml` 配置文件和来自 `auth-dir` 目录的身份验证令牌。这允许对您的配置进行集中管理和版本控制。

要启用此功能，请将 `GITSTORE_GIT_URL` 环境变量设置为您的 Git 仓库的 URL。

**环境变量**

| 变量                      | 必需 | 默认值    | 描述                                                 |
|-------------------------|----|--------|----------------------------------------------------|
| `MANAGEMENT_PASSWORD`   | 是  |        | 管理面板密码                                             |
| `GITSTORE_GIT_URL`      | 是  |        | 要使用的 Git 仓库的 HTTPS URL。                            |
| `GITSTORE_LOCAL_PATH`   | 否  | 当前工作目录 | 将克隆 Git 仓库的本地路径。在 Docker 内部，此路径默认为 `/CLIProxyAPI`。 |
| `GITSTORE_GIT_USERNAME` | 否  |        | 用于 Git 身份验证的用户名。                                   |
| `GITSTORE_GIT_TOKEN`    | 否  |        | 用于 Git 身份验证的个人访问令牌（或密码）。                           |

**工作原理**

1.  **克隆：** 启动时，应用程序会将远程 Git 仓库克隆到 `GITSTORE_LOCAL_PATH`。
2.  **配置：** 然后，它会在克隆的仓库内的 `config` 目录中查找 `config.yaml` 文件。
3.  **引导：** 如果仓库中不存在 `config/config.yaml`，应用程序会将本地的 `config.example.yaml` 复制到该位置，然后提交并推送到远程仓库作为初始配置。您必须确保 `config.example.yaml` 文件可用。
4.  **令牌同步：** `auth-dir` 也在此仓库中管理。对身份验证令牌的任何更改（例如，通过新的登录）都会自动提交并推送到远程 Git 仓库。

### PostgreSQL 支持的配置与令牌存储

在托管环境中运行服务时，可以选择使用 PostgreSQL 来保存配置与令牌，借助托管数据库减轻本地文件管理压力。

**环境变量**

| 变量                      | 必需 | 默认值          | 描述                                                                 |
|-------------------------|----|---------------|----------------------------------------------------------------------|
| `MANAGEMENT_PASSWORD`   | 是  |               | 管理面板密码（启用远程管理时必需）。                                          |
| `PGSTORE_DSN`           | 是  |               | PostgreSQL 连接串，例如 `postgresql://user:pass@host:5432/db`。       |
| `PGSTORE_SCHEMA`        | 否  | public        | 创建表时使用的 schema；留空则使用默认 schema。                               |
| `PGSTORE_LOCAL_PATH`    | 否  | 当前工作目录       | 本地镜像根目录，服务将在 `<值>/pgstore` 下写入缓存；若无法获取工作目录则退回 `/tmp/pgstore`。 |

**工作原理**

1.  **初始化：** 启动时通过 `PGSTORE_DSN` 连接数据库，确保 schema 存在，并在缺失时创建 `config_store` 与 `auth_store`。
2.  **本地镜像：** 在 `<PGSTORE_LOCAL_PATH 或当前工作目录>/pgstore` 下建立可写缓存，复用 `config/config.yaml` 与 `auths/` 目录。
3.  **引导：** 若数据库中无配置记录，会使用 `config.example.yaml` 初始化，并以固定标识 `config` 写入。
4.  **令牌同步：** 配置与令牌的更改会写入 PostgreSQL，同时数据库中的内容也会反向同步至本地镜像，便于文件监听与管理接口继续工作。

### 对象存储驱动的配置与令牌存储

可以选择使用 S3 兼容的对象存储来托管配置与鉴权数据。

**环境变量**

| 变量                     | 是否必填 | 默认值             | 说明                                                                                                                     |
|--------------------------|----------|--------------------|--------------------------------------------------------------------------------------------------------------------------|
| `MANAGEMENT_PASSWORD`    | 是       |                    | 管理面板密码（启用远程管理时必需）。                                                                             |
| `OBJECTSTORE_ENDPOINT`   | 是       |                    | 对象存储访问端点。可带 `http://` 或 `https://` 前缀指定协议（省略则默认 HTTPS）。                                      |
| `OBJECTSTORE_BUCKET`     | 是       |                    | 用于存放 `config/config.yaml` 与 `auths/*.json` 的 Bucket 名称。                                                        |
| `OBJECTSTORE_ACCESS_KEY` | 是       |                    | 对象存储账号的访问密钥 ID。                                                                                              |
| `OBJECTSTORE_SECRET_KEY` | 是       |                    | 对象存储账号的访问密钥 Secret。                                                                                          |
| `OBJECTSTORE_LOCAL_PATH` | 否       | 当前工作目录 (CWD) | 本地镜像根目录；服务会写入到 `<值>/objectstore`。                                                                         |

**工作流程**

1. **启动阶段：** 解析端点地址（识别协议前缀），创建 MinIO 兼容客户端并使用 Path-Style 模式，如 Bucket 不存在会自动创建。
2. **本地镜像：** 在 `<OBJECTSTORE_LOCAL_PATH 或当前工作目录>/objectstore` 维护可写缓存，同步 `config/config.yaml` 与 `auths/`。
3. **初始化：** 若 Bucket 中缺少配置文件，将以 `config.example.yaml` 为模板生成 `config/config.yaml` 并上传。
4. **双向同步：** 本地变更会上传到对象存储，同时远端对象也会拉回到本地，保证文件监听、管理 API 与 CLI 命令行为一致。

### OpenAI 兼容上游提供商

通过 `openai-compatibility` 配置上游 OpenAI 兼容提供商（例如 OpenRouter）。

- name：内部识别名
- base-url：提供商基础地址
- api-key-entries：API密钥条目列表，支持可选的每密钥代理配置（推荐）
- api-keys：(已弃用) 简单的API密钥列表，不支持代理配置
- models：将上游模型 `name` 映射为本地可用 `alias`

支持每密钥代理配置的示例：

```yaml
openai-compatibility:
  - name: "openrouter"
    base-url: "https://openrouter.ai/api/v1"
    api-key-entries:
      - api-key: "sk-or-v1-...b780"
        proxy-url: "socks5://proxy.example.com:1080"
      - api-key: "sk-or-v1-...b781"
    models:
      - name: "moonshotai/kimi-k2:free"
        alias: "kimi-k2"
```

旧格式（仍支持）：

```yaml
openai-compatibility:
  - name: "openrouter"
    base-url: "https://openrouter.ai/api/v1"
    api-keys:
      - "sk-or-v1-...b780"
      - "sk-or-v1-...b781"
    models:
      - name: "moonshotai/kimi-k2:free"
        alias: "kimi-k2"
```

使用方式：在 `/v1/chat/completions` 中将 `model` 设为别名（如 `kimi-k2`），代理将自动路由到对应提供商与模型。

并且，对于这些与OpenAI兼容的提供商模型，您始终可以通过将CODE_ASSIST_ENDPOINT设置为 http://127.0.0.1:8317 来使用Gemini CLI。

### 身份验证目录

`auth-dir` 参数指定身份验证令牌的存储位置。当您运行登录命令时，应用程序将在此目录中创建包含 Google 账户身份验证令牌的 JSON 文件。多个账户可用于轮询。

### 官方生成式语言 API

`generative-language-api-key` 参数允许您定义可用于验证对官方 AIStudio Gemini API 请求的 API 密钥列表。

## 热更新

服务会监听配置文件与 `auth-dir` 目录的变化并自动重新加载客户端与配置。您可以在运行中新增/移除 Gemini/OpenAI 的令牌 JSON 文件，无需重启服务。

## Gemini CLI 多账户负载均衡

启动 CLI 代理 API 服务器，然后将 `CODE_ASSIST_ENDPOINT` 环境变量设置为 CLI 代理 API 服务器的 URL。

```bash
export CODE_ASSIST_ENDPOINT="http://127.0.0.1:8317"
```

服务器将中继 `loadCodeAssist`、`onboardUser` 和 `countTokens` 请求。并自动在多个账户之间轮询文本生成请求。

> [!NOTE]  
> 此功能仅允许本地访问，因为找不到一个可以验证请求的方法。
> 所以只能强制只有 `127.0.0.1` 可以访问。

## Claude Code 的使用方法

启动 CLI Proxy API 服务器, 设置如下系统环境变量 `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL`

使用 Gemini 模型：
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=gemini-2.5-pro
export ANTHROPIC_SMALL_FAST_MODEL=gemini-2.5-flash
```

使用 OpenAI GPT 5 模型：
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=gpt-5
export ANTHROPIC_SMALL_FAST_MODEL=gpt-5-minimal
```

使用 OpenAI GPT 5 Codex 模型:
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=gpt-5-codex
export ANTHROPIC_SMALL_FAST_MODEL=gpt-5-codex-low
```


使用 Claude 模型：
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=claude-sonnet-4-20250514
export ANTHROPIC_SMALL_FAST_MODEL=claude-3-5-haiku-20241022
```

使用 Qwen 模型：
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=qwen3-coder-plus
export ANTHROPIC_SMALL_FAST_MODEL=qwen3-coder-flash
```

使用 iFlow 模型：
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
export ANTHROPIC_MODEL=qwen3-max
export ANTHROPIC_SMALL_FAST_MODEL=qwen3-235b-a22b-instruct
```

## Codex 多账户负载均衡

启动 CLI Proxy API 服务器, 修改 `~/.codex/config.toml` 和 `~/.codex/auth.json` 文件。

config.toml:
```toml
model_provider = "cliproxyapi"
model = "gpt-5-codex" # 或者是gpt-5，你也可以使用任何我们支持的模型
model_reasoning_effort = "high"

[model_providers.cliproxyapi]
name = "cliproxyapi"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "responses"
```

auth.json:
```json
{
  "OPENAI_API_KEY": "sk-dummy"
}
```

## 使用 Docker 运行

运行以下命令进行登录（Gemini OAuth，端口 8085）：

```bash
docker run --rm -p 8085:8085 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --login
```

运行以下命令进行登录（OpenAI OAuth，端口 1455）：

```bash
docker run --rm -p 1455:1455 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --codex-login
```

运行以下命令进行登录（Claude OAuth，端口 54545）：

```bash
docker run --rm -p 54545:54545 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --claude-login
```

运行以下命令进行登录（Qwen OAuth）：

```bash
docker run -it -rm -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --qwen-login
```

运行以下命令进行登录（iFlow OAuth，端口 11451）：

```bash
docker run --rm -p 11451:11451 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --iflow-login
```


运行以下命令启动服务器：

```bash
docker run --rm -p 8317:8317 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest
```

> [!NOTE]
> 要在 Docker 中使用 Git 支持的配置存储，您可以使用 `-e` 标志传递 `GITSTORE_*` 环境变量。例如：
>
> ```bash
> docker run --rm -p 8317:8317 \
>   -e GITSTORE_GIT_URL="https://github.com/your/config-repo.git" \
>   -e GITSTORE_GIT_TOKEN="your_personal_access_token" \
>   -v /path/to/your/git-store:/CLIProxyAPI/remote \
>   eceasy/cli-proxy-api:latest
> ```
> 在这种情况下，您可能不需要直接挂载 `config.yaml` 或 `auth-dir`，因为它们将由容器内的 Git 存储在 `GITSTORE_LOCAL_PATH`（默认为 `/CLIProxyAPI`，在此示例中我们将其设置为 `/CLIProxyAPI/remote`）进行管理。

## 使用 Docker Compose 运行

1.  克隆仓库并进入目录：
    ```bash
    git clone https://github.com/luispater/CLIProxyAPI.git
    cd CLIProxyAPI
    ```

2.  准备配置文件：
    通过复制示例文件来创建 `config.yaml` 文件，并根据您的需求进行自定义。
    ```bash
    cp config.example.yaml config.yaml
    ```
    *（Windows 用户请注意：您可以在 CMD 或 PowerShell 中使用 `copy config.example.yaml config.yaml`。）*

    要在 Docker Compose 中使用 Git 支持的配置存储，您可以将 `GITSTORE_*` 环境变量添加到 `docker-compose.yml` 文件中的 `cli-proxy-api` 服务定义下。例如：
    ```yaml
    services:
      cli-proxy-api:
        image: eceasy/cli-proxy-api:latest
        container_name: cli-proxy-api
        ports:
          - "8317:8317"
          - "8085:8085"
          - "1455:1455"
          - "54545:54545"
          - "11451:11451"
        environment:
          - GITSTORE_GIT_URL=https://github.com/your/config-repo.git
          - GITSTORE_GIT_TOKEN=your_personal_access_token
        volumes:
          - ./git-store:/CLIProxyAPI/remote # GITSTORE_LOCAL_PATH
        restart: unless-stopped
    ```
    在使用 Git 存储时，您可能不需要直接挂载 `config.yaml` 或 `auth-dir`。

3.  启动服务：
    -   **适用于大多数用户（推荐）：**
        运行以下命令，使用 Docker Hub 上的预构建镜像启动服务。服务将在后台运行。
        ```bash
        docker compose up -d
        ```
    -   **适用于进阶用户：**
        如果您修改了源代码并需要构建新镜像，请使用交互式辅助脚本：
        -   对于 Windows (PowerShell):
            ```powershell
            .\docker-build.ps1
            ```
        -   对于 Linux/macOS:
            ```bash
            bash docker-build.sh
            ```
        脚本将提示您选择运行方式：
        - **选项 1：使用预构建的镜像运行 (推荐)**：从镜像仓库拉取最新的官方镜像并启动容器。这是最简单的开始方式。
        - **选项 2：从源码构建并运行 (适用于开发者)**：从本地源代码构建镜像，将其标记为 `cli-proxy-api:local`，然后启动容器。如果您需要修改源代码，此选项很有用。

4. 要在容器内运行登录命令进行身份验证：
    - **Gemini**: 
    ```bash
    docker compose exec cli-proxy-api /CLIProxyAPI/CLIProxyAPI -no-browser --login
    ```
    - **OpenAI (Codex)**:
    ```bash
    docker compose exec cli-proxy-api /CLIProxyAPI/CLIProxyAPI -no-browser --codex-login
    ```
    - **Claude**:
    ```bash
    docker compose exec cli-proxy-api /CLIProxyAPI/CLIProxyAPI -no-browser --claude-login
    ```
    - **Qwen**:
    ```bash
    docker compose exec cli-proxy-api /CLIProxyAPI/CLIProxyAPI -no-browser --qwen-login
    ```
    - **iFlow**:
    ```bash
    docker compose exec cli-proxy-api /CLIProxyAPI/CLIProxyAPI -no-browser --iflow-login
    ```

5.  查看服务器日志：
    ```bash
    docker compose logs -f
    ```

6.  停止应用程序：
    ```bash
    docker compose down
    ```

## 管理 API 文档

请参见 [MANAGEMENT_API_CN.md](MANAGEMENT_API_CN.md)

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
