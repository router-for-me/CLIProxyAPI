# CLIProxyAPI Magisk Module

将 CLIProxyAPI 作为 Magisk 模块在 Android 设备上运行。

## 功能特性

- ✅ 在 Android 上运行 CLIProxyAPI 代理服务
- ✅ 开机自动启动服务
- ✅ 支持 arm64/arm/amd64 架构
- ✅ 完整的服务管理（启动/停止/重启/状态）
- ✅ 配置文件持久化
- ✅ 日志记录

## 系统要求

- Android 8.0+
- 已安装 Magisk v24+
- arm64 / arm / amd64 架构

## 安装方法

### 方法一：下载预编译模块

1. 前往 [Releases](https://github.com/router-for-me/CLIProxyAPI/releases) 页面
2. 下载适合您设备架构的模块：
   - `cliproxyapi-arm64.zip` - 大多数现代手机
   - `cliproxyapi-arm.zip` - 旧款 32 位手机
   - `cliproxyapi-amd64.zip` - 模拟器/x86 设备
3. 在 Magisk Manager 中选择「从本地安装」
4. 选择下载的 zip 文件并安装
5. 重启设备

### 方法二：自行编译

```bash
# 克隆仓库
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI

# 构建并打包
cd magisk
chmod +x build-android.sh
./build-android.sh all

# 生成的模块位于 magisk/bin/ 目录
```

## 配置

### 配置文件位置

```
/data/adb/modules/cliproxyapi/config.yaml
```

### 基本配置

编辑 `config.yaml` 文件：

```yaml
# 服务器端口
port: 8317

# API 密钥（请修改）
api-keys:
  - "sk-your-secret-key-here"

# Gemini API 密钥
gemini-api-key:
  - api-key: "AIzaSy..."
    prefix: "gemini"

# Claude API 密钥
claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "claude"
```

### 认证文件位置

```
/data/adb/modules/cliproxyapi/auths/
```

## 服务管理

通过 ADB 或终端模拟器：

```bash
# 启动服务
/data/adb/modules/cliproxyapi/service.sh start

# 停止服务
/data/adb/modules/cliproxyapi/service.sh stop

# 重启服务
/data/adb/modules/cliproxyapi/service.sh restart

# 查看状态
/data/adb/modules/cliproxyapi/service.sh status
```

## 使用方法

服务启动后，API 端点为：

```
http://127.0.0.1:8317
```

### OpenAI 兼容端点

```bash
# Chat Completions
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Claude 兼容端点

```bash
# Messages
curl http://127.0.0.1:8317/v1/messages \
  -H "x-api-key: sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Gemini 兼容端点

```bash
# Generate Content
curl "http://127.0.0.1:8317/v1beta/models/gemini-2.5-flash:generateContent" \
  -H "x-api-key: sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Hello!"}]}]
  }'
```

## OAuth 登录

由于 Android 上没有浏览器环境，OAuth 登录需要在其他设备上完成：

### 方法一：复制认证文件

1. 在电脑上完成 OAuth 登录
2. 复制认证文件到手机：
   ```bash
   adb push ~/.cli-proxy-api/gemini_token.json /data/adb/modules/cliproxyapi/auths/
   ```

### 方法二：手动配置 API 密钥

直接在 `config.yaml` 中配置 API 密钥，无需 OAuth。

## 日志

日志文件位置：

```
/data/adb/modules/cliproxyapi/logs/
├── service.log      # 服务日志
├── access.log       # 访问日志
└── error.log        # 错误日志
```

查看日志：

```bash
# 实时查看服务日志
tail -f /data/adb/modules/cliproxyapi/logs/service.log
```

## 故障排除

### 服务无法启动

1. 检查二进制文件权限：
   ```bash
   chmod 755 /data/adb/modules/cliproxyapi/cli-proxy-api
   ```

2. 检查配置文件语法：
   ```bash
   cat /data/adb/modules/cliproxyapi/config.yaml
   ```

3. 查看错误日志：
   ```bash
   cat /data/adb/modules/cliproxyapi/logs/service.log
   ```

### 端口被占用

修改 `config.yaml` 中的端口号：

```yaml
port: 8318
```

### SELinux 问题

Magisk 模块通常不受 SELinux 限制，如果遇到问题：

```bash
# 临时设置为宽容模式
setenforce 0
```

## 卸载

1. 在 Magisk Manager 中找到 CLIProxyAPI 模块
2. 点击删除
3. 重启设备

或通过命令行：

```bash
rm -rf /data/adb/modules/cliproxyapi
```

## 架构支持

| 架构 | 设备类型 | 模块文件 |
|------|----------|----------|
| arm64 | 大多数现代手机 | cliproxyapi-arm64.zip |
| arm | 旧款 32 位手机 | cliproxyapi-arm.zip |
| amd64 | 模拟器/x86 设备 | cliproxyapi-amd64.zip |

## 相关链接

- [CLIProxyAPI 主项目](https://github.com/router-for-me/CLIProxyAPI)
- [用户手册](https://help.router-for.me/cn/)
- [问题反馈](https://github.com/router-for-me/CLIProxyAPI/issues)

## 许可证

MIT License
