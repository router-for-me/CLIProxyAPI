# 227 服务器发布手册

本文档记录将当前项目发布到 `192.168.15.227` 的标准步骤，目标是下次可以直接按本文快速部署，而不需要重新排查运行目录、编译环境和启动方式。

## 目标环境

- SSH 主机：`root@192.168.15.227`
- 远端代码目录：`/opt/cliproxy`
- 远端监听端口：`18318`
- 远端启动方式：二进制直跑
- 当前启动命令：`./cli-proxy -config config.yaml`

## 部署原则

- 保留远端 `config.yaml`
- 保留远端 `auths/`
- 保留远端 `logs/`
- 仅同步代码和静态资源
- 在远端本机编译 Linux 二进制，不直接上传本地 macOS 二进制

这样做的原因很直接：

- 本地构建出的二进制不能直接在 Linux ARM 机器上运行
- 远端已有实际在用的配置和认证数据，不应被覆盖
- 原地编译和原地替换回滚最简单

## 发布前检查

先在本地仓库执行：

```bash
cd "/Users/joslyn/.config/opencode/worktrees/CLIProxyAPI/circuit-breaker-feature"
git status --short
```

要求工作区干净，避免把未确认改动一起发上去。

再确认远端当前状态：

```bash
ssh -i "$HOME/.ssh/id_ed25519" -o IdentitiesOnly=yes "root@192.168.15.227" '
  set -e
  readlink -f /proc/$(pgrep -f "./cli-proxy -config config.yaml" | head -n 1)/cwd
  readlink -f /proc/$(pgrep -f "./cli-proxy -config config.yaml" | head -n 1)/exe
  (ss -ltnp || netstat -ltnp) 2>/dev/null | grep 18318 || true
'
```

正常情况下应看到：

- 工作目录是 `/opt/cliproxy`
- 可执行文件是 `/opt/cliproxy/cli-proxy`
- `18318` 正在监听

## 一次标准发布

### 1. 同步代码到 227

注意：这里显式排除了远端配置、认证目录和日志目录。

```bash
rsync -avz --delete \
  --exclude '.git' \
  --exclude 'logs' \
  --exclude 'auths' \
  --exclude 'config.yaml' \
  --exclude 'config.yaml.*' \
  --exclude 'cli-proxy-new' \
  -e "ssh -i \"$HOME/.ssh/id_ed25519\" -o IdentitiesOnly=yes" \
  "/Users/joslyn/.config/opencode/worktrees/CLIProxyAPI/circuit-breaker-feature/" \
  "root@192.168.15.227:/opt/cliproxy/"
```

### 2. 在远端编译新二进制

关键点：

- 不要直接使用默认 `go`
- 227 上默认 `go` 可能还是旧版本
- 当前项目要求 `go >= 1.26.0`
- 应显式使用 `/usr/bin/go`

```bash
ssh -i "$HOME/.ssh/id_ed25519" -o IdentitiesOnly=yes "root@192.168.15.227" '
  set -euo pipefail
  cd /opt/cliproxy
  /usr/bin/go version
  /usr/bin/go build -o cli-proxy.new ./cmd/server
  chmod +x cli-proxy.new
  sha256sum cli-proxy.new
'
```

如果这里出现 `go.mod requires go >= 1.26.0`，说明你误用了旧版 `go`。

### 3. 备份旧二进制并切换

```bash
ssh -i "$HOME/.ssh/id_ed25519" -o IdentitiesOnly=yes "root@192.168.15.227" '
  set -euo pipefail
  cd /opt/cliproxy
  ts=$(date +%Y%m%d_%H%M%S)
  [ -f cli-proxy ] && cp -f cli-proxy "cli-proxy.bak.${ts}" || true
  mv -f cli-proxy.new cli-proxy
  chmod +x cli-proxy
'
```

### 4. 重启服务

```bash
ssh -i "$HOME/.ssh/id_ed25519" -o IdentitiesOnly=yes "root@192.168.15.227" '
  set -euo pipefail
  cd /opt/cliproxy
  pkill -f "./cli-proxy -config config.yaml" || true
  sleep 2
  nohup ./cli-proxy -config config.yaml > logs/app.log 2>&1 < /dev/null &
  echo $! > logs/app.pid
  sleep 3
  cat logs/app.pid
  (ss -ltnp || netstat -ltnp) 2>/dev/null | grep 18318 || true
  tail -n 20 logs/app.log
'
```

如果不希望留下多余的父 shell 进程，启动后可以再检查一次：

```bash
ssh -i "$HOME/.ssh/id_ed25519" -o IdentitiesOnly=yes "root@192.168.15.227" '
  ps -fp $(cat /opt/cliproxy/logs/app.pid) || true
  pgrep -af "./cli-proxy -config config.yaml" || true
'
```

理想状态是只剩下真正的 `./cli-proxy -config config.yaml` 主进程。

## 发布后验证

### 1. 模型列表检查

```bash
curl -sS --max-time 20 \
  -H "Authorization: Bearer <YOUR_API_KEY>" \
  "http://192.168.15.227:18318/v1/models"
```

要求返回 `200`，并能看到模型列表 JSON。

### 2. `glm-4.7` Responses 流式检查

```bash
curl -sS -N --max-time 45 \
  -H "Authorization: Bearer <YOUR_API_KEY>" \
  -H "Content-Type: application/json" \
  "http://192.168.15.227:18318/v1/responses" \
  -d '{"model":"glm-4.7","input":"只回复: OK","stream":true}'
```

要求流里至少出现：

- `event: response.output_text.delta`
- `delta: "OK"` 对应正文
- `event: response.completed`

如果只有 `response.created` / `response.in_progress` 而没有正文输出，说明流式链路仍有问题，不能算发布成功。

### 3. `codex cli` 兼容性检查

```bash
OPENAI_API_KEY="<YOUR_API_KEY>" \
codex exec \
  -c model_provider="custom" \
  -c 'model_providers.custom.base_url="http://192.168.15.227:18318/v1"' \
  -m "glm-4.7" \
  "只回复: OK"
```

要求最终输出：

```text
OK
```

注意这里必须使用：

```text
-c model_provider="custom"
-c 'model_providers.custom.base_url="http://192.168.15.227:18318/v1"'
```

不要误写成旧格式的 `-c base_url=...`。

## 常见坑位

### 1. 上传了错误平台的二进制

症状：

- 服务替换后无法启动
- 或远端启动直接报格式错误

原因：

- 本地是 macOS
- 远端是 Linux ARM
- 本地编译产物不能直接拿去覆盖远端运行

正确做法：

- 只同步源码
- 在远端用 `/usr/bin/go build` 本机编译

### 2. 远端默认 `go` 版本太低

症状：

```text
go.mod requires go >= 1.26.0
```

原因：

- shell 里默认的 `go` 不是 `/usr/bin/go`

正确做法：

- 显式用 `/usr/bin/go`

### 3. 把远端 `config.yaml` 覆盖掉了

风险：

- API Key 丢失
- 上游渠道配置被覆盖
- 认证目录失效

正确做法：

- `rsync` 时排除 `config.yaml`、`auths/`、`logs/`

### 4. 只看端口起来，没测真实流式

风险：

- 服务表面正常
- 实际 `/v1/responses` 仍然会卡住、空响应或只返回首包

正确做法：

- 至少跑一次 `glm-4.7` 的流式请求
- 再跑一次 `codex exec`

## 快速回滚

如果新版本有问题，可以直接切回最近备份的二进制：

```bash
ssh -i "$HOME/.ssh/id_ed25519" -o IdentitiesOnly=yes "root@192.168.15.227" '
  set -euo pipefail
  cd /opt/cliproxy
  ls -t cli-proxy.bak.* | head
'
```

选一个最近备份执行回滚：

```bash
ssh -i "$HOME/.ssh/id_ed25519" -o IdentitiesOnly=yes "root@192.168.15.227" '
  set -euo pipefail
  cd /opt/cliproxy
  cp -f cli-proxy.bak.YYYYMMDD_HHMMSS cli-proxy
  chmod +x cli-proxy
  pkill -f "./cli-proxy -config config.yaml" || true
  sleep 2
  nohup ./cli-proxy -config config.yaml > logs/app.log 2>&1 < /dev/null &
  echo $! > logs/app.pid
  sleep 3
  (ss -ltnp || netstat -ltnp) 2>/dev/null | grep 18318 || true
'
```

## 本次已验证通过的事实

- 227 实际部署目录是 `/opt/cliproxy`
- 227 实际运行命令是 `./cli-proxy -config config.yaml`
- 227 实际监听端口是 `18318`
- 227 编译必须显式使用 `/usr/bin/go`
- `GET /v1/models` 已验证正常
- `POST /v1/responses` 使用 `glm-4.7` 流式已验证正常
- 本机 `codex cli` 直连 `http://192.168.15.227:18318/v1` 已验证兼容

## 建议的最短发布路径

如果你只想最快完成一次发布，按这个顺序执行即可：

1. `rsync` 同步源码到 `/opt/cliproxy`
2. 远端用 `/usr/bin/go build -o cli-proxy.new ./cmd/server`
3. 备份旧 `cli-proxy`
4. 用 `cli-proxy.new` 替换正式二进制
5. 重启 `./cli-proxy -config config.yaml`
6. 验证 `/v1/models`
7. 验证 `glm-4.7` 流式
8. 验证 `codex exec`
