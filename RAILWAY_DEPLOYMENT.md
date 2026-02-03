# Railway 部署指南

## 环境变量配置

在 Railway 控制台的 **Variables** 页面添加以下环境变量：

### 必需变量

| 变量名 | 说明 | 示例 |
|--------|------|------|
| `API_KEY` | API 密钥，用于客户端请求认证 | `your-secret-api-key-2024` |
| `MANAGEMENT_PASSWORD` | 管理后台密码 | `admin-password-2024` |

### 可选变量

| 变量名 | 默认值 | 说明 |
|--------|--------|------|
| `PORT` | `8080` | 服务器端口（Railway 自动设置） |
| `DEBUG` | `false` | 是否启用调试日志 (`true`/`false`) |
| `PROXY_URL` | - | 上游代理地址（如 `socks5://host:port`） |
| `ALLOW_REMOTE_MANAGEMENT` | `true` | 允许远程访问管理面板 |
| `REQUEST_RETRY` | `3` | 请求重试次数 |
| `MAX_RETRY_INTERVAL` | `30` | 最大重试间隔（秒） |
| `SWITCH_PROJECT` | `true` | 配额超限时自动切换项目 |
| `SWITCH_PREVIEW_MODEL` | `true` | 配额超限时切换预览模型 |
| `ROUTING_STRATEGY` | `round-robin` | 路由策略（`round-robin` 或 `fill-first`） |
| `WS_AUTH` | `false` | WebSocket API 认证 |
| `USAGE_STATS` | `true` | 启用使用统计（用于管理面板显示额度和记录） |
| `COMMERCIAL_MODE` | `false` | 商业模式（降低内存开销） |
| `FORCE_MODEL_PREFIX` | `false` | 强制模型前缀 |

## 部署步骤

1. **Fork 或导入项目到 Railway**
   - 在 Railway 创建新项目
   - 从 GitHub 仓库导入

2. **配置环境变量**
   ```
   API_KEY=your-secret-api-key-2024
   MANAGEMENT_PASSWORD=admin-password-2024
   DEBUG=false
   ```

3. **部署**
   - Railway 会自动构建 Docker 镜像
   - 使用 `start-cloud.sh` 启动脚本
   - 自动生成 `config.yaml`

4. **访问服务**
   - API 端点：`https://your-app.railway.app/v1/chat/completions`
   - 管理面板：`https://your-app.railway.app/v0/management/panel`

## 使用示例

### API 调用

```bash
curl https://your-app.railway.app/v1/chat/completions \
  -H "Authorization: Bearer your-secret-api-key-2024" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### 管理面板

访问 `https://your-app.railway.app/v0/management/panel`，使用 `MANAGEMENT_PASSWORD` 登录。

## 故障排查

### 问题：无法访问管理面板（返回 404）

**原因：** 未设置 `MANAGEMENT_PASSWORD` 环境变量

**解决：** 在 Railway 控制台添加 `MANAGEMENT_PASSWORD` 变量并重新部署

### 问题：API 调用返回 401 未授权

**原因：** `API_KEY` 错误或未设置

**解决：** 检查请求头中的 `Authorization: Bearer` 值是否与环境变量匹配

### 问题：容器启动失败

**原因：** 缺少必需的环境变量

**解决：** 查看部署日志，确保 `API_KEY` 已设置

## OAuth 配置

如需使用 OAuth 登录（Gemini CLI、Claude、Codex 等），需通过管理面板配置：

1. 访问管理面板
2. 使用对应的登录命令获取认证
3. 凭证将保存在 `/CLIProxyAPI/.cli-proxy-api` 目录

**注意：** Railway 默认不提供持久化存储，容器重启后 OAuth 凭证可能丢失。建议使用 Railway Volumes 或外部存储。

## 持久化存储

为保持 OAuth 凭证在重启后不丢失，建议：

1. **使用 Railway Volumes**（推荐）
   ```
   挂载路径: /CLIProxyAPI/.cli-proxy-api
   ```

2. **使用 API Key 模式**
   - 直接在 `config.yaml` 中配置 API Key
   - 不依赖 OAuth 登录
   - 适合生产环境

## 安全建议

1. ✅ 使用强密码作为 `API_KEY` 和 `MANAGEMENT_PASSWORD`
2. ✅ 定期轮换 API 密钥
3. ✅ 生产环境设置 `DEBUG=false`
4. ✅ 如不需要远程管理，设置 `ALLOW_REMOTE_MANAGEMENT=false`
5. ⚠️ 不要在公共仓库中提交包含密钥的配置文件
