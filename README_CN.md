# CLIProxyAPI Plus

[English](README.md) | 中文

这是 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 Plus 版本，在原有基础上增加了第三方供应商的支持。

所有的第三方供应商支持都由第三方社区维护者提供，CLIProxyAPI 不提供技术支持。如需取得支持，请与对应的社区维护者联系。

该 Plus 版本的主线功能与主线功能强制同步。

## 与主线版本版本差异

[![bigmodel.cn](https://assets.router-for.me/chinese-5.png)](https://www.bigmodel.cn/claude-code?ic=RRVJPB5SII)

## 新增功能 (Plus 增强版)

GLM CODING PLAN 是专为AI编码打造的订阅套餐，每月最低仅需20元，即可在十余款主流AI编码工具如 Claude Code、Cline、Roo Code 中畅享智谱旗舰模型GLM-4.7（受限于算力，目前仅限Pro用户开放），为开发者提供顶尖的编码体验。

智谱AI为本产品提供了特别优惠，使用以下链接购买可以享受九折优惠：https://www.bigmodel.cn/claude-code?ic=RRVJPB5SII

### 命令行登录

> **注意：** 由于 AWS Cognito 限制，Google/GitHub 登录不可用于第三方应用。

**AWS Builder ID**（推荐）：

```bash
# 设备码流程
./CLIProxyAPI --kiro-aws-login

# 授权码流程
./CLIProxyAPI --kiro-aws-authcode
```

**从 Kiro IDE 导入令牌：**

```bash
./CLIProxyAPI --kiro-import
```

获取令牌步骤：

1. 打开 Kiro IDE，使用 Google（或 GitHub）登录
2. 找到令牌文件：`~/.kiro/kiro-auth-token.json`
3. 运行：`./CLIProxyAPI --kiro-import`

**AWS IAM Identity Center (IDC)：**

```bash
./CLIProxyAPI --kiro-idc-login --kiro-idc-start-url https://d-xxxxxxxxxx.awsapps.com/start

# 指定区域
./CLIProxyAPI --kiro-idc-login --kiro-idc-start-url https://d-xxxxxxxxxx.awsapps.com/start --kiro-idc-region us-west-2
```

**附加参数：**

| 参数 | 说明 |
|------|------|
| `--no-browser` | 不自动打开浏览器，打印 URL |
| `--no-incognito` | 使用已有浏览器会话（Kiro 默认使用无痕模式），适用于需要已登录浏览器会话的企业 SSO 场景 |
| `--kiro-idc-start-url` | IDC Start URL（`--kiro-idc-login` 必需） |
| `--kiro-idc-region` | IDC 区域（默认：`us-east-1`） |
| `--kiro-idc-flow` | IDC 流程类型：`authcode`（默认）或 `device` |

### 网页端 OAuth 登录

访问 Kiro OAuth 网页认证界面：

```
http://your-server:8080/v0/oauth/kiro
```

提供基于浏览器的 Kiro (AWS CodeWhisperer) OAuth 认证流程，支持：
- AWS Builder ID 登录
- AWS Identity Center (IDC) 登录
- 从 Kiro IDE 导入令牌

## Docker 快速部署

### 一键部署

```bash
# 创建部署目录
mkdir -p ~/cli-proxy && cd ~/cli-proxy

# 创建 docker-compose.yml
cat > docker-compose.yml << 'EOF'
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api-plus:latest
    container_name: cli-proxy-api-plus
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
      - ./logs:/CLIProxyAPI/logs
    restart: unless-stopped
EOF

# 下载示例配置
curl -o config.yaml https://raw.githubusercontent.com/router-for-me/CLIProxyAPIPlus/main/config.example.yaml

# 拉取并启动
docker compose pull && docker compose up -d
```

### 配置说明

启动前请编辑 `config.yaml`：

```yaml
# 基本配置示例
server:
  port: 8317

# 在此添加你的供应商配置
```

### 更新到最新版本

```bash
cd ~/cli-proxy
docker compose pull && docker compose up -d
```

## 贡献

该项目仅接受第三方供应商支持的 Pull Request。任何非第三方供应商支持的 Pull Request 都将被拒绝。

如果需要提交任何非第三方供应商支持的 Pull Request，请提交到[主线](https://github.com/router-for-me/CLIProxyAPI)版本。

## 许可证

此项目根据 MIT 许可证授权 - 有关详细信息，请参阅 [LICENSE](LICENSE) 文件。