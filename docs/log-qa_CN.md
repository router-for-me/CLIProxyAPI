# 本地未上传日志质检服务（log-qa）

`log-qa` 是**独立进程**。它只读扫描 `logs-root/<key_name>/*.log`（尚未被 uploader 删除的源日志），按 **session_id** 聚合后套用甲方三条规则，把结果写到独立 `work-dir`，并在 Management 面板展示。

它**不会**：

- 获取 `log-uploader` 的锁
- 读写 uploader 的 `state.json` / `audit.jsonl`
- 删除、移动或修改任何源 `.log`
- 拦截上传

## 1. 配置

```bash
cp log-qa.example.yaml log-qa.yaml
```

关键项：

| 字段 | 含义 |
|------|------|
| `logs-root` | 与 uploader 相同的源日志目录（只读） |
| `work-dir` | 报告与 `qa-state.json`（独立目录） |
| `schedule.interval` | 默认 `30m` |
| `schedule.initial-delay` | 默认 `12m`，错开 uploader `:05` |
| `rules.min-prompt-rounds` | 默认 `4`（即有效轮次 > 3） |
| `scan.max-files-per-run` | **`0` = 全量**（不限制文件数）；`>0` 时本轮最多扫 N 个新文件 |
| `scan.max-bytes-per-run` | **`0` = 全量**；`>0` 时本轮最多扫描该字节数 |
| `scan.max-file-size` | **`0` = 不限制单文件大小** |

面板「部分扫描 = 是」表示本轮触发了 files/bytes 上限。全量扫描请将上述三项设为 `0`。

## 2. 运行

```bash
# 单次
go run ./cmd/log-qa --config log-qa.yaml --once

# 常驻
go run ./cmd/log-qa --config log-qa.yaml
```

构建：

```bash
go build -o bin/log-qa ./cmd/log-qa
./bin/log-qa --config log-qa.yaml
```

## 3. 规则（session 最终快照）

**Session 快照**：同一 `session_id` 下通常有多条请求日志（多轮 turn）。质检不会对每一条分别出“是否合格”，而是先选出 **一条代表请求**，再用该条上的指标判定整个 session。选取规则：

1. **排除 `request_kind` 含 `compact` 的 compaction 轮次**（它们常把整段历史塞进 `input`，体积最大，但有效提问会被整单过滤成 0）
2. 在剩余请求中取 **input 最长**，其次 **时间最晚**
3. 若 session **只剩** compaction 日志（普通 turn 已被 uploader 删掉），则回退用最长的 compaction 作快照

对该快照再判定（与 log-uploader `session-gate` 对齐，另加展示项）：

1. **有效 user prompt 数 ≥ min-prompt-rounds（默认 4）**  
   排除 title/summary、IDE context、`environment_context`
2. **session_id 非空**（真实 session，非 path 伪造）
3. **至少 1 次工具调用**（`function_call` / `custom_tool_call` 等）
4. **工具 call/output 按 call_id 成对**
5. **最新非 title 轮 RESPONSE 不以 tool_call 结尾**
6. **无完全相同的 assistant 文本重复**（仅 QA 展示；uploader 门禁不含此项）

报告字段 `upload_eligibility`：`eligible` / `hold` / `orphan`，便于对照「为何尚未上传」。

同一 session 下的 subagent thread **会合并**进同一条样本。

面板上「失败会话数（按原因）」里的数字是**该原因失败的会话个数**，不是轮次阈值。  
例如「有效提问轮次不足：8 个」= 有 8 个 session 未达到 ≥4 轮，**不是**“不足 8 轮就失败”。

运行批次（`run_id`）按配置时区（默认 **Asia/Shanghai / 北京时间**）命名，形如 `2026-07-23T17-56-55+0800`。

## 4. 报告位置

```text
work-dir/
  qa-state.json
  qa.lock          # 运行中存在
  reports/
    latest.json
    <run_id>/
      summary.json
      session_qa.jsonl
      fail_sessions.jsonl
      request_qa.jsonl
```

## 5. Management 查看

主 API 需能**只读**同一 `work-dir`（以及能读到 `log-qa.yaml` 以解析路径）。

登录 `/management.html` 后，右侧会出现 **LOG QA** 按钮，可查看：

- 合格率、会话数、失败原因分布
- 失败 session 列表（可筛选）
- **标题**：优先取 Codex 标题生成轮次（`request_kind` 含 title）响应中的短标题；若标题日志已被 uploader 删掉，则回退为首条有效用户提问预览，便于在 Codex 里对照

API（需 management 鉴权）：

- `GET /v0/management/log-qa/status`（含 `running`：是否质检中）
- `GET /v0/management/log-qa/summary`
- `GET /v0/management/log-qa/sessions`
- `GET /v0/management/log-qa/sessions/logs?session_id=...`（下载失败会话完整源日志 zip）
- `GET /v0/management/log-qa/runs`
- `POST /v0/management/log-qa/run`（手动触发一轮质检；进行中返回 409）

Management 面板支持 **立即质检**（质检中按钮置灰）与失败会话的 **下载日志**。

## 6. 与 uploader 并存

| 进程 | 职责 |
|------|------|
| log-uploader | 打包上传删源 |
| log-qa | 预检未上传日志 |
| API server | 展示报告 |

源日志被 uploader 删除后，自然不再出现在下次 QA 中。这是**上传前预检**，不是 TOS 终验。

## 7. Docker 一键部署

仓库已把 `log-qa` 打进同一镜像，并在 `docker-compose.yml` 中作为独立服务启动。

### 服务器上

```bash
# 代码目录内
chmod +x docker-build.sh
./docker-build.sh
```

脚本会：

1. 若缺失则从 example 生成 `config.yaml` / `log-uploader.yaml` / `log-qa.yaml` / `.env`
2. 创建 `logs/`、`auths/`
3. 选择 1 拉预构建镜像，或 2 本地源码构建
4. `docker compose up -d` 启动三个服务：`cli-proxy-api`、`log-uploader`、`log-qa`

### 选择说明

| 选项 | 场景 |
|------|------|
| **1 预构建镜像** | 镜像 registry 已包含新版 `./log-qa` 二进制时 |
| **2 源码构建（推荐刚合入 log-qa 后）** | 确保镜像内有 `log-qa`；`CLI_PROXY_IMAGE=cli-proxy-api:local` |

### 部署后检查

```bash
docker compose ps
docker compose logs -f log-qa

# 立即跑一轮质检（不等 30 分钟）
docker compose exec log-qa ./log-qa -config /CLIProxyAPI/log-qa.yaml -once

# 看报告
ls logs/log-qa/reports/
```

Management：`http://<服务器IP>:8317/management.html` → 右侧 **LOG QA**。

### 路径约定（Docker）

| 宿主机 | 容器内 |
|--------|--------|
| `./logs` | `/CLIProxyAPI/logs` |
| `./log-qa.yaml` | `/CLIProxyAPI/log-qa.yaml` |
| 源日志 | `/CLIProxyAPI/logs/keys`（`log-qa.yaml` 的 `logs-root`） |
| QA 报告 | `/CLIProxyAPI/logs/log-qa`（`work-dir`） |

请保证 **log-uploader 的 `logs-root` 与 log-qa 的 `logs-root` 指向同一目录**，否则预检与上传集合会对不齐。

### 仅重启 QA

```bash
docker compose up -d log-qa --force-recreate
```
