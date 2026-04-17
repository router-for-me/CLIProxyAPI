# AGENTS.md - CLIProxyAPI 开发指南

## 项目概述
CLIProxyAPI 是一个基于 Go 语言的代理服务器，为 CLI 工具提供 OpenAI/Gemini/Claude/Codex 兼容的 API 接口。支持 OAuth 认证、多账户管理和多种 AI 提供商。

## 构建与测试命令

### 本地开发环境

项目使用**两套配置文件**：
- `config.yaml` — 本地开发（`auth-dir: "./auths"`）
- `config-277.yaml` — 227 服务器生产部署（`auth-dir: "/opt/cliproxy/auths"`）

```bash
# 本地 Go 路径
export PATH="/Users/joslyn/local/go/bin:$PATH"
export GOROOT="/Users/joslyn/local/go"

# 直接运行（默认读取 config.yaml）
go run ./cmd/server

# 或指定配置文件
go run ./cmd/server -config config.yaml

# 编译二进制
go build -o cli-proxy-new ./cmd/server
```

### 构建
```bash
# 构建服务器二进制
go build -o cli-proxy-new ./cmd/server

# 自定义输出名称
go build -o custom-name ./cmd/server
```

### 测试
```bash
# 运行所有测试
go test ./...

# 运行单个测试文件
go test -v ./internal/api/server_test.go

# 运行单个测试函数
go test -v -run TestServerStart ./internal/api/

# 运行特定包的测试
go test -v ./internal/runtime/executor/

# 运行测试并显示覆盖率
go test -cover ./...

# 运行基准测试
go test -bench=. ./internal/sdk/cliproxy/auth/

# 详细输出运行测试
go test -v -count=1 ./...
```

### 代码质量
```bash
# Go 格式化
gofmt -w .

# Go 检查
go vet ./...

# 静态分析 / 导入整理
go run golang.org/x/tools/cmd/goimports@latest -w .

# 刷新模型目录（CI 工作流）
git fetch --depth 1 https://github.com/router-for-me/models.git main
git show FETCH_HEAD:models.json > internal/registry/models/models.json
```

## 代码风格指南

### 导入
- 分组导入：标准库 → 外部包 → internal → sdk
- 使用完整导入路径：`github.com/router-for-me/CLIProxyAPI/v6/internal/...`
- 空白导入用于副作用：`_ "package/path"`（如 init() 注册）
- 包别名避免冲突：`sdkauth "github.com/.../sdk/auth"`

### 格式
- 使用 `gofmt` 或 `goimports` 自动格式化
- 4 空格缩进（Go 标准）
- 最大行长度：约 100 字符（软性规范）
- 相关常量和变量分组

### 类型与声明
- 除非上下文明确，否则使用显式类型
- 除非需要多态，否则优先使用具体类型
- 可变对象使用指针（`*T`），不可变使用值（`T`）
- 错误声明为具体类型：`var ErrNotFound = errors.New("not found")`

### 命名规范
- **文件**：`snake_case.go`（如 `claude_executor.go`、`server_test.go`）
- **函数/变量**：导出用 `PascalCase`，未导出用 `camelCase`
- **常量**：`PascalCase` 或枚举值用 `snake_case`
- **接口**：`PascalCase`，以 `er` 结尾（如 `Executor`、`Handler`）
- **错误变量**：以 `Err` 开头（如 `ErrInvalidToken`）

### 错误处理
- 显式返回错误；避免用 `_` 忽略
- 包装错误并附带上下文：`fmt.Errorf("failed to %s: %w", operation, err)`
- 对已知条件使用哨兵错误
- 返回前在适当级别记录错误

### 包结构
```
cmd/          # 入口点
internal/     # 私有应用代码（不可导入）
sdk/          # 可复用的嵌入 SDK
test/         # 集成测试
```

### 测试模式
- 测试文件在同一包中，以 `_test.go` 结尾
- 对多场景使用表驱动测试
- 测试命名：`Test<函数名>_<场景>`
- 测试工具使用 `t.Helper()`
- 资源清理使用 `defer` 或 `t.Cleanup()`

### 日志
- 使用 `log "github.com/sirupsen/logrus"` 结构化日志
- 包含请求 ID 以便追踪
- 使用适当的日志级别：Error、Warn、Info、Debug

### 并发
- 使用 `sync.WaitGroup` 协调 goroutine
- 优先使用 `context.Context` 取消
- 读多写少使用 `sync.RWMutex`
- 避免跨包共享互斥锁

### 配置
- 用户配置使用 YAML（`config.yaml`）
- 敏感设置使用环境变量
- 提供带验证的合理默认值

### 文档
- 包级文档：`// Package pkgname provides...`
- 导出函数文档：`// FunctionName does...`
- 复杂逻辑的内联注释（优先代码清晰度）
- 注释保持简洁，用英文

### API Handler 模式
- 使用 `handlers.BaseAPIHandler` 作为 API handler 基类
- 请求解析用结构体绑定：`gin.Bind(&requestStruct)`
- 响应模式：JSON 使用 `c.JSON(status, response)`
- 流式响应：使用 `c.Stream()` 配合适当的媒体类型

### Executor 模式
- Executors 实现 `Executor` 接口，包含 `Identifier()`、`PrepareRequest()`、`Stream()` 等方法
- 使用 `cliproxyauth.Auth` 处理凭据
- 通过 `util.ApplyCustomHeadersFromAttrs()` 应用自定义头部
- 处理流式和非流式响应

### 模型别名
- 支持模型别名后缀：`model-name(option)` 语法
- 使用正则或字符串操作解析
- 在模型注册表中注册别名用于路由

### 熔断器
- 实现熔断器模式以提高上游弹性
- 跟踪失败和成功率
- 使用可配置阈值确定打开/半开状态
- 内置对 OpenAI 兼容提供商的熔断器支持

### 加权提供商轮询
- 支持加权提供商调度实现公平负载分配
- 在配置中设置每个提供商的权重
- 改善多账户间的调度公平性

### OAuth 流程
- 处理 OAuth 回调重定向
- 安全存储会话令牌
- 使用 state 参数防止 CSRF 攻击

### WebSocket
- 使用 Gorilla WebSocket 双向通信
- 实现适当的 ping/pong 保活
- 处理重连和清理

### 数据库
- 使用 PostgreSQL via `jackc/pgx/v5` 持久化
- 实现迁移管理模式变更
- 使用连接池提高性能
