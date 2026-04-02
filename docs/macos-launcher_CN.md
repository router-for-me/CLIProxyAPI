# macOS 本地启动器方案

本文总结了一套适合在 macOS 上本地运行 CLIProxyAPI 的启动与更新方案。目标不是绑定某一份私有脚本，而是整理出一套长期稳定、易维护的接线方式。

## 目标

- 将 CLIProxyAPI 放在一个固定且可写的目录中。
- 允许用户从 Finder、Launchpad、Spotlight 或 `/Applications` 一键启动。
- 服务启动后自动打开内置管理 Web UI。
- 允许启动器同时关闭本地代理进程和对应的 Web UI 窗口。
- 将代码更新、运行状态和 `.app` 包本身解耦。

## 推荐目录结构

建议把真实运行目录放在 `.app` 之外，例如：

```text
~/CLIProxyAPI/
  bin/
  auths/
  logs/
  temp/
  config.yaml
```

推荐职责划分：

- `config.yaml`：本地运行配置
- `auths/`：OAuth 和 provider 认证文件
- `logs/`：运行日志
- `bin/`：本地构建产物与辅助脚本
- `temp/`：PID、浏览器 profile、临时控制文件

这样可以避免把运行态文件塞进 app 包，也避免和 git 跟踪文件混在一起。

## 内置 Web UI 应使用 `/management.html`

CLIProxyAPI 的内置管理面板入口是：

```text
http://127.0.0.1:8317/management.html
```

不应把 `/` 当成管理面板入口。根路径只是一个轻量级的 API 状态页。

若要保证 Web UI 可用，通常需要：

- 配置 `remote-management.secret-key`
- 保持 `remote-management.disable-control-panel` 为 `false`
- 在纯本机场景下让服务只监听 localhost

## 让 `.app` 成为“薄启动器”

macOS 下的 `.app` 更适合做“薄启动器”，而不是第二份独立安装。

推荐做法：

- `.app` 只负责调用真实项目目录里的脚本
- 真正的二进制和配置都放在固定 checkout 目录中
- `.app` 不内嵌一份会逐渐过期的服务端二进制

这样做的好处：

- 更新只需要改真实项目目录
- `.app` 天然跟随最新构建结果
- 配置、日志、认证和更新状态都集中在一个地方

## 启动流程

一套稳健的启动流程通常是：

1. 先判断 CLIProxyAPI 是否已经在运行。
2. 如果没运行，则以脱离当前 shell 的方式启动本地二进制。
3. 等待 HTTP 健康检查通过。
4. 等待 `/management.html` 可以访问。
5. 打开 Web UI。

推荐检查点：

- 服务健康检查：`http://127.0.0.1:8317/`
- 管理面板检查：`http://127.0.0.1:8317/management.html`

## 使用专属浏览器 Profile 打开 Web UI

如果启动器还需要可靠地执行“关闭 Web UI”，不要把管理面板随意开在用户现有的普通浏览器标签页中。

更稳的方式是使用专属 profile 或 app-style window。例如 Chrome：

```bash
open -na "Google Chrome" --args \
  --user-data-dir="$HOME/CLIProxyAPI/temp/webui-browser-profile" \
  --app="http://127.0.0.1:8317/management.html"
```

这样做的好处：

- 启动器可以只关闭这组 Web UI 浏览器进程
- 不会误伤用户正常浏览器窗口
- 管理面板更接近桌面控制台体验

## 关闭流程

再次点击启动器时，一个实用的 toggle 流程可以是：

1. 检测代理服务是否已经在运行。
2. 如果在运行，则给用户两个动作：
   - 再次打开 Web UI
   - 关闭本地服务
3. 如果选择关闭：
   - 先结束专属 Web UI 浏览器 profile 进程
   - 再停止 CLIProxyAPI 进程
   - 必要时清理陈旧 PID 文件

这样 `.app` 既能作为启动入口，也能作为控制入口，而不必强依赖后台守护器。

## 不要在 `.app` 内做更新

不要把更新逻辑设计成直接替换 app 包里的二进制。

更好的模式是：

1. 将真实 git checkout 固定在一个目录，例如 `~/CLIProxyAPI`
2. 使用本地更新脚本负责：
   - 检查远端分支或 release
   - 快进更新代码
   - 重新构建二进制
   - 如有需要，重启当前服务
3. `.app` 始终只指向这一个固定运行目录

这种方式非常适合挂接到 `topgrade`、`up` 之类已有的终端更新流里。

## 更新时的保护措施

- 如果仓库跟踪文件存在本地修改，则跳过自动更新。
- 对 `git fetch` / `git ls-remote` 设置超时。
- 先构建临时二进制，校验通过后再替换正式二进制。
- 将配置、认证和日志继续放在 git 跟踪范围之外。

## 总结

一套稳定的 macOS 本地方案通常包括：

- 固定的真实运行目录
- 只负责调度的 `.app` 启动器
- 以 `management.html` 作为内置管理界面入口
- 通过专属浏览器 profile 精准管理 Web UI 生命周期
- 将更新接入用户已有的终端更新工作流

这套方案简单、可恢复，也能避免“app 包里藏着一份过期二进制”的常见问题。
