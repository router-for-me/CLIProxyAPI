# 冰糖雪梨

[English](README.md) | 中文

基于 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 二创的开源公益站 — 通过额度分发系统为社区提供免费 AI 模型访问。

## 冰糖雪梨是什么？

冰糖雪梨将 CLIProxyAPI 从个人 CLI 代理工具扩展为**多用户公益额度分发平台**。用户注册账号后，通过兑换码领取额度，即可使用共享凭证池访问 AI 模型（Claude、Gemini、OpenAI 等），所有功能均通过现代化 Web 面板管理。

## 功能特性

### 用户体系
- 账号注册 + 邮箱验证码（SMTP）
- JWT 认证体系（Access Token + Refresh Token）
- 邀请码 & 推荐系统，双向额度奖励
- 每用户独立 API Key，直接对接代理 API

### 额度与凭证管理
- 模型级别额度分配（请求次数 / Token 上限）
- 兑换码模板，用户自助领取
- 共享凭证池 + 加权负载均衡
- 贡献者池 / 公共池 / 独立池三种模式

### 管理后台
- 实时仪表盘：请求趋势、模型分布图表
- 用户管理：封禁/解封、角色分配
- 凭证池健康监控
- 兑换码批量生成 & 模板管理
- 按模型配置额度规则
- 路由引擎调优（策略、权重、健康检查）
- 系统设置（SMTP、OAuth、通用配置）

### 安全防护
- IP 黑白名单（CIDR 格式）
- 全局 & 单 IP 速率限制
- 异常行为检测（高频访问、模型扫描、错误激增）
- 风险标记 + 自动过期
- 完整审计日志
- 验证码恒定时间比对
- 全链路加密安全随机数

### 前端面板
- React 19 + Tailwind CSS + Vite 单页应用
- 响应式设计，移动端侧边栏抽屉
- 中英双语 UI（Zustand i18n 状态管理）
- 挂载于 `/panel` 路径，与代理 API 并行服务

## 技术栈

| 层级 | 技术 |
|------|------|
| 后端 | Go 1.26、Gin、modernc.org/sqlite（无 CGO） |
| 前端 | React 19、TypeScript、Tailwind CSS、Vite、Zustand、Recharts |
| 认证 | JWT HS256（密钥最低 32 字节） |
| 数据库 | SQLite（WAL 模式） |
| 容器 | Docker 多架构（amd64 + arm64） |
| CI/CD | GitHub Actions + GHCR |

## 快速开始

### Docker Compose（推荐）

```bash
# 1. 克隆仓库
git clone https://github.com/hajimilvdou/BTXL.git
cd BTXL

# 2. 复制并编辑配置
cp config.example.yaml config.yaml
# 编辑 config.yaml 填入你的配置

# 3. 启动服务
docker compose up -d
```

服务暴露端口：
- **8317** — API 代理 + 管理面板 (`/panel`)

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `CLI_PROXY_IMAGE` | `ghcr.io/hajimilvdou/btxl:latest` | Docker 镜像地址 |
| `CLI_PROXY_CONFIG_PATH` | `./config.yaml` | 配置文件路径 |
| `CLI_PROXY_AUTH_PATH` | `./auths` | OAuth 凭证目录 |
| `CLI_PROXY_LOG_PATH` | `./logs` | 日志目录 |

### 源码构建

```bash
# 需要 Go 1.26+
go build -o bingtang-xueli ./cmd/server/
./bingtang-xueli
```

## 项目结构

```
internal/
  community/           # 公益站扩展层
    user/              # 认证、JWT、邮箱验证
    quota/             # 额度引擎、风控
    credential/        # 兑换、推荐、模板
    security/          # IP 控制、限流、异常检测、审计
    stats/             # 请求统计
    community.go       # 统一初始化入口
  panel/web/           # React SPA 前端
    src/pages/         # 认证 / 用户 / 管理页面
    src/api/           # TypeScript API 客户端
    src/stores/        # Zustand 状态管理
    src/i18n/          # 中英双语翻译
  db/                  # SQLite 存储 + 迁移脚本
  translator/          # 上游 API 格式翻译（核心层）
```

## 致谢

本项目基于 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（Router-For.ME）二次开发。

## 许可证

本项目基于 MIT 许可证授权 — 详见 [LICENSE](LICENSE) 文件。
