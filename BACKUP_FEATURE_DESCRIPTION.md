# 备份功能开发说明

## 后端仓库 (CLIProxyAPI)

### 功能概述
为 CLIProxyAPI 添加完整的备份/恢复功能，支持多种存储后端（本地、S3、WebDAV），实现配置文件、OAuth 认证文件和日志的自动化备份与一键恢复。

### 核心功能

#### 1. 多存储后端支持
- **本地存储**：备份到服务器本地目录
- **S3 对象存储**：支持 AWS S3 及兼容服务（阿里云 OSS、腾讯云 COS 等）
- **WebDAV**：支持 Nextcloud、ownCloud 等 WebDAV 服务
- **多后端同时启用**：可同时配置多个存储后端，备份时自动上传到所有启用的后端

#### 2. 智能备份管理
- 自动备份计划（支持 cron 表达式：@hourly、@daily、@weekly、@monthly）
- 最大备份数限制，自动清理旧备份
- 备份内容包括：
  - 主配置文件 (config.yaml)
  - OAuth 认证文件 (auths/ 目录)
  - 最近 10 个日志文件

#### 3. 一键恢复
- 上传 .zip 备份文件即可恢复
- 自动解压并恢复到正确位置
- 热重载配置，无需重启服务器

#### 4. API 端点
- `GET /v0/management/backup/config` - 获取备份配置
- `PUT /v0/management/backup/config` - 更新备份配置
- `POST /v0/management/backup/create` - 手动创建备份
- `POST /v0/management/backup/create?download=true` - 下载备份到浏览器
- `GET /v0/management/backup/list` - 列出所有备份
- `GET /v0/management/backup/download?name=xxx` - 下载指定备份
- `DELETE /v0/management/backup/delete?name=xxx` - 删除指定备份
- `POST /v0/management/backup/test-connection` - 测试存储连接
- `POST /v0/management/backup/restore` - 从备份文件恢复

### 技术亮点

1. **S3 Endpoint 自动清理**
   - 自动移除 `https://` 协议前缀
   - 自动移除路径部分
   - 兼容各种 S3 兼容服务

2. **多存储后端容错**
   - 创建备份时上传到所有启用的存储
   - 单个后端失败不影响其他后端
   - 返回成功上传的后端数量

3. **中文错误消息**
   - 所有 API 响应均为中文
   - 用户友好的错误提示

4. **热重载支持**
   - 恢复配置后自动触发配置重载
   - 无需手动重启服务器

### 代码文件

**核心模块** (`internal/backup/`)
- `backup.go` - 备份管理器，负责创建和恢复备份
- `storage_local.go` - 本地存储实现
- `storage_s3.go` - S3 对象存储实现（带 endpoint 清理）
- `storage_webdav.go` - WebDAV 存储实现
- `types.go` - 接口定义和数据结构
- `scheduler.go` - 定时备份调度器

**API 处理器** (`internal/api/handlers/management/`)
- `backup.go` - 备份相关 API 处理，支持多存储上传

**路由注册** (`internal/api/`)
- `server.go` - 注册备份 API 路由

### 配置示例

```yaml
backup:
  enabled: true
  schedule: "@daily"  # 每天备份一次
  storage: "local,s3"  # 同时使用本地和 S3 存储
  local-dir: "./backups"
  max-backups: 10  # 保留最近 10 个备份
  
  s3:
    endpoint: "https://s3.amazonaws.com"  # 自动清理协议前缀
    region: "us-east-1"
    bucket: "my-backups"
    path: "cliproxy/"
    access-key: "YOUR_ACCESS_KEY"
    secret-key: "YOUR_SECRET_KEY"
    use-ssl: true
    
  webdav:
    url: "https://cloud.example.com/remote.php/dav"
    username: "user"
    password: "pass"
    path: "/backups/"
```

### 依赖库
- `github.com/minio/minio-go/v7` - S3 客户端
- `github.com/studio-b12/gowebdav` - WebDAV 客户端
- `github.com/robfig/cron/v3` - 定时任务调度

---

## 前端仓库 (Cli-Proxy-API-Management-Center)

### 功能概述
为管理界面添加完整的备份配置和管理页面，提供直观的 UI 界面管理备份功能。

### 核心功能

#### 1. 可视化配置编辑
- 集成到统一配置系统中
- 与其他配置共享保存/重置机制
- 实时表单验证

#### 2. 备份配置面板
- **基础配置**：启用开关、备份计划、最大备份数
- **本地存储**：配置本地目录，下载备份按钮
- **S3 存储**：Endpoint、Region、Bucket、认证信息配置，测试连接按钮
- **WebDAV 存储**：URL、认证信息配置，测试连接按钮
- 支持同时启用多个存储后端（独立复选框）

#### 3. 手动操作
- **下载备份**：直接下载到浏览器
- **创建备份**：手动触发备份，上传到所有启用的存储
- **测试连接**：验证 S3/WebDAV 配置是否正确
- **恢复备份**：上传 .zip 文件，一键恢复配置

#### 4. 用户体验优化
- 通知系统集成（右上角通知弹窗）
- 确认对话框（非浏览器原生弹窗）
- 中文提示消息
- S3 Endpoint 输入提示（需要 https:// 前缀）

### 技术亮点

1. **统一配置系统集成**
   - 使用 `useVisualConfig` hook
   - YAML 序列化/反序列化
   - 智能 dirty tracking

2. **多存储后端 UI**
   - 独立的启用开关（enableLocal、enableS3、enableWebDAV）
   - 自动转换为逗号分隔字符串（"local,s3,webdav"）
   - 条件显示配置选项

3. **通知系统替换**
   - 替换所有 `alert()` 为 `showNotification()`
   - 替换所有 `confirm()` 为 `showConfirmation()`
   - 统一的 UI 风格

4. **恢复功能优化**
   - 上传后自动 `window.location.reload()`
   - 模拟热重载体验
   - 无需手动刷新

### 代码文件

**组件** (`src/components/config/`)
- `BackupConfigSection.tsx` - 备份配置 UI 组件
- `BackupConfigSection.module.scss` - 样式文件

**API 客户端** (`src/services/api/`)
- `backup.ts` - 备份 API 封装
- `client.ts` - HTTP 客户端

**配置钩子** (`src/hooks/`)
- `useVisualConfig.ts` - 可视化配置管理，包含备份配置的读写逻辑

**类型定义** (`src/types/`)
- `visualConfig.ts` - 备份配置类型定义

**路由** (`src/router/`)
- `MainLayout.tsx` - 添加备份菜单项和图标

### UI 截图功能

- ✅ 备份配置卡片（基础配置、存储类型）
- ✅ 本地存储卡片（目录配置、下载/创建按钮）
- ✅ S3 存储卡片（完整配置表单、测试/创建按钮）
- ✅ WebDAV 存储卡片（完整配置表单、测试/创建按钮）
- ✅ 右上角通知系统（成功/错误提示）
- ✅ 确认对话框（恢复备份确认）

### 依赖更新
- 无新增依赖，完全使用现有 UI 组件库

---

## 完整功能流程

1. **用户在前端配置备份**
   - 启用备份功能
   - 选择存储类型（可多选）
   - 填写存储配置
   - 点击"测试连接"验证
   - 保存配置

2. **自动备份（如果启用定时）**
   - 后端 cron 调度器定时触发
   - 创建 .zip 压缩包
   - 上传到所有启用的存储后端
   - 自动清理旧备份

3. **手动备份**
   - 前端点击"创建备份"
   - 上传到所有配置的存储
   - 显示成功上传的后端数量

4. **下载备份**
   - 直接下载到浏览器
   - 或从存储后端下载

5. **恢复备份**
   - 上传 .zip 文件
   - 后端解压并恢复到正确位置
   - 前端自动刷新应用配置

---

## 贡献总结

### 后端改进
- ✅ 完整的备份/恢复架构
- ✅ 三种存储后端实现
- ✅ 多存储同时上传
- ✅ S3 Endpoint 智能清理
- ✅ 中文错误消息
- ✅ 热重载支持
- ✅ 定时备份调度

### 前端改进
- ✅ 可视化配置编辑
- ✅ 多存储后端 UI
- ✅ 通知系统集成
- ✅ 手动操作按钮
- ✅ 文件上传恢复
- ✅ 中文提示消息

### 测试验证
- ✅ 本地存储：创建/下载/恢复
- ✅ S3 存储：连接测试通过
- ✅ 多存储上传：逻辑实现
- ✅ 恢复功能：完整测试通过
- ✅ UI 交互：所有功能正常

---

## 提交记录

**后端 (CLIProxyAPI)**
- `8dc6b033` - feat: add backup functionality with S3/WebDAV/Local storage support
- `2e290e05` - fix: resolve compilation errors in backup module
- `b972ddaa` - fix: correct persist method usage (returns bool, not error)
- `241dca85` - feat: implement backup restore API
- `b55ecc1b` - fix: resolve backup S3 connection and multi-storage issues
- `d332fdc2` - feat: complete backup feature improvements
- `01ac4ff9` - fix: remove frontend submodule and add to gitignore

**前端 (Cli-Proxy-API-Management-Center)**
- `fb13eca` - fix: add backup section to sidebar navigation
- `daf68d7` - feat: enhance backup config UI with download and restore
- `4cc06f0` - refactor: remove independent save buttons and implement file restore
- `b1504b6` - feat: integrate backup config into visual config system
- `0c41bc3` - feat: major backup UI improvements

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
