# VERIFICATION: Fix zhipu Provider Auth File Format

## 问题描述

在验证迁移到 Claude Agent SDK 的过程中，发现 CLIProxyAPI 无法通过 53355 端口正确调用 Python Bridge，转发到 GLM-4.6 模型。测试返回错误：
- `auth_not_found: no auth available`
- `unknown provider for model glm-4.6`

## 根因分析

通过并行搜索源代码，定位到问题出现在认证文件解析逻辑中：

1. **文件解析位置**：`/home/adam/projects/CLIProxyAPI/internal/watcher/watcher.go:1010-1014`
   ```go
   var metadata map[string]any
   if err = json.Unmarshal(data, &metadata); err != nil {
       continue
   }
   t, _ := metadata["type"].(string)  // 系统期望 type 字段
   if t == "" {
       continue
   }
   provider := strings.ToLower(t)     // 使用 type 作为 provider
   ```

2. **认证文件格式错误**：
   - **错误格式**：使用 `provider` 字段
     ```json
     {
       "provider": "zhipu",
       "label": "zhipu-final-test",
       "attributes": {
         "api_key": "...",
         "base_url": "..."
       }
     }
     ```
   - **正确格式**：使用 `type` 字段
     ```json
     {
       "type": "zhipu",
       "api_key": "...",
       "base_url": "https://open.bigmodel.cn/api/anthropic",
       "proxy_url": ""
     }
     ```

3. **影响范围**：
   - 认证管理器无法找到匹配的认证 (`pickNext()` 返回 `auth_not_found`)
   - zhipu executor 未注册（即使调用了 `ensureZhipuExecutorRegistered()`）
   - GLM 模型未正确关联到认证

## 修复方案

### 1. 修复认证文件格式
**文件**：`/home/adam/.cli-proxy-api/zhipu-auth.json`

**修改前**：
```json
{
  "id": "zhipu-final-test",
  "provider": "zhipu",
  "label": "zhipu-final-test",
  "status": "active",
  "attributes": {
    "api_key": "2daae61b47e0420a80de9d3941ce9f30.Wu1lFPXoHBYaCkxv",
    "base_url": "https://open.bigmodel.cn/api/anthropic",
    "source": "zhipu"
  }
}
```

**修改后**：
```json
{
  "type": "zhipu",
  "api_key": "2daae61b47e0420a80de9d3941ce9d3941ce9f30.Wu1lFPXoHBYaCkxv",
  "base_url": "https://open.bigmodel.cn/api/anthropic",
  "proxy_url": ""
}
```

### 2. 重启服务
```bash
pkill -9 cli-proxy-api && sleep 2 && ./cli-proxy-api
```

**验证输出**：
```
CLIProxyAPI Version: dev, Commit: none, BuiltAt: unknown
API server started successfully
server clients and configuration updated: 1 clients (1 auth files + 0 GL API keys + 0 Claude API keys + 0 Codex keys + 0 OpenAI-compat + 0 Packycode)
```

## 验证结果

### 1. 模型注册验证
```bash
curl -s http://127.0.0.1:53355/v1/models -H 'Authorization: Bearer sk-dummy'
```
**结果**：
- ✅ glm-4.5 (owned_by: zhipu)
- ✅ glm-4.6 (owned_by: zhipu)

### 2. Provider列表验证
```bash
curl -s http://127.0.0.1:53355/v0/management/providers -H 'Authorization: Bearer adamcf'
```
**结果**：
```json
{
  "providers": ["copilot", "zhipu"]
}
```

### 3. 非流式调用测试
```bash
curl -s -X POST http://127.0.0.1:53355/v1/chat/completions \
  -H 'Authorization: Bearer sk-dummy' \
  -H 'Content-Type: application/json' \
  -d '{"model": "glm-4.6", "messages": [{"role": "user", "content": "你好，请用中文回复"}], "stream": false}'
```

**结果**：
```json
{
  "id": "chatcmpl-bridge",
  "object": "chat.completion",
  "created": 34956,
  "model": "glm-4.6",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "你好！我会用中文与您交流。请问您需要什么帮助？"
      },
      "finish_reason": "stop"
    }
  ]
}
```

### 4. 流式调用测试
```bash
curl -N -s -X POST http://127.0.0.1:53355/v1/chat/completions \
  -H 'Authorization: Bearer sk-dummy' \
  -H 'Content-Type: application/json' \
  -d '{"model": "glm-4.6", "messages": [{"role": "user", "content": "写一首诗"}], "stream": true}'
```

**结果**（SSE格式）：
```
data: {"id": "chatcmpl-bridge", "object": "chat.completion.chunk", "model": "glm-4.6", "choices": [{"delta": {"content": "..."}, "finish_reason": null}]}

data: [DONE]
```

### 5. 官方测试验证
```bash
E2E_SERVER_URL=http://127.0.0.1:53355 \
E2E_ACCESS_KEY=sk-dummy \
go test -v -run TestServerHTTP_Zhipu_GLM46_ChatCompletions ./tests/server_http_zhipu_test.go
```

**结果**：
```
=== RUN   TestServerHTTP_Zhipu_GLM46_ChatCompletions
--- PASS: TestServerHTTP_Zhipu_GLM46_ChatCompletions (2.26s)
PASS
```

## 架构验证

### 完整调用链
```
客户端 (端口 53355)
    ↓ POST /v1/chat/completions
CLIProxyAPI (Go)
    ↓ 解析请求：model=glm-4.6 → provider=zhipu
认证管理器 (coreManager)
    ↓ pickNext() 查找匹配的认证
    ↓ 找到：zhipu-auth.json (type=zhipu)
ZhipuExecutor
    ↓ 转发到 Python Bridge
Python Agent Bridge (端口 35331)
    ↓ 环境变量：ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN
    ↓ 调用 GLM-4.6
智谱AI (GLM-4.6)
    ↓ 返回响应
    ↓ SSE 格式流式/非流式
客户端
```

### 端口状态
- ✅ 53355: CLIProxyAPI 主服务 (PID: 227909)
- ✅ 35331: Python Agent Bridge (PID: 218726)
- ✅ 认证文件: `/home/adam/.cli-proxy-api/zhipu-auth.json` (格式正确)

## OpenSpec Diff

### 修改文件
**`openspec/changes/2025-10-23-migrate-zhipu-to-claude-agent-sdk/VERIFICATION.md`**
- 新增：完整验证报告
- 包含：问题描述、根因分析、修复方案、测试结果

### 任务清单更新
**`tasks.md` 第 11 项**
- ✅ 修复 zhipu provider 和模型注册问题
- ✅ 验证修复：providers 列表包含 "zhipu"，模型列表包含 glm-4.5 和 glm-4.6
- ✅ 验证 Bridge 连通性：直接调用 http://127.0.0.1:53355 成功返回 GLM-4.6 响应

## 结论

本次修复成功解决了认证文件格式不匹配的问题，验证了完整调用链的正常工作：

1. ✅ **认证机制正常**：zhipu 认证文件被正确解析和注册
2. ✅ **模型注册正常**：glm-4.5 和 glm-4.6 正确关联到 zhipu provider
3. ✅ **执行器正常**：zhipu executor 成功注册并转发到 Python Bridge
4. ✅ **桥接正常**：Python Agent Bridge 成功接收请求并调用 GLM-4.6
5. ✅ **响应正常**：非流式和流式调用均返回正确格式的响应
6. ✅ **测试通过**：官方测试 `TestServerHTTP_Zhipu_GLM46_ChatCompletions` PASS

**系统状态**：迁移完成，所有功能正常运行，Bridge 可以通过 53355 端口成功调用。

---

**修复日期**: 2025-10-25 23:58
**修复人员**: Claude Code
**测试环境**: Linux 6.6.87.2-microsoft-standard-WSL2
**版本**: CLIProxyAPI Version: dev
