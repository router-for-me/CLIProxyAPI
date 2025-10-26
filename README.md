# CLI Proxy API

English | [中文](README_CN.md)

A proxy server that provides OpenAI/Gemini/Claude/Codex compatible API interfaces for CLI.

It now also supports OpenAI Codex (GPT models) and Claude Code via OAuth.

So you can use local or multi-account CLI access with OpenAI(include Responses)/Gemini/Claude-compatible clients and SDKs.

Chinese providers have now been added: [Qwen Code](https://github.com/QwenLM/qwen-code), [iFlow](https://iflow.cn/).

## Features

- OpenAI/Gemini/Claude compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Claude Code support via OAuth login
- Qwen Code support via OAuth login
- iFlow support via OAuth login
- Streaming and non-streaming responses
- Function calling/tools support
- Multimodal input support (text and images)
- Multiple accounts with round-robin load balancing (Gemini, OpenAI, Claude, Qwen and iFlow)
- Simple CLI authentication flows (Gemini, OpenAI, Claude, Qwen and iFlow)
- Generative Language API Key support
- Gemini CLI multi-account load balancing
- Claude Code multi-account load balancing
- Qwen Code multi-account load balancing
- iFlow multi-account load balancing
- OpenAI Codex multi-account load balancing
- OpenAI-compatible upstream providers via config (e.g., OpenRouter)
- Reusable Go SDK for embedding the proxy (see `docs/sdk-usage.md`)

## Installation

### Prerequisites

- Go 1.24 or higher
- A Google account with access to Gemini CLI models (optional)
- An OpenAI account for Codex/GPT access (optional)
- An Anthropic account for Claude Code access (optional)
- A Qwen Chat account for Qwen Code access (optional)
- An iFlow account for iFlow access (optional)

### Building from Source

1. Clone the repository:
   ```bash
   git clone https://github.com/luispater/CLIProxyAPI.git
   cd CLIProxyAPI
   ```

2. Build the application:
   
   Linux, macOS:
   ```bash
   go build -o cli-proxy-api ./cmd/server
   ```
   Windows: 
   ```bash
   go build -o cli-proxy-api.exe ./cmd/server
   ```

### Installation via Homebrew

```bash
brew install cliproxyapi
brew services start cliproxyapi
```

## Usage

### GUI Client & Official WebUI

#### [EasyCLI](https://github.com/router-for-me/EasyCLI)

A cross-platform desktop GUI client for CLIProxyAPI. 

#### [Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center)

A web-based management center for CLIProxyAPI.  

Set `remote-management.disable-control-panel` to `true` if you prefer to host the management UI elsewhere; the server will skip downloading `management.html` and `/management.html` will return 404.

You can set the `MANAGEMENT_STATIC_PATH` environment variable to choose the directory where `management.html` is stored.

### Authentication

You can authenticate for Gemini, OpenAI, Claude, Qwen, and/or iFlow. All can coexist in the same `auth-dir` and will be load balanced.

- Gemini (Google):
  ```bash
  ./cli-proxy-api --login
  ```
  If you are an existing Gemini Code user, you may need to specify a project ID:
  ```bash
  ./cli-proxy-api --login --project_id <your_project_id>
  ```
  The local OAuth callback uses port `8085`.

  Options: add `--no-browser` to print the login URL instead of opening a browser. The local OAuth callback uses port `8085`.

- OpenAI (Codex/GPT via OAuth):
  ```bash
  ./cli-proxy-api --codex-login
  ```
  Options: add `--no-browser` to print the login URL instead of opening a browser. The local OAuth callback uses port `1455`.

- Claude (Anthropic via OAuth):
  ```bash
  ./cli-proxy-api --claude-login
  ```
  Options: add `--no-browser` to print the login URL instead of opening a browser. The local OAuth callback uses port `54545`.

- Qwen (Qwen Chat via OAuth):
  ```bash
  ./cli-proxy-api --qwen-login
  ```
  Options: add `--no-browser` to print the login URL instead of opening a browser. Use the Qwen Chat's OAuth device flow.

- iFlow (iFlow via OAuth):
  ```bash
  ./cli-proxy-api --iflow-login
  ```
  Options: add `--no-browser` to print the login URL instead of opening a browser. The local OAuth callback uses port `11451`.


### Starting the Server

Once authenticated, start the server:

```bash
./cli-proxy-api
```

By default, the server runs on port 8317.

### API Endpoints

#### List Models

```
GET http://localhost:8317/v1/models
```

#### Chat Completions

```
POST http://localhost:8317/v1/chat/completions
```

Request body example:

```json
{
  "model": "gemini-2.5-pro",
  "messages": [
    {
      "role": "user",
      "content": "Hello, how are you?"
    }
  ],
  "stream": true
}
```

Notes:
- Use a `gemini-*` model for Gemini (e.g., "gemini-2.5-pro"), a `gpt-*` model for OpenAI (e.g., "gpt-5"), a `claude-*` model for Claude (e.g., "claude-3-5-sonnet-20241022"), a `qwen-*` model for Qwen (e.g., "qwen3-coder-plus"), or an iFlow-supported model (e.g., "tstars2.0", "deepseek-v3.1", "kimi-k2", etc.). The proxy will route to the correct provider automatically.

#### Claude Messages (SSE-compatible)

```
POST http://localhost:8317/v1/messages
```

### Using with OpenAI Libraries

You can use this proxy with any OpenAI-compatible library by setting the base URL to your local server:

#### Python (with OpenAI library)

```python
from openai import OpenAI

client = OpenAI(
    api_key="dummy",  # Not used but required
    base_url="http://localhost:8317/v1"
)

# Gemini example
gemini = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "Hello, how are you?"}]
)

# Codex/GPT example
gpt = client.chat.completions.create(
    model="gpt-5",
    messages=[{"role": "user", "content": "Summarize this project in one sentence."}]
)

# Claude example (using messages endpoint)
import requests
claude_response = requests.post(
    "http://localhost:8317/v1/messages",
    json={
        "model": "claude-3-5-sonnet-20241022",
        "messages": [{"role": "user", "content": "Summarize this project in one sentence."}],
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
  apiKey: 'dummy', // Not used but required
  baseURL: 'http://localhost:8317/v1',
});

// Gemini
const gemini = await openai.chat.completions.create({
  model: 'gemini-2.5-pro',
  messages: [{ role: 'user', content: 'Hello, how are you?' }],
});

// Codex/GPT
const gpt = await openai.chat.completions.create({
  model: 'gpt-5',
  messages: [{ role: 'user', content: 'Summarize this project in one sentence.' }],
});

// Claude example (using messages endpoint)
const claudeResponse = await fetch('http://localhost:8317/v1/messages', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    model: 'claude-3-5-sonnet-20241022',
    messages: [{ role: 'user', content: 'Summarize this project in one sentence.' }],
    max_tokens: 1000
  })
});

console.log(gemini.choices[0].message.content);
console.log(gpt.choices[0].message.content);
console.log(await claudeResponse.json());
```

## Supported Models

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
- And other iFlow-supported models
- Gemini models auto-switch to preview variants when needed

## Configuration

The server uses a YAML configuration file (`config.yaml`) located in the project root directory by default. You can specify a different configuration file path using the `--config` flag:

```bash
./cli-proxy-api --config /path/to/your/config.yaml
```

### Configuration Options

| Parameter                               | Type     | Default            | Description                                                                                                                                                                               |
|-----------------------------------------|----------|--------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `port`                                  | integer  | 8317               | The port number on which the server will listen.                                                                                                                                          |
| `auth-dir`                              | string   | "~/.cli-proxy-api" | Directory where authentication tokens are stored. Supports using `~` for the home directory. If you use Windows, please set the directory like this: `C:/cli-proxy-api/`                  |
| `proxy-url`                             | string   | ""                 | Proxy URL. Supports socks5/http/https protocols. Example: socks5://user:pass@192.168.1.1:1080/                                                                                            |
| `request-retry`                         | integer  | 0                  | Number of times to retry a request. Retries will occur if the HTTP response code is 403, 408, 500, 502, 503, or 504.                                                                      |
| `remote-management.allow-remote`        | boolean  | false              | Whether to allow remote (non-localhost) access to the management API. If false, only localhost can access. A management key is still required for localhost.                              |
| `remote-management.secret-key`          | string   | ""                 | Management key. If a plaintext value is provided, it will be hashed on startup using bcrypt and persisted back to the config file. If empty, the entire management API is disabled (404). |
| `remote-management.disable-control-panel` | boolean  | false              | When true, skip downloading `management.html` and return 404 for `/management.html`, effectively disabling the bundled management UI.                                                        |
| `quota-exceeded`                        | object   | {}                 | Configuration for handling quota exceeded.                                                                                                                                                |
| `quota-exceeded.switch-project`         | boolean  | true               | Whether to automatically switch to another project when a quota is exceeded.                                                                                                              |
| `quota-exceeded.switch-preview-model`   | boolean  | true               | Whether to automatically switch to a preview model when a quota is exceeded.                                                                                                              |
| `debug`                                 | boolean  | false              | Enable debug mode for verbose logging.                                                                                                                                                    |
| `logging-to-file`                       | boolean  | true               | Write application logs to rotating files instead of stdout. Set to `false` to log to stdout/stderr.                                                                                      |
| `usage-statistics-enabled`              | boolean  | true               | Enable in-memory usage aggregation for management APIs. Disable to drop all collected usage metrics.                                                                                    |
| `api-keys`                              | string[] | []                 | Legacy shorthand for inline API keys. Values are mirrored into the `config-api-key` provider for backwards compatibility.                                                                 |
| `generative-language-api-key`           | string[] | []                 | List of Generative Language API keys.                                                                                                                                                     |
| `codex-api-key`                                    | object   | {}                 | List of Codex API keys.                                                                                                                                                                   |
| `codex-api-key.api-key`                            | string   | ""                 | Codex API key.                                                                                                                                                                            |
| `codex-api-key.base-url`                           | string   | ""                 | Custom Codex API endpoint, if you use a third-party API endpoint.                                                                                                                         |
| `codex-api-key.proxy-url`                          | string   | ""                 | Proxy URL for this specific API key. Overrides the global proxy-url setting. Supports socks5/http/https protocols.                                                                        |
| `claude-api-key`                                   | object   | {}                 | List of Claude API keys.                                                                                                                                                                  |
| `claude-api-key.api-key`                           | string   | ""                 | Claude API key.                                                                                                                                                                           |
| `claude-api-key.base-url`                          | string   | ""                 | Custom Claude API endpoint, if you use a third-party API endpoint.                                                                                                                        |
| `claude-api-key.proxy-url`                         | string   | ""                 | Proxy URL for this specific API key. Overrides the global proxy-url setting. Supports socks5/http/https protocols.                                                                        |
| `openai-compatibility`                             | object[] | []                 | Upstream OpenAI-compatible providers configuration (name, base-url, api-keys, models).                                                                                                    |
| `openai-compatibility.*.name`                      | string   | ""                 | The name of the provider. It will be used in the user agent and other places.                                                                                                             |
| `openai-compatibility.*.base-url`                  | string   | ""                 | The base URL of the provider.                                                                                                                                                             |
| `openai-compatibility.*.api-keys`                  | string[] | []                 | (Deprecated) The API keys for the provider. Use api-key-entries instead for per-key proxy support.                                                                                        |
| `openai-compatibility.*.api-key-entries`           | object[] | []                 | API key entries with optional per-key proxy configuration. Preferred over api-keys.                                                                                                        |
| `openai-compatibility.*.api-key-entries.*.api-key` | string   | ""                 | The API key for this entry.                                                                                                                                                               |
| `openai-compatibility.*.api-key-entries.*.proxy-url` | string | ""                 | Proxy URL for this specific API key. Overrides the global proxy-url setting. Supports socks5/http/https protocols.                                                                      |
| `openai-compatibility.*.models`                    | object[] | []                 | The actual model name.                                                                                                                                                                    |
| `openai-compatibility.*.models.*.name`             | string   | ""                 | The models supported by the provider.                                                                                                                                                     |
| `openai-compatibility.*.models.*.alias`            | string   | ""                 | The alias used in the API.                                                                                                                                                                |

### Example Configuration File

```yaml
# Server port
port: 8317

# Management API settings
remote-management:
  # Whether to allow remote (non-localhost) management access.
  # When false, only localhost can access management endpoints (a key is still required).
  allow-remote: false

  # Management key. If a plaintext value is provided here, it will be hashed on startup.
  # All management requests (even from localhost) require this key.
  # Leave empty to disable the Management API entirely (404 for all /v0/management routes).
  secret-key: ""

  # Disable the bundled management control panel asset download and HTTP route when true.
  disable-control-panel: false

# Authentication directory (supports ~ for home directory). If you use Windows, please set the directory like this: `C:/cli-proxy-api/`
auth-dir: "~/.cli-proxy-api"

# API keys for authentication
api-keys:
  - "your-api-key-1"
  - "your-api-key-2"

# Enable debug logging
debug: false

# When true, write application logs to rotating files instead of stdout
logging-to-file: true

# When false, disable in-memory usage statistics aggregation
usage-statistics-enabled: true

# Proxy URL. Supports socks5/http/https protocols. Example: socks5://user:pass@192.168.1.1:1080/
proxy-url: ""

# Number of times to retry a request. Retries will occur if the HTTP response code is 403, 408, 500, 502, 503, or 504.
request-retry: 3

# Quota exceeded behavior
quota-exceeded:
   switch-project: true # Whether to automatically switch to another project when a quota is exceeded
   switch-preview-model: true # Whether to automatically switch to a preview model when a quota is exceeded

# API keys for official Generative Language API
generative-language-api-key:
  - "AIzaSy...01"
  - "AIzaSy...02"
  - "AIzaSy...03"
  - "AIzaSy...04"

# Codex API keys
codex-api-key:
  - api-key: "sk-atSM..."
    base-url: "https://www.example.com" # use the custom codex API endpoint
    proxy-url: "socks5://proxy.example.com:1080" # optional: per-key proxy override

# Claude API keys
claude-api-key:
  - api-key: "sk-atSM..." # use the official claude API key, no need to set the base url
  - api-key: "sk-atSM..."
    base-url: "https://www.example.com" # use the custom claude API endpoint
    proxy-url: "socks5://proxy.example.com:1080" # optional: per-key proxy override

# OpenAI compatibility providers
openai-compatibility:
  - name: "openrouter" # The name of the provider; it will be used in the user agent and other places.
    base-url: "https://openrouter.ai/api/v1" # The base URL of the provider.
    # New format with per-key proxy support (recommended):
    api-key-entries:
      - api-key: "sk-or-v1-...b780"
        proxy-url: "socks5://proxy.example.com:1080" # optional: per-key proxy override
      - api-key: "sk-or-v1-...b781" # without proxy-url
    # Legacy format (still supported, but cannot specify proxy per key):
    # api-keys:
    #   - "sk-or-v1-...b780"
    #   - "sk-or-v1-...b781"
    models: # The models supported by the provider.
      - name: "moonshotai/kimi-k2:free" # The actual model name.
        alias: "kimi-k2" # The alias used in the API.
```

### Git-backed Configuration and Token Store

The application can be configured to use a Git repository as a backend for storing both the `config.yaml` file and the authentication tokens from the `auth-dir`. This allows for centralized management and versioning of your configuration.

To enable this feature, set the `GITSTORE_GIT_URL` environment variable to the URL of your Git repository.

**Environment Variables**

| Variable                | Required | Default                   | Description                                                                                             |
|-------------------------|----------|---------------------------|---------------------------------------------------------------------------------------------------------|
| `MANAGEMENT_PASSWORD`   | Yes      |                           | The password for management webui.                                                                      |
| `GITSTORE_GIT_URL`      | Yes      |                           | The HTTPS URL of the Git repository to use.                                                             |
| `GITSTORE_LOCAL_PATH`   | No       | Current working directory | The local path where the Git repository will be cloned. Inside Docker, this defaults to `/CLIProxyAPI`. |
| `GITSTORE_GIT_USERNAME` | No       |                           | The username for Git authentication.                                                                    |
| `GITSTORE_GIT_TOKEN`    | No       |                           | The personal access token (or password) for Git authentication.                                         |

**How it Works**

1.  **Cloning:** On startup, the application clones the remote Git repository to the `GITSTORE_LOCAL_PATH`.
2.  **Configuration:** It then looks for a `config.yaml` inside a `config` directory within the cloned repository.
3.  **Bootstrapping:** If `config/config.yaml` does not exist in the repository, the application will copy the local `config.example.yaml` to that location, commit, and push it to the remote repository as an initial configuration. You must have `config.example.yaml` available.
4.  **Token Sync:** The `auth-dir` is also managed within this repository. Any changes to authentication tokens (e.g., through a new login) are automatically committed and pushed to the remote Git repository.

### PostgreSQL-backed Configuration and Token Store

You can also persist configuration and authentication data in PostgreSQL when running CLIProxyAPI in hosted environments that favor managed databases over local files.

**Environment Variables**

| Variable              | Required | Default               | Description                                                                                                   |
|-----------------------|----------|-----------------------|---------------------------------------------------------------------------------------------------------------|
| `MANAGEMENT_PASSWORD` | Yes      |                       | Password for the management web UI (required when remote management is enabled).                              |
| `PGSTORE_DSN`         | Yes      |                       | PostgreSQL connection string (e.g. `postgresql://user:pass@host:5432/db`).                                     |
| `PGSTORE_SCHEMA`      | No       | public                | Schema where the tables will be created. Leave empty to use the default schema.                                |
| `PGSTORE_LOCAL_PATH`  | No       | Current working directory  | Root directory for the local mirror; the server writes to `<value>/pgstore`. If unset and CWD is unavailable, `/tmp/pgstore` is used. |

**How it Works**

1.  **Initialization:** On startup the server connects via `PGSTORE_DSN`, ensures the schema exists, and creates the `config_store` / `auth_store` tables when missing.
2.  **Local Mirror:** A writable cache at `<PGSTORE_LOCAL_PATH or CWD>/pgstore` mirrors `config/config.yaml` and `auths/` so the rest of the application can reuse the existing file-based logic.
3.  **Bootstrapping:** If no configuration row exists, `config.example.yaml` seeds the database using the fixed identifier `config`.
4.  **Token Sync:** Changes flow both ways—file updates are written to PostgreSQL and database records are mirrored back to disk so watchers and management APIs continue to operate.

### Object Storage-backed Configuration and Token Store

An S3-compatible object storage service can host configuration and authentication records.

**Environment Variables**

| Variable                 | Required | Default                        | Description                                                                                                              |
|--------------------------|----------|--------------------------------|--------------------------------------------------------------------------------------------------------------------------|
| `MANAGEMENT_PASSWORD`    | Yes      |                                | Password for the management web UI (required when remote management is enabled).                                        |
| `OBJECTSTORE_ENDPOINT`   | Yes      |                                | Object storage endpoint. Include `http://` or `https://` to force the protocol (omitted scheme → HTTPS).                |
| `OBJECTSTORE_BUCKET`     | Yes      |                                | Bucket that stores `config/config.yaml` and `auths/*.json`.                                                             |
| `OBJECTSTORE_ACCESS_KEY` | Yes      |                                | Access key ID for the object storage account.                                                                           |
| `OBJECTSTORE_SECRET_KEY` | Yes      |                                | Secret key for the object storage account.                                                                              |
| `OBJECTSTORE_LOCAL_PATH` | No       | Current working directory      | Root directory for the local mirror; the server writes to `<value>/objectstore`. If unset, defaults to current CWD.     |

**How it Works**

1. **Startup:** The endpoint is parsed (respecting any scheme prefix), a MinIO-compatible client is created in path-style mode, and the bucket is created when missing.
2. **Local Mirror:** A writable cache at `<OBJECTSTORE_LOCAL_PATH or CWD>/objectstore` mirrors `config/config.yaml` and `auths/`.
3. **Bootstrapping:** When `config/config.yaml` is absent in the bucket, the server copies `config.example.yaml`, uploads it, and uses it as the initial configuration.
4. **Sync:** Changes to configuration or auth files are uploaded to the bucket, and remote updates are mirrored back to disk, keeping watchers and management APIs in sync.

### OpenAI Compatibility Providers

Configure upstream OpenAI-compatible providers (e.g., OpenRouter) via `openai-compatibility`.

- name: provider identifier used internally
- base-url: provider base URL
- api-key-entries: list of API key entries with optional per-key proxy configuration (recommended)
- api-keys: (deprecated) simple list of API keys without proxy support
- models: list of mappings from upstream model `name` to local `alias`

Example with per-key proxy support:

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

Legacy format (still supported):

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

Usage: 

Call OpenAI's endpoint `/v1/chat/completions` with `model` set to the alias (e.g., `kimi-k2`). The proxy routes to the configured provider/model automatically.

Also, you may call Claude's endpoint `/v1/messages`, Gemini's `/v1beta/models/model-name:streamGenerateContent` or `/v1beta/models/model-name:generateContent`.

And you can always use Gemini CLI with `CODE_ASSIST_ENDPOINT` set to `http://127.0.0.1:8317` for these OpenAI-compatible provider's models.


### Authentication Directory

The `auth-dir` parameter specifies where authentication tokens are stored. When you run the login command, the application will create JSON files in this directory containing the authentication tokens for your Google accounts. Multiple accounts can be used for load balancing.

### Official Generative Language API

The `generative-language-api-key` parameter allows you to define a list of API keys that can be used to authenticate requests to the official Generative Language API.

## Hot Reloading

The server watches the config file and the `auth-dir` for changes and reloads clients and settings automatically. You can add or remove Gemini/OpenAI token JSON files while the server is running; no restart is required.

## Gemini CLI with multiple account load balancing

Start CLI Proxy API server, and then set the `CODE_ASSIST_ENDPOINT` environment variable to the URL of the CLI Proxy API server.

```bash
export CODE_ASSIST_ENDPOINT="http://127.0.0.1:8317"
```

The server will relay the `loadCodeAssist`, `onboardUser`, and `countTokens` requests. And automatically load balance the text generation requests between the multiple accounts.

> [!NOTE]  
> This feature only allows local access because there is currently no way to authenticate the requests.   
> 127.0.0.1 is hardcoded for load balancing.

## Claude Code with multiple account load balancing

Start CLI Proxy API server, and then set the `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL` (or `ANTHROPIC_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL` for version 1.x.x) environment variables.

Using Gemini models:
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
# version 2.x.x
export ANTHROPIC_DEFAULT_OPUS_MODEL=gemini-2.5-pro
export ANTHROPIC_DEFAULT_SONNET_MODEL=gemini-2.5-flash
export ANTHROPIC_DEFAULT_HAIKU_MODEL=gemini-2.5-flash-lite
# version 1.x.x
export ANTHROPIC_MODEL=gemini-2.5-pro
export ANTHROPIC_SMALL_FAST_MODEL=gemini-2.5-flash
```

Using OpenAI GPT 5 models:
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
# version 2.x.x
export ANTHROPIC_DEFAULT_OPUS_MODEL=gpt-5-high
export ANTHROPIC_DEFAULT_SONNET_MODEL=gpt-5-medium
export ANTHROPIC_DEFAULT_HAIKU_MODEL=gpt-5-minimal
# version 1.x.x
export ANTHROPIC_MODEL=gpt-5
export ANTHROPIC_SMALL_FAST_MODEL=gpt-5-minimal
```

Using OpenAI GPT 5 Codex models:
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
# version 2.x.x
export ANTHROPIC_DEFAULT_OPUS_MODEL=gpt-5-codex-high
export ANTHROPIC_DEFAULT_SONNET_MODEL=gpt-5-codex-medium
export ANTHROPIC_DEFAULT_HAIKU_MODEL=gpt-5-codex-low
# version 1.x.x
export ANTHROPIC_MODEL=gpt-5-codex
export ANTHROPIC_SMALL_FAST_MODEL=gpt-5-codex-low
```

Using Claude models:
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
# version 2.x.x
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-1-20250805
export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-5-20250929
export ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-3-5-haiku-20241022
# version 1.x.x
export ANTHROPIC_MODEL=claude-sonnet-4-20250514
export ANTHROPIC_SMALL_FAST_MODEL=claude-3-5-haiku-20241022
```

Using Qwen models:
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
# version 2.x.x
export ANTHROPIC_DEFAULT_OPUS_MODEL=qwen3-coder-plus
export ANTHROPIC_DEFAULT_SONNET_MODEL=qwen3-coder-plus
export ANTHROPIC_DEFAULT_HAIKU_MODEL=qwen3-coder-flash
# version 1.x.x
export ANTHROPIC_MODEL=qwen3-coder-plus
export ANTHROPIC_SMALL_FAST_MODEL=qwen3-coder-flash
```

Using iFlow models:
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-dummy
# version 2.x.x
export ANTHROPIC_DEFAULT_OPUS_MODEL=qwen3-max
export ANTHROPIC_DEFAULT_SONNET_MODEL=qwen3-coder-plus
export ANTHROPIC_DEFAULT_HAIKU_MODEL=qwen3-235b-a22b-instruct
# version 1.x.x
export ANTHROPIC_MODEL=qwen3-max
export ANTHROPIC_SMALL_FAST_MODEL=qwen3-235b-a22b-instruct
```

## Codex with multiple account load balancing

Start CLI Proxy API server, and then edit the `~/.codex/config.toml` and `~/.codex/auth.json` files.

config.toml:
```toml
model_provider = "cliproxyapi"
model = "gpt-5-codex" # Or gpt-5, you can also use any of the models that we support.
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

## Run with Docker

Run the following command to login (Gemini OAuth on port 8085): 

```bash
docker run --rm -p 8085:8085 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --login
```

Run the following command to login (OpenAI OAuth on port 1455):

```bash
docker run --rm -p 1455:1455 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --codex-login
```

Run the following command to logi (Claude OAuth on port 54545):

```bash
docker run -rm -p 54545:54545 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --claude-login
```

Run the following command to login (Qwen OAuth):

```bash
docker run -it -rm -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --qwen-login
```

Run the following command to login (iFlow OAuth on port 11451):

```bash
docker run --rm -p 11451:11451 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest /CLIProxyAPI/CLIProxyAPI --iflow-login
```

Run the following command to start the server:

```bash
docker run --rm -p 8317:8317 -v /path/to/your/config.yaml:/CLIProxyAPI/config.yaml -v /path/to/your/auth-dir:/root/.cli-proxy-api eceasy/cli-proxy-api:latest
```

> [!NOTE]
> To use the Git-backed configuration store with Docker, you can pass the `GITSTORE_*` environment variables using the `-e` flag. For example:
>
> ```bash
> docker run --rm -p 8317:8317 \
>   -e GITSTORE_GIT_URL="https://github.com/your/config-repo.git" \
>   -e GITSTORE_GIT_TOKEN="your_personal_access_token" \
>   -v /path/to/your/git-store:/CLIProxyAPI/remote \
>   eceasy/cli-proxy-api:latest
> ```
> In this case, you may not need to mount `config.yaml` or `auth-dir` directly, as they will be managed by the Git store inside the container at the `GITSTORE_LOCAL_PATH` (which defaults to `/CLIProxyAPI` and we are setting it to `/CLIProxyAPI/remote` in this example).

## Run with Docker Compose

1.  Clone the repository and navigate into the directory:
    ```bash
    git clone https://github.com/luispater/CLIProxyAPI.git
    cd CLIProxyAPI
    ```

2.  Prepare the configuration file:
    Create a `config.yaml` file by copying the example and customize it to your needs.
    ```bash
    cp config.example.yaml config.yaml
    ```
    *(Note for Windows users: You can use `copy config.example.yaml config.yaml` in CMD or PowerShell.)*

    To use the Git-backed configuration store, you can add the `GITSTORE_*` environment variables to your `docker-compose.yml` file under the `cli-proxy-api` service definition. For example:
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
    When using the Git store, you may not need to mount `config.yaml` or `auth-dir` directly.

3.  Start the service:
    -   **For most users (recommended):**
        Run the following command to start the service using the pre-built image from Docker Hub. The service will run in the background.
        ```bash
        docker compose up -d
        ```
    -   **For advanced users:**
        If you have modified the source code and need to build a new image, use the interactive helper scripts:
        -   For Windows (PowerShell):
            ```powershell
            .\docker-build.ps1
            ```
        -   For Linux/macOS:
            ```bash
            bash docker-build.sh
            ```
        The script will prompt you to choose how to run the application:
        - **Option 1: Run using Pre-built Image (Recommended)**: Pulls the latest official image from the registry and starts the container. This is the easiest way to get started.
        - **Option 2: Build from Source and Run (For Developers)**: Builds the image from the local source code, tags it as `cli-proxy-api:local`, and then starts the container. This is useful if you are making changes to the source code.

4. To authenticate with providers, run the login command inside the container:
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

5.  To view the server logs:
    ```bash
    docker compose logs -f
    ```

6.  To stop the application:
    ```bash
    docker compose down
    ```

## Management API

see [MANAGEMENT_API.md](MANAGEMENT_API.md)

## SDK Docs

- Usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- Advanced (executors & translators): [docs/sdk-advanced.md](docs/sdk-advanced.md)
- Access: [docs/sdk-access.md](docs/sdk-access.md)
- Watcher: [docs/sdk-watcher.md](docs/sdk-watcher.md)
- Custom Provider Example: `examples/custom-provider`

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Who is with us?

Those projects are based on CLIProxyAPI:

### [vibeproxy](https://github.com/automazeio/vibeproxy)

Native macOS menu bar app to use your Claude Code & ChatGPT subscriptions with AI coding tools - no API keys needed

### [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator)

Browser-based tool to translate SRT subtitles using your Gemini subscription via CLIProxyAPI with automatic validation/error correction - no API keys needed

> [!NOTE]  
> If you developed a project based on CLIProxyAPI, please open a PR to add it to this list.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
