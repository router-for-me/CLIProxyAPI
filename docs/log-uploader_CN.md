# 请求日志定时上传服务

`log-uploader` 是一个独立进程。它读取 `auths/logs/keys/<key_name>/*.log`，将同一个完整小时内所有用户、所有模型的日志合并成一个 JSONL 文件，再使用 Zstandard 流式压缩并上传到火山引擎 TOS。

正常情况下，每个有日志的完整小时只产生一个文件名标签为 `codex56sol` 的归档。`codex56sol` 只是固定的归档命名标签，不表示模型筛选：归档仍合并该小时内全部 `key_name` 和全部模型。每个原始 `.log` 对应 JSONL 中的一行，行内仍保留真实的 `key_name` 和 `model`，因此可以在同一个归档中按用户或模型检索。转换过程不会在磁盘上生成一份完整的未压缩 JSONL 临时文件。

## 0. Session 门禁（可选）

`session-gate` 在打包上传前按 **session_id** 做质量过滤（默认 `enabled: false`，灰度后再打开）。

五条规则（全部满足才上传）：

1. 有效提问轮次 ≥ `min-prompt-rounds`（默认 4，即 >3；排除 title/summary/compact 等）
2. `session_id` 非空
3. 会话最后一轮 RESPONSE 不以 tool_call 结尾
4. 工具 `call_id` 的 call/output 成对
5. 全链路至少一次工具调用

不满足的会话 **Hold** 在本地（log-qa 仍可扫描展示）；达标后整 session 再上传。  
迟传使用 **eligibility-hour**：并入最晚文件所属、已 settle 且未 seal 的小时正常单包，**不做 residual 补包**。  
Hold 超过 `max-hold-age`（默认 48h 无新活动）或 `max-absolute-age`（默认 7d）后本地删除，永不上传。

## 1. 创建配置

从示例复制一份本地配置：

```bash
cp log-uploader.example.yaml log-uploader.yaml
```

示例已预置以下 TOS 信息：

- 原生 Endpoint：`https://tos-cn-beijing.volces.com`（本服务使用）
- S3 Endpoint：`https://tos-s3-cn-beijing.volces.com`（本服务不使用，配置后会拒绝启动）
- Region：`cn-beijing`
- Bucket：`llm-d1`
- Bucket 域名：`llm-d1.tos-cn-beijing.volces.com`（仅供核对，服务上传时使用 Endpoint 与 Bucket）

`log-uploader.yaml` 已加入 `.gitignore`。Access Key 不写入 YAML，而是放入项目根目录的 `.env` 或进程环境变量：

```dotenv
VOLC_TOS_ACCESS_KEY_ID=待配置
VOLC_TOS_SECRET_ACCESS_KEY=待配置
# 只有 STS 临时凭证需要，必须与临时 AK/SK 配套：
# VOLC_TOS_SESSION_TOKEN=待配置
```

准备正式上传时，将 `upload.enabled` 改为 `true`。在 Access Key 尚未配置时，请保持为 `false`。本服务使用 TOS 原生 SDK，只向目标前缀写入对象，不列举或下载远端对象。上传时禁止覆盖已有对象，并把压缩文件的 SHA-256 写入自定义元数据 `cliproxy-sha256`。只有上传返回“对象已存在”时，服务才会用 `HeadObject` 查询该对象的大小和 SHA-256：完全一致则按幂等恢复，不一致则停止该小时的清理并保留本地日志。

## 2. 每小时单包与启动扫描

示例配置如下：

```yaml
schedule:
  interval: 1h
  run-on-start: true
  settle-delay: 5m
```

服务的时间规则为：

- 启动时立即扫描所有尚未处理的历史日志，并按小时从旧到新分别归档。
- 归档小时按最终 `.log` 文件的完成时间（文件修改时间）计算；日志内的原始请求时间仍保存在 JSONL 的 `timestamp` 字段中。
- 当前完成时间所在的小时始终跳过，不会打入一个不完整的包。
- 一个小时结束并经过 `settle-delay` 后才允许处理；示例会在每小时的 `:05` 运行。
- 最近仍有修改的文件会继续等待，避免读取尚未写完的日志。
- API 主服务先把日志完整写入同目录临时文件，执行 `Sync` 和 `Close` 后再原子发布为 `.log`；发布不会覆盖已有同名日志，上传器也只扫描已经完整发布的 `.log`。
- 某个小时没有日志时，不生成空归档。

例如服务在 `2026-07-15 02:05` 启动，会处理 `01:00–01:59` 以及更早尚未处理的小时，但不会处理 `02:00` 这一当前小时。

如果请求在 `01:59` 开始、到 `02:10` 才完成，它会进入 `02` 点归档，并在 `03:05` 处理。这样跨小时长请求不会在已经封口的 `01` 点后再产生第二个压缩包。

## 3. 先做 dry-run

在配置正式上传前，先执行一次本地测试：

```bash
go run ./cmd/log-uploader --config log-uploader.yaml --once --dry-run
```

dry-run 会完成扫描、JSONL 转换、Zstandard 压缩并写入审计日志，但不会上传、记录“已上传”状态，也不会删除任何源日志或本地归档。即使示例配置启用了成功后清理，`--dry-run` 仍然不会执行清理。

检查以下位置：

- 本地测试归档：`auths/log-uploader/dry-run-archives/<年>/<月>/<日>/`
- 审计日志：`auths/log-uploader/audit.jsonl`

首次 dry-run 会扫描全部可处理的历史小时，可能产生较多本地归档。执行前请确认磁盘空间充足；验证完成后，可以手工清理这些 dry-run 归档。

## 4. 启动正式服务

Access Key 配好并将 `upload.enabled` 设为 `true` 后，可以先执行一次上传：

```bash
go run ./cmd/log-uploader --config log-uploader.yaml --once
```

随后启动常驻服务：

```bash
go run ./cmd/log-uploader --config log-uploader.yaml
```

生产环境建议构建独立二进制，再交给 systemd、supervisord 或其他进程管理器保持运行：

```bash
go build -o bin/log-uploader ./cmd/log-uploader
./bin/log-uploader --config log-uploader.yaml
```

服务启动时会同时获取两层进程锁；常驻服务、`--once` 和旧状态迁移命令都遵循同一规则：

- `work-dir/service.lock` 保护该工作目录及其中的状态文件，强制同一个 `work-dir` 只能由一个实例使用。
- 共享资源锁保护“规范化后的 `logs-root` + 上传目标”。锁文件位于规范化 `logs-root` 的父目录 `.log-uploader-locks/<hash>.lock`；`<hash>` 由规范化日志目录以及 Provider、Endpoint、Region、Bucket、`object-prefix` 组成的目标标识派生，不包含 Access Key 或 Secret Key。

因此，相同 `logs-root` 和上传目标的实例即使使用不同 `work-dir`，也会自动争用同一个共享资源锁；后启动的实例会在扫描日志前失败退出，不再要求它们为了互斥而共享 `work-dir`。路径中的 `..`、符号链接等别名会先规范化，不能用来绕过共享锁。不同 `logs-root` 或不同上传目标使用不同的共享资源锁。运行账户必须有权在规范化 `logs-root` 的父目录中创建和访问 `.log-uploader-locks`。相对的 `logs-root` 和 `work-dir` 会以配置文件所在目录为基准解析，因此进程管理器不依赖特定启动目录。

### 4.1 将服务器本地历史统计补传到 Supabase

配置好 `supabase.ingest-url` 和 `LOG_STATS_INGEST_TOKEN` 后，可以先做只读预检：

```bash
./bin/log-uploader --config log-uploader.yaml --sync-supabase-history --dry-run
```

确认输出的 JSON 汇总正确后执行正式补传：

```bash
./bin/log-uploader --config log-uploader.yaml --sync-supabase-history
```

该模式只读取服务器本地的以下可信数据：

- `work-dir/history/*.jsonl`
- `work-dir/audit.jsonl`
- schema-v3 `work-dir/state.json`

它不会创建 TOS 客户端，不会列举、下载或读取 TOS 对象，也不会读取原始 `.log` 或 `.jsonl.zst` 内容。补传前会完整校验所有本地账本和上传状态；任何一条记录不一致时，在发出第一个 Supabase 请求前即失败。

历史审计可以精确提供每个 `key_name` 的源文件数量和源日志字节数，以及批次级 JSONL/压缩字节数，但不能精确反推出每个人分别产生的 JSONL 字节数。因此历史事件使用 `usage_precision: batch_only`，不会按比例虚构每 Key JSONL 数值；正常的新上传事件仍提供精确的每 Key JSONL 字节。

补传使用确定性事件 ID、持久化 outbox 和按 Supabase 目标隔离的 checkpoint。相同目标重复执行不会重复计数；更换 ingest URL 后会按新目标重新补传。历史补传只把 schema-v3 `state.json` 对应 `hours` 条目中耐久保存的 `supabase_event_id` 视为正常实时流程已接管的标记；仅在 `audit.jsonl` 中出现该 ID 的批次仍会作为历史事件补传。若审计记录与耐久标记中的 ID 不一致，预检会在网络请求前失败。命令输出只包含数量和字节汇总，不包含用户名、对象路径、日志正文或 token。

## 5. JSONL、文件名与对象路径

每个原始 `.log` 对应 JSONL 中的一行。记录包含可检索元数据和经过安全处理的完整日志内容：

```json
{"schema_version":1,"key_name":"panda","model":"gpt-5.6-sol","source_file":"panda/v1-responses-example.log","source_size_bytes":12345,"timestamp":"2026-07-15T01:00:00+08:00","sensitive_headers_redacted":true,"raw_log":"已对认证头和 Cookie 脱敏的完整原始日志内容"}
```

上传版本会把 `Authorization`、`X-Api-Key`、`Cookie` 等敏感 HTTP 头替换为 `[REDACTED]`，避免把用户 API Key 写进对象存储；其他请求、响应和正文仍完整保留。本地原始 `.log` 在删除前不会因转换而被修改。

归档文件名格式为：

```text
YYYY-MM-DD-HH-codex56sol-压缩前JSONL大小.jsonl.zst
```

例如：

```text
2026-07-15-01-codex56sol-2G.jsonl.zst
```

其中 `2G` 是压缩前 JSONL 文件大小，不是 `.zst` 文件大小；容量按 1024 进制计算。`codex56sol` 始终只是固定文件名标签，归档中的每一行仍保存对应日志的真实 `model` 值。

旧配置中的 `model-aliases` 仍可被读取以保持升级兼容，但不会影响固定的 `codex56sol` 文件名；新配置无需设置该字段。升级前已经生成或上传的 `all-models` 对象保持原名称，服务不会自动重命名或移动这些历史对象；新归档才使用 `codex56sol` 标签。

本地归档路径为：

```text
auths/log-uploader/archives/<年>/<月>/<日>/<归档文件名>
```

默认 TOS 对象路径为：

```text
cliproxy-logs/<年>/<月>/<日>/<归档文件名>
```

完整示例：

```text
cliproxy-logs/2026/07/15/2026-07-15-01-codex56sol-2G.jsonl.zst
```

`key_name` 不再出现在目录或文件名中，而是保存在每条 JSONL 记录和本地审计统计中。

每个完成小时最多生成一个 `codex56sol` 对象，服务不会生成 `-part2`、`-part3`。该标签不会改变“全部 key、全部模型合并为一个小时包”的规则。小时成功上传后会在 `state.json` 中封口；如果后来出现修改时间被人为回填到已封口小时的日志，服务会把它保留在本地并写入 `late_logs_retained` 审计，而不会创建第二个对象或删除该日志。

远端同名对象存在时，服务使用 `HeadObject` 比较压缩文件大小与 `cliproxy-sha256`。完全一致表示上一次上传可能已经成功，只是本地状态尚未来得及落盘，此时会恢复状态并继续安全清理；任何不一致或无法查询都会失败关闭并保留源日志。严格单包还依赖上述共享资源锁：所有指向同一规范化 `logs-root` 和上传目标的进程会争用同一个 `.log-uploader-locks/<hash>.lock`，即使它们使用不同 `work-dir` 也不能并发处理。

为了覆盖“远端 PUT 已成功、但进程在本地提交状态前退出”的窗口，正式上传前会先在 `state.json` 的 `prepared_hours` 中持久化固定对象名、归档 SHA-256 和精确源文件清单。重启只能恢复这个固定批次，不能因源集合变化创建另一个对象。TOS 凭证若没有 `HeadObject` 权限，正常首次 PUT 不受影响；但发生同名冲突时会安全停住并保留本地数据，直到补充权限或人工处理。

## 6. 每个 key_name 的本地审计

审计文件位于：

```text
auths/log-uploader/audit.jsonl
```

每个小时批次的审计记录包含总文件数、总原始字节数、JSONL 大小、压缩后大小、对象路径、删除文件数和状态。`key_names` 是按用户名统计的对象，每个用户名下包含 `source_count`、`source_bytes`，以及按模型细分的 `models`：

```json
{
  "hour": "2026-07-15T01:00:00+08:00",
  "source_count": 18,
  "source_bytes": 2048000,
  "key_names": {
    "panda": {
      "source_count": 12,
      "source_bytes": 1536000,
      "models": {
        "gpt-5.6-sol": {"source_count": 12, "source_bytes": 1536000}
      }
    }
  }
}
```

常见状态为 `dry_run`、`uploaded` 或 `failed`。本地清理暂时失败时会记录 `uploaded_delete_pending`、`uploaded_archive_delete_pending` 或 `uploaded_cleanup_pending`；人为回填到已封口小时的日志会记录 `late_logs_retained` 并保留文件。

启用成功后清理时，服务会先持久化一条 `uploaded_cleanup_pending`，确认写入后才删除本地数据，清理结束再追加最终状态。因此一个正常上传并清理的小时批次通常会有两条审计记录，这不是重复上传。

## 7. 成功后删除源日志与本地归档

示例配置按正式运行要求启用双清理：

```yaml
retention:
  delete-source-after-upload: true
  keep-local-archives: false
```

清理顺序为：

1. 生成 `.jsonl.zst`，计算归档及每个原始 `.log` 的 SHA-256。
2. 原子写入 `prepared_hours`，固定本小时的源清单和唯一对象名。
3. 上传到 TOS，并写入上传审计和已封口状态。
4. 重新校验源文件大小、修改时间和 SHA-256，将匹配文件原子移入 `delete-pending` 后删除。
5. 删除已上传的本地 `.jsonl.zst`，再写入最终清理审计。

上传失败、上传成功审计写入失败或本地状态落盘失败时，源日志不会被删除。删除失败会保留待清理状态，并在下一轮或服务重启后重试。源文件的大小、修改时间或 SHA-256 任一变化都会保留新版本，交给下一轮重新归档。

成功上传过的对象名、已封口小时和待清理信息记录在：

```text
auths/log-uploader/state.json
```

不要在服务运行时手工修改或删除 `state.json`。所有上传请求均禁止覆盖已有对象，并保存压缩文件的 SHA-256 元数据。

`state.json` 当前使用 schema v3，并绑定规范化后的 Endpoint、Region、Bucket、对象前缀及归档策略。配置目标或策略发生变化时，服务会在任何删除之前拒绝启动；请使用新的 `work-dir` 或完成显式迁移，不能把旧状态直接用于另一个 Bucket。

新版本首次以非 dry-run 模式读取受信任的 schema v2 状态时，会在锁内自动执行一次单向的 v2 → v3 迁移，并在继续处理前原子保存。迁移后的 schema v3 状态不能被旧版二进制读取；如需回退旧版，必须同时恢复迁移前备份的 schema v2 状态，不能让旧版直接使用已迁移的 `state.json`。

首次启用 Supabase 前，必须先确认旧状态中的 `prepared_hours` 积压已经处理完。可以先让旧版完成这些批次；如果已经升级，则保持 `supabase.enabled: false`，用新版本执行一次 `./log-uploader --config log-uploader.yaml --once`，让它恢复并结算旧的 prepared 批次，确认不再有积压后再启用 Supabase。旧 prepared 批次可能没有 Supabase 所需的精确用量元数据，直接启用会被安全拒绝。

旧版无目标绑定的状态必须停服务后显式迁移。迁移命令会先核对旧状态、审计、manifest 和本地归档，全部通过后才原子替换状态：

```bash
./log-uploader --config log-uploader.yaml \
  --migrate-legacy-manifest /path/to/hourly-objects-sha256.jsonl \
  --migrate-legacy-archives /path/to/verified-archives
```

如果当前 TOS 策略没有 `HeadObject` 权限，但本地归档已逐个校验、manifest 来自成功上传审计，可显式增加 `--migrate-legacy-trust-local`。该模式只适合一次性迁移；建议仍为正式凭证补充目标前缀的只读对象元数据权限，以便进程崩溃后的同名冲突能够自动校验恢复。

开启双清理后，TOS 对象将成为日志正文的长期副本。如果尚未验证 Bucket 的权限、持久性和恢复流程，可以在首次正式验证时暂时设置 `delete-source-after-upload: false`、`keep-local-archives: true`，验证完成后再恢复示例中的双清理配置。

## 8. 备份与恢复

### 需要备份的内容

建议定期备份以下运行元数据：

- `auths/log-uploader/state.json`：对象命名、上传和待清理状态。
- `auths/log-uploader/audit.jsonl`：每小时以及每个 `key_name` 的产出和处理结果。
- `log-uploader.yaml`：非敏感运行配置。

为了获得一致的 `state.json` 和 `audit.jsonl` 备份，建议暂停服务后复制，或使用支持原子快照的文件系统。Access Key 和 Secret Access Key 不应写入普通备份；请通过密钥管理系统单独保存和恢复，并按需轮换。

启用双清理后，远端 TOS 对象是日志正文的唯一长期副本。建议为 Bucket 配置适合业务的版本控制、生命周期、服务端加密和异地备份或复制策略。不要设置会在所需保留期之前删除归档的生命周期规则。

### 下载并验证归档

恢复日志时，把对象下载到 `logs-root` 之外的独立恢复目录，不要直接放入 `auths/logs/keys`。下载后按以下顺序验证：

1. 读取对象自定义元数据 `cliproxy-sha256`。
2. 计算本地 `.jsonl.zst` 的 SHA-256，与元数据比较。
3. 使用 `zstd -t archive.jsonl.zst` 检查压缩包完整性。
4. 使用 `zstd -d archive.jsonl.zst -o archive.jsonl` 解压，或通过 `zstdcat` 流式读取。
5. 逐行解析 JSONL；`source_file` 是原相对路径，`raw_log` 是对应的日志内容。

恢复出的 `raw_log` 已对认证头和 Cookie 做不可逆脱敏，因此无法恢复被替换掉的密钥值。这是预期的安全行为。

### 恢复服务运行状态

发生主机故障时：

1. 先停止或不要启动 `log-uploader`。
2. 恢复 `log-uploader.yaml`、`state.json` 和 `audit.jsonl` 到原相对位置。
3. 通过环境变量或密钥管理系统恢复 AK/SK，并确认 Bucket、Region 和 Endpoint 正确。
4. 检查文件权限后启动服务；`run-on-start: true` 会立即扫描尚未处理的历史小时，但仍跳过当前小时。

如果 `state.json` 丢失而源日志仍存在，服务无法确认这些源文件此前是否上传过。新版本不会自动信任无目标绑定的旧状态，也不会生成 `-partN`；应结合 `audit.jsonl`、对象时间和 SHA-256 完成显式迁移，或使用新的 `work-dir`，不能直接删除状态后重跑正式上传。

如果只是查询历史日志，不要把恢复文件放回 `auths/logs/keys`。如果确实需要把 `raw_log` 重建为源 `.log` 并重新处理，请先停止服务；恢复文件的修改时间和指纹会变化，因此可能触发一次新的上传。

> 详细请求日志仍可能包含敏感业务正文。请严格限制 Bucket、恢复目录、`state.json` 和 `audit.jsonl` 的访问权限。

## 9. 管理页按 Key 查看日志数据量

管理页右下角的“Key 日志用量”入口用于查看每个 `key_name` 产生的日志数据。统计分为：

- 已上传历史：来自上传器审计账本，只累计成功上传的小时批次。
- 本地尚存：来自 `logs-root` 中当前存在的非空普通 `.log` 文件。生产配置启用成功上传后删除时，它通常等同于待上传数据；若关闭删除或清理尚未完成，其中也可能包含已上传文件。
- 历史未绑定：审计中存在、但已不在当前 `api-key-names` 配置中的旧用户名或测试目录。

页面中的“数据量”是完整原始 `.log` 文件的字节数，包括请求、响应和日志元数据，不等同于模型输出字节数、Token 数或 `.zst` 压缩包大小。由于每小时的所有 Key 会合并到一个压缩包，系统不会虚构无法精确计算的“每 Key 压缩后大小”。

上传成功并删除源日志后，管理页仍可从 `audit.jsonl` 查看历史统计。审计写入采用两阶段流程，同一个小时可能同时出现 `uploaded_cleanup_pending` 和 `uploaded`；管理接口会按小时去重，优先采用最终的 `uploaded` 记录，避免将同一批数据计算两次。

需要保留的旧审计可以按 `.jsonl` 文件放入以下目录；管理接口先读取这些历史账本，再用当前 `audit.jsonl` 覆盖同一个小时：

```text
auths/log-uploader/history/
```

历史账本只用于统计，不会被上传器修改、上传或删除。不要把旧版“按 Key/模型分别打包”的重复审计与后来的“每小时单包”审计同时导入。

管理接口只返回用户名、时间和汇总数字，不返回原始 API Key、日志正文或本地文件路径。修改 `api-key-names` 后，既有审计无法反推出原始 Key，因此旧用户名会作为单独的历史项目保留，不会自动合并到新用户名。
