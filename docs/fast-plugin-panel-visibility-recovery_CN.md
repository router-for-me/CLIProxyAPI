# Fast 插件网页端可见性恢复说明

## 背景

在主实例容器重建后，管理页面中的插件入口消失，导致网页端看不到 `codex-service-tier` 插件和其中的 `fast` 开关。

## 根因

问题不在后端插件能力，而在前端管理页面资源版本：

1. 后端仍然暴露插件能力头：`X-Cpa-Support-Plugin: true`
2. 主实例重新创建后，`/management.html` 回退到了旧版 fallback 资源
3. 旧版 fallback 页面不包含插件管理入口，因此网页端看不到 `fast` 插件配置
4. 此前仅在容器内手工覆盖 `management.html`，容器重建后该覆盖会丢失

另外，本机 Windows 将 `51121` 划入了保留端口范围，导致恢复默认 `51121:51121` 端口映射会让主容器直接启动失败。

## 本次修复

### 1. 持久化新版管理页面

将本地已验证可用的管理前端页面持久化到仓库：

- 新增挂载：`./static:/CLIProxyAPI/static`
- 持久化文件：`static/management.html`

这样容器重建后，仍会继续提供带插件入口的管理页面，不会再回退到旧版 fallback 页面。

### 2. 保持主实例可启动

由于 `51121` 在当前 Windows 主机上不可绑定：

- 不恢复 `51121:51121` 默认映射
- 增加替代回调端口映射：`52121:52121`
- 需要走 Antigravity OAuth 时，改用：

```bash
--oauth-callback-port 52121
```

这能保证：

- 主实例持续可启动
- Codex / Claude 常用登录链路不受影响
- Antigravity 登录也有可用替代端口

## 验证结果

### 管理页

- 当前服务页面哈希恢复为：
  - `f213772973a16705f9bc9dfe70c5db227e53f0441649235f529742e99fd0888f`
- 不再是旧版 fallback 资源哈希：
  - `0e981a89f03c8bd79510c8636435736e0ccd14125db93e5ca272fb33d7927221`

### 后端插件能力

- 管理接口仍返回：
  - `X-Cpa-Support-Plugin: true`

### 主代理能力

- `POST /v1/messages?beta=true`
- `model=gpt-5.4`
- 返回 `OK`

### 登录链路探测

已验证以下流程仍可正常拉起并等待回调：

- `--codex-login --no-browser`
- `--claude-login --no-browser`
- `--antigravity-login --no-browser --oauth-callback-port 52121`

## 影响范围

- 影响本地 Docker 运行时管理页可见性
- 不修改核心代理协议逻辑
- 不修改插件后端 API 行为
- 仅修复管理页资源持久化与本机回调端口适配

## 建议

如果浏览器仍显示旧页面，请使用：

- `Ctrl + F5`

直接访问插件页：

- `http://127.0.0.1:8317/management.html#/plugins`
