# 备份功能 - 后端实现

## 功能概述
为 CLIProxyAPI 添加完整的备份/恢复功能，支持多种存储后端，实现配置和认证文件的自动化备份与一键恢复。

## 核心功能

### 多存储后端支持
- ✅ **本地存储**：备份到服务器本地目录
- ✅ **S3 对象存储**：支持 AWS S3 及兼容服务（阿里云 OSS、腾讯云 COS 等）
- ✅ **WebDAV**：支持 Nextcloud、ownCloud 等服务
- ✅ **多后端同时启用**：可同时配置多个存储，备份时自动上传到所有后端

### 智能备份管理
- 自动备份计划（cron 表达式支持）
- 最大备份数限制，自动清理旧备份
- 备份内容：config.yaml + OAuth 认证文件 + 最近日志

### 一键恢复
- 上传 .zip 文件即可恢复
- 自动解压到正确位置
- 热重载配置，无需重启

## 技术亮点

1. **S3 Endpoint 自动清理**
   - 自动移除 `https://` 前缀和路径
   - 兼容各种 S3 服务

2. **多存储容错**
   - 上传到所有启用的存储
   - 单个失败不影响其他
   - 返回成功数量

3. **中文错误消息**
   - 所有 API 响应中文化
   - 用户友好的提示

## API 端点

```
GET    /v0/management/backup/config           # 获取配置
PUT    /v0/management/backup/config           # 更新配置
POST   /v0/management/backup/create           # 创建备份
POST   /v0/management/backup/create?download=true  # 下载备份
GET    /v0/management/backup/list             # 列出备份
GET    /v0/management/backup/download?name=xxx    # 下载指定备份
DELETE /v0/management/backup/delete?name=xxx      # 删除备份
POST   /v0/management/backup/test-connection # 测试连接
POST   /v0/management/backup/restore          # 恢复备份
```

## 配置示例

```yaml
backup:
  enabled: true
  schedule: "@daily"
  storage: "local,s3"  # 多存储支持
  local-dir: "./backups"
  max-backups: 10
  
  s3:
    endpoint: "https://s3.amazonaws.com"
    region: "us-east-1"
    bucket: "my-backups"
    path: "cliproxy/"
    access-key: "xxx"
    secret-key: "xxx"
    use-ssl: true
```

## 核心文件

- `internal/backup/` - 备份核心模块
  - `backup.go` - 备份管理器
  - `storage_local.go` - 本地存储
  - `storage_s3.go` - S3 存储（带 endpoint 清理）
  - `storage_webdav.go` - WebDAV 存储
- `internal/api/handlers/management/backup.go` - API 处理器
- `internal/api/server.go` - 路由注册

## 测试验证

- ✅ 本地存储：创建/下载/恢复
- ✅ S3 存储：连接测试通过
- ✅ 多存储上传：逻辑完整
- ✅ 恢复功能：完整测试通过

## 依赖

- `github.com/minio/minio-go/v7` - S3 客户端
- `github.com/studio-b12/gowebdav` - WebDAV 客户端
- `github.com/robfig/cron/v3` - 定时任务

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
