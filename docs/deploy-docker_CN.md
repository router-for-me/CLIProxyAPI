# Docker 部署指南（API + 日志上传 + Log QA）

本文说明如何在服务器上一键部署：

| 服务 | 容器名 | 作用 |
|------|--------|------|
| 主 API | `cli-proxy-api` | 代理请求 + Management 管理页 |
| 日志上传 | `cli-proxy-api-uploader` | 按小时打包上传 TOS |
| 日志质检 | `cli-proxy-api-log-qa` | 只读检查本地未上传日志，写报告，不拦截上传 |

---

## 0. 前置条件

服务器需要：

- Docker + Docker Compose v2（`docker compose version` 可用）
- 磁盘：日志目录建议预留充足空间（未上传积压可能到数十 GB）
- 网络：能拉基础镜像 / 访问 TOS（若启用上传）
- 端口：默认 `8317`（及 compose 中其它端口）对运维网段开放

本机检查：

```bash
docker version
docker compose version
```

---

## 1. 获取代码

```bash
# 示例：进入已有仓库目录
cd /path/to/CLIProxyAPI
git pull   # 确保包含 log-qa 与 docker-compose 更新
```

确认关键文件存在：

```bash
ls docker-build.sh docker-compose.yml Dockerfile \
   log-uploader.example.yaml log-qa.example.yaml config.example.yaml
```

---

## 2. 一键部署（推荐）

```bash
chmod +x docker-build.sh
./docker-build.sh
```

### 交互选项

| 选项 | 说明 | 何时用 |
|------|------|--------|
| **1** 预构建镜像 | `docker compose up`，不本地编译 | 远端镜像**已包含** `./log-qa` 二进制 |
| **2** 源码构建并启动 | 本地 `docker compose build` 再启动 | **刚更新 log-qa 后首选** |

脚本自动：

1. 缺失时从 example 生成  
   - `config.yaml`  
   - `log-uploader.yaml`  
   - `log-qa.yaml`  
   - `.env`  
2. 创建 `logs/`、`auths/`、`plugins/`（插件商店安装的二进制会落在这里，需持久化）  
3. 启动三个服务  

插件相关注意：

- `config.yaml` 中必须 `plugins.enabled: true`，且对应插件 `plugins.configs.<id>.enabled: true`  
- 商店安装**不会**自动打开单插件；若配置里是 `enabled: false`，重启后不会加载  
- compose 已挂载 `./plugins` → `/CLIProxyAPI/plugins`，避免容器重建后插件文件丢失

### 首次部署建议选 2

```text
./docker-build.sh
→ 输入 2
```

---

## 3. 部署后必改配置

### 3.1 对齐日志目录（最重要）

`log-uploader` 与 `log-qa` 必须扫**同一批源日志**。

Docker 默认（`log-qa.example.yaml`）：

```yaml
# log-qa.yaml
logs-root: logs/keys
work-dir: logs/log-qa
```

对应容器路径：

| 含义 | 容器内路径 | 宿主机（默认） |
|------|------------|----------------|
| 源 `.log` | `/CLIProxyAPI/logs/keys` | `./logs/keys` |
| QA 报告 | `/CLIProxyAPI/logs/log-qa` | `./logs/log-qa` |

请打开 `log-uploader.yaml`，确认 `logs-root` 也是同一目录（例如也是 `logs/keys` 或绝对路径 `/CLIProxyAPI/logs/keys`）。

若 uploader 仍是 `auths/logs/keys`，二选一：

- 把 uploader 改成 `logs/keys`，或  
- 把 log-qa 改成与 uploader 相同路径  

改完后：

```bash
docker compose up -d log-uploader log-qa --force-recreate
```

### 3.2 上传 TOS（若要用 uploader）

编辑 `log-uploader.yaml`：

```yaml
upload:
  enabled: true   # 验证过 dry-run 后再开
```

在 `.env` 中配置密钥（名称以你 `log-uploader.yaml` 中 `access-key-id-env` 为准）：

```bash
VOLC_TOS_ACCESS_KEY_ID=你的AK
VOLC_TOS_SECRET_ACCESS_KEY=你的SK
```

重启 uploader：

```bash
docker compose up -d log-uploader --force-recreate
```

### 3.3 主服务 config.yaml

按业务修改 `config.yaml`（端口、api-keys、management 密码等），然后：

```bash
docker compose up -d cli-proxy-api --force-recreate
```

---

## 4. 验证是否部署成功

### 4.1 容器状态

```bash
docker compose ps
```

期望三个服务都是 `Up`：

- `cli-proxy-api`
- `cli-proxy-api-uploader`
- `cli-proxy-api-log-qa`

### 4.2 看 QA 日志

```bash
docker compose logs --tail=50 log-qa
```

无持续 error 即可。默认会先等 `initial-delay`（约 12 分钟）再跑第一轮。

### 4.3 立刻跑一轮质检（推荐）

```bash
docker compose exec log-qa ./log-qa -config /CLIProxyAPI/log-qa.yaml -once
```

成功后应有：

```bash
ls logs/log-qa/reports/
# 有 latest.json 与某个 run_id 目录
cat logs/log-qa/reports/latest.json
```

### 4.4 Management 页面

浏览器打开：

```text
http://<服务器IP>:8317/management.html
```

1. 使用 Management 密码登录  
2. 右侧点击 **LOG QA**  
3. 应能看到合格率 / 失败原因 / session 列表  

若提示无报告：先执行上面的 `-once`。

### 4.5 上传诊断（可选）

```bash
bash diag-uploader.sh
```

---

## 5. 日常运维命令

```bash
# 查看状态
docker compose ps

# 跟踪日志
docker compose logs -f log-qa
docker compose logs -f log-uploader
docker compose logs -f cli-proxy-api

# 仅重启 QA
docker compose up -d log-qa --force-recreate

# 仅重启上传
docker compose up -d log-uploader --force-recreate

# 更新代码后完整重建（推荐流程）
git pull
./docker-build.sh    # 选 2

# 停止全部
docker compose down
```

---

## 6. 目录与数据说明

```text
项目目录/
  config.yaml              # 主服务配置（勿提交密钥）
  log-uploader.yaml        # 上传配置
  log-qa.yaml              # 质检配置
  .env                     # 环境变量 / TOS 密钥
  logs/
    keys/                  # 源请求日志 *.log（API 写入）
    log-qa/
      reports/             # 质检报告（Management 读取）
      qa-state.json
    .log-uploader-work/    # 上传状态（若配置在 logs 下）
  auths/                   # 认证等
```

**注意：**

- log-qa **只读**源日志，**不删除**  
- 上传成功后 uploader 可能删除源 `.log`，之后 QA 自然检不到这些文件  
- QA 是**上传前预检**，不是 TOS 终验替代品  

---

## 7. 常见问题

### Q1：`log-qa` 容器一直重启 / 找不到二进制

原因：预构建镜像过旧，没有 `log-qa`。

处理：

```bash
./docker-build.sh   # 选 2 源码构建
```

### Q2：Management 打开 LOG QA 没有数据

1. 是否执行过 `-once` 或等过定时任务  
2. `logs/log-qa/reports/latest.json` 是否存在  
3. `cli-proxy-api` 是否挂载了同一 `./logs` 与 `./log-qa.yaml`  
4. `log-qa.yaml` 的 `work-dir` 是否为 `logs/log-qa`  

### Q3：质检结果和预期差很多

- 确认 uploader 与 log-qa 的 `logs-root` 一致  
- 有效轮次规则默认 ≥ 4（>3），一轮长任务会判不合格  
- 按 `session_id` 聚合，子 agent 会合并进同一 session  

### Q4：`qa.lock` 导致无法启动

进程异常退出可能残留锁：

```bash
# 确认没有正在跑的 log-qa 后
rm -f logs/log-qa/qa.lock
docker compose up -d log-qa
```

### Q5：改配置不生效

配置是挂载文件，改完需 recreate：

```bash
docker compose up -d --force-recreate log-qa
# 或
docker compose up -d --force-recreate
```

---

## 8. 安全建议

- 不要把 `log-uploader.yaml` / `log-qa.yaml` / `.env` / 真实 `config.yaml` 提交到公开仓库  
- Management 密码与 TOS 密钥仅放服务器环境  
- 日志与报告目录权限限制为运维账号可读  
- 管理端口 `8317` 建议仅内网或 VPN 访问  

---

## 9. 最小检查清单（上线勾选）

- [ ] `./docker-build.sh` 选 2 构建成功  
- [ ] `docker compose ps` 三个服务 Up  
- [ ] `log-uploader.yaml` 与 `log-qa.yaml` 的 `logs-root` 一致  
- [ ] `docker compose exec log-qa ./log-qa -config /CLIProxyAPI/log-qa.yaml -once` 成功  
- [ ] `logs/log-qa/reports/latest.json` 存在  
- [ ] Management → LOG QA 能看到汇总  
- [ ] （若启用上传）TOS 凭证正确，`diag-uploader.sh` 无 error  

---

## 10. 一页速查

```bash
# 部署
./docker-build.sh          # 选 2

# 对齐路径后重启
docker compose up -d --force-recreate

# 立刻质检
docker compose exec log-qa ./log-qa -config /CLIProxyAPI/log-qa.yaml -once

# 看结果
ls logs/log-qa/reports/
# 浏览器: http://IP:8317/management.html → LOG QA
```
