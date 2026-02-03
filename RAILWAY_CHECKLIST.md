# Railway 部署检查清单

## 部署前检查

### 1. 必需文件检查

- [x] `Dockerfile` - 主构建文件
- [x] `start-cloud.sh` - 统一启动脚本
- [x] `railway.toml` - Railway 配置
- [x] `config.example.yaml` - 配置示例（容器内参考）
- [x] `RAILWAY_DEPLOYMENT.md` - 部署文档

### 2. Railway 环境变量配置

在 Railway 控制台 **Variables** 页面添加：

#### 必需变量 ⚠️
- [ ] `API_KEY` - 你的 API 密钥
- [ ] `MANAGEMENT_PASSWORD` - 管理后台密码

#### 可选变量
- [ ] `DEBUG=false` - 调试模式
- [ ] `PROXY_URL` - 上游代理
- [ ] `COMMERCIAL_MODE=false` - 商业模式
- [ ] 其他变量见 [RAILWAY_DEPLOYMENT.md](RAILWAY_DEPLOYMENT.md#可选变量)

### 3. Railway 配置验证

#### railway.toml 设置
```toml
[build]
builder = "dockerfile"
dockerfilePath = "./Dockerfile"  # ✅ 使用主 Dockerfile

[deploy]
healthcheckPath = "/"
```

#### Dockerfile 验证
- 使用 `start-cloud.sh` 启动脚本
- 暴露端口由 Railway 自动处理（通过 PORT 环境变量）

### 4. 启动脚本验证

`start-cloud.sh` 应该：
- [x] 读取 PORT 环境变量（Railway 自动设置）
- [x] 验证 API_KEY 必需
- [x] 警告 MANAGEMENT_PASSWORD 未设置
- [x] 生成 config.yaml
- [x] 启动 CLIProxyAPI

## 部署后验证

### 1. 检查部署日志

在 Railway 控制台查看 **Deployments** 日志，应该看到：

```
=== Cloud Deployment Environment ===
PORT: 8080
API_KEY set: yes
MANAGEMENT_PASSWORD set: yes
DEBUG: false
====================================

Generated config.yaml with:
  - Port: 8080
  - API Key: your-key***
  - Management: enabled

Starting CLIProxyAPI...
```

### 2. 测试 API 端点

```bash
# 健康检查
curl https://your-app.railway.app/

# API 调用测试
curl https://your-app.railway.app/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### 3. 访问管理面板

访问 `https://your-app.railway.app/v0/management/panel`

- 使用 `MANAGEMENT_PASSWORD` 登录
- 如果返回 404，检查 `MANAGEMENT_PASSWORD` 是否设置

### 4. 检查模型列表

```bash
curl https://your-app.railway.app/v1/models \
  -H "Authorization: Bearer YOUR_API_KEY"
```

## 常见问题排查

### 问题 1: 容器启动失败

**日志显示**：`ERROR: API_KEY environment variable is required`

**解决**：在 Railway 控制台添加 `API_KEY` 环境变量

### 问题 2: 管理面板 404

**原因**：`MANAGEMENT_PASSWORD` 未设置

**解决**：在 Railway 控制台添加 `MANAGEMENT_PASSWORD` 环境变量

### 问题 3: Port binding 错误

**日志显示**：`bind: address already in use`

**检查**：
1. Railway 是否正确设置 PORT 环境变量（自动）
2. `start-cloud.sh` 是否正确读取 PORT

### 问题 4: OAuth 凭证丢失

**原因**：Railway 默认无持久化存储，容器重启后 `/CLIProxyAPI/.cli-proxy-api` 内容丢失

**解决**：
1. 使用 Railway Volumes 持久化存储
2. 或使用 API Key 模式（不依赖 OAuth）

## 生产环境建议

### 安全配置
- [x] 使用强密码作为 `API_KEY`
- [x] 使用强密码作为 `MANAGEMENT_PASSWORD`
- [x] 设置 `DEBUG=false`
- [ ] 定期轮换密钥
- [ ] 如需限制管理访问，设置 `ALLOW_REMOTE_MANAGEMENT=false`

### 性能优化
- [ ] 设置 `COMMERCIAL_MODE=true`（高并发场景）
- [ ] 配置 `REQUEST_RETRY=3`
- [ ] 根据需要调整 `ROUTING_STRATEGY`

### 监控
- [ ] 监控 Railway 的 Metrics（CPU、内存、网络）
- [ ] 配置 Railway 的 Alerts
- [ ] 定期检查部署日志

## 持久化存储配置

如需保存 OAuth 凭证，在 Railway 项目中：

1. 进入 **Settings** → **Volumes**
2. 添加新 Volume：
   - **Mount Path**: `/CLIProxyAPI/.cli-proxy-api`
   - **Size**: 1GB

3. 重新部署

## 更新部署

### 自动部署
Railway 检测到 GitHub 仓库更新后自动重新部署

### 手动部署
在 Railway 控制台点击 **Deploy** → **Redeploy**

### 回滚
在 **Deployments** 页面选择历史版本 → **Rollback**

## 成本优化

- Railway 按使用量计费
- 检查 **Usage** 页面监控成本
- 优化：
  - 设置 `COMMERCIAL_MODE=true` 减少内存使用
  - 合理配置 `REQUEST_RETRY` 避免无效重试
  - 设置 `USAGE_STATS=false` 减少内存开销

## 支持

遇到问题？

1. 查看 [RAILWAY_DEPLOYMENT.md](RAILWAY_DEPLOYMENT.md) 完整文档
2. 检查 Railway 部署日志
3. 在 GitHub 提 Issue：https://github.com/martin98ksJ/CLIProxyAPI/issues
