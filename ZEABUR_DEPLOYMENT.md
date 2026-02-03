# Zeabur 部署指南

## 环境变量配置

在 Zeabur 控制台的 **Environment Variables** 页面添加以下环境变量：

### 必需变量

| 变量名 | 说明 | 示例 |
|--------|------|------|
| `API_KEY` | API 密钥，用于客户端请求认证 | `your-secret-api-key-2024` |
| `MANAGEMENT_PASSWORD` | 管理后台密码 | `admin-password-2024` |

### 可选变量

| 变量名 | 默认值 | 说明 |
|--------|--------|------|
| `PORT` | `8080` | 服务器端口（Zeabur 自动设置） |
| `DEBUG` | `false` | 是否启用调试日志 (`true`/`false`) |
| `PROXY_URL` | - | 上游代理地址（如 `socks5://host:port`） |
| `ALLOW_REMOTE_MANAGEMENT` | `true` | 允许远程访问管理面板 |
| `REQUEST_RETRY` | `3` | 请求重试次数 |
| `MAX_RETRY_INTERVAL` | `30` | 最大重试间隔（秒） |
| `SWITCH_PROJECT` | `true` | 配额超限时自动切换项目 |
| `SWITCH_PREVIEW_MODEL` | `true` | 配额超限时切换预览模型 |
| `ROUTING_STRATEGY` | `round-robin` | 路由策略（`round-robin` 或 `fill-first`） |
| `WS_AUTH` | `false` | WebSocket API 认证 |
| `USAGE_STATS` | `true` | 启用使用统计（**重要：必须设为 true 才能在管理面板查看额度和记录**） |
| `COMMERCIAL_MODE` | `false` | 商业模式（降低内存开销） |
| `FORCE_MODEL_PREFIX` | `false` | 强制模型前缀 |

## 部署步骤

### 方法 1：从 GitHub 仓库部署（推荐）

1. **登录 Zeabur 控制台**
   - 访问 https://zeabur.com
   - 使用 GitHub 账号登录

2. **创建新项目**
   - 点击 "Create Project"
   - 选择区域（推荐选择离你最近的区域）

3. **添加服务**
   - 点击 "Add Service"
   - 选择 "Git Repository"
   - 授权并选择你的仓库
   - Zeabur 会自动检测到 `Dockerfile.zeabur`

4. **配置环境变量**
   ```
   API_KEY=your-secret-api-key-2024
   MANAGEMENT_PASSWORD=admin-password-2024
   USAGE_STATS=true
   DEBUG=false
   ```

5. **部署**
   - Zeabur 会自动构建 Docker 镜像
   - 使用 `start-cloud.sh` 启动脚本
   - 自动生成 `config.yaml`

6. **访问服务**
   - Zeabur 会自动分配域名
   - 或在 "Networking" 中绑定自定义域名

### 方法 2：一键部署模板

点击下方按钮一键部署：

[![Deploy on Zeabur](https://zeabur.com/button.svg)](https://zeabur.com/templates)

## 访问服务

部署完成后，你会获得一个类似 `https://your-app.zeabur.app` 的域名。

- **API 端点**：`https://your-app.zeabur.app/v1/chat/completions`
- **管理面板**：`https://your-app.zeabur.app/v0/management/panel`
- **模型列表**：`https://your-app.zeabur.app/v1/models`

## 使用示例

### API 调用

```bash
curl https://your-app.zeabur.app/v1/chat/completions \
  -H "Authorization: Bearer your-secret-api-key-2024" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### 管理面板

1. 访问 `https://your-app.zeabur.app/v0/management/panel`
2. 使用 `MANAGEMENT_PASSWORD` 登录
3. 查看使用统计和额度记录（需确保 `USAGE_STATS=true`）

## 查看使用额度和记录

**重要提示：** 要在管理面板中查看使用额度和记录，必须：

1. **启用使用统计**
   ```
   USAGE_STATS=true
   ```

2. **访问管理面板**
   - 登录后点击 "Usage Statistics" 或 "使用统计"
   - 可查看：
     - 每个模型的调用次数
     - Token 使用量
     - 成本统计
     - 请求历史记录

3. **导出/导入数据**
   - 管理面板支持导出使用数据为 JSON
   - 可在重新部署时导入历史数据

## 故障排查

### 问题：无法访问管理面板（返回 404）

**原因：** 未设置 `MANAGEMENT_PASSWORD` 环境变量

**解决：** 在 Zeabur 控制台添加 `MANAGEMENT_PASSWORD` 变量并重新部署

### 问题：管理面板看不到使用额度和记录

**原因：** `USAGE_STATS` 未启用或设为 `false`

**解决：**
```bash
# 在 Zeabur 控制台添加或修改环境变量
USAGE_STATS=true
```
然后重新部署。

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

### 持久化存储

为保持 OAuth 凭证在重启后不丢失，建议使用 Zeabur Volumes：

1. **创建 Volume**
   - 在 Zeabur 服务页面点击 "Volumes"
   - 添加新 Volume：
     - **Mount Path**: `/CLIProxyAPI/.cli-proxy-api`
     - **Size**: 1GB

2. **重新部署**
   - 凭证现在会持久保存

3. **导出使用数据**（可选）
   - 在管理面板导出使用统计数据
   - 重新部署后可导入恢复

## 更新部署

### 自动部署
Zeabur 检测到 GitHub 仓库更新后自动重新部署

### 手动触发部署
在服务页面点击 "Redeploy"

### 查看部署日志
点击 "Logs" 查看实时日志，确认配置是否正确：

```
=== Cloud Deployment Environment ===
PORT: 8080
API_KEY set: yes
MANAGEMENT_PASSWORD set: yes
DEBUG: false
USAGE_STATS: true  ← 确认已启用
====================================
```

## 性能优化

### 内存优化
```bash
COMMERCIAL_MODE=true  # 高并发场景下减少内存开销
USAGE_STATS=false     # 如不需要统计功能可关闭以节省内存
```

### 请求优化
```bash
REQUEST_RETRY=3           # 重试次数
MAX_RETRY_INTERVAL=30     # 最大重试间隔
ROUTING_STRATEGY=round-robin  # 或 fill-first
```

## 成本优化

- Zeabur 按使用量计费
- 检查 "Usage" 页面监控成本
- 优化建议：
  - 合理配置 `REQUEST_RETRY` 避免无效重试
  - 不需要时禁用 `DEBUG` 模式
  - 根据实际需求调整实例规格

## 自定义域名

1. 在 Zeabur 服务页面点击 "Networking"
2. 点击 "Add Domain"
3. 输入你的域名（如 `api.example.com`）
4. 在域名 DNS 设置中添加 CNAME 记录指向 Zeabur 提供的地址
5. 等待 DNS 生效（通常几分钟）

## 安全建议

1. ✅ 使用强密码作为 `API_KEY` 和 `MANAGEMENT_PASSWORD`
2. ✅ 定期轮换 API 密钥
3. ✅ 生产环境设置 `DEBUG=false`
4. ✅ 如不需要远程管理，设置 `ALLOW_REMOTE_MANAGEMENT=false`
5. ⚠️ 不要在公共仓库中提交包含密钥的配置文件
6. ✅ 使用 HTTPS（Zeabur 自动提供）
7. ✅ 定期检查访问日志

## 对比 Railway 和 Zeabur

| 特性 | Railway | Zeabur |
|------|---------|--------|
| 免费额度 | $5/月 | 有限免费 |
| 部署速度 | 快 | 很快 |
| 自动 HTTPS | ✅ | ✅ |
| 自定义域名 | ✅ | ✅ |
| 持久化存储 | Volumes | Volumes |
| 亚洲节点 | 有限 | 优秀 |
| 价格 | 按量计费 | 按量计费 |

**推荐：**
- 如果在亚洲地区，建议使用 **Zeabur**（延迟更低）
- 如果在美洲/欧洲，建议使用 **Railway**

## 支持

遇到问题？

1. 查看本文档和 [RAILWAY_DEPLOYMENT.md](RAILWAY_DEPLOYMENT.md)（配置相同）
2. 检查 Zeabur 部署日志
3. 在 GitHub 提 Issue：https://github.com/martin98ksJ/CLIProxyAPI/issues

## 常见使用场景

### 场景 1：个人使用
```bash
API_KEY=my-personal-key
MANAGEMENT_PASSWORD=my-admin-pass
USAGE_STATS=true
DEBUG=false
```

### 场景 2：团队共享
```bash
API_KEY=team-shared-key
MANAGEMENT_PASSWORD=strong-admin-password
USAGE_STATS=true
COMMERCIAL_MODE=true  # 高并发优化
ALLOW_REMOTE_MANAGEMENT=true
```

### 场景 3：开发测试
```bash
API_KEY=dev-test-key
MANAGEMENT_PASSWORD=dev-admin
USAGE_STATS=true
DEBUG=true  # 启用调试日志
```
