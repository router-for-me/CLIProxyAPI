# FEAT-clustered-proxy-fingerprint-quota-scheduler-plan

# 实施计划书：方案 A —— KunKun 真实浏览器登录隔离 + CLIProxyAPI uTLS 调度层指纹

> 上游需求文档：`docs/requirements/FEAT-clustered-proxy-fingerprint-quota-scheduler_CN.md`
> 本计划书基于方案 A（KunKun 不篡改 TLS、保持"内核与 UA 一致"；TLS 指纹多样化由 CLIProxyAPI 的 uTLS 层承担）制定。

---

## 1. 方案 A 分工边界（先冻结契约，再动手）

### 1.1 职责划分

| 层 | 负责方 | 承担内容 | 明确不做的事 |
| :--- | :--- | :--- | :--- |
| OAuth 交互式登录 | **KunKun** | 独立代理出网、独立 `user-data-dir` 沙盒、设备硬件指纹注入（Canvas/WebGL/Audio/UA/视口）、**内核与 UA 一致性保证** | 不修改底层 TLS（BoringSSL/NSS 特征保持真实浏览器原貌，信誉分最高） |
| API 高频调用 / Token 刷新 | **CLIProxyAPI** | 账号级 uTLS ClientHello 指纹、账号级代理绑定、代理池管理、额度重置优先调度 | 不做浏览器指纹（无 JS 执行环境） |

### 1.2 内核与 UA 一致性规则（方案 A 的防检测红线）

KunKun 分配设备配置时必须满足：

- 设备指纹为 Chrome/Android 桌面系 → 启动 Playwright **Chromium**（`KunScan.py` 中 `should_use_chromium_for_device` 已有此分支，需固化为登录模式的强制规则）；
- 设备指纹为 Firefox 系 → 启动 Playwright **Firefox**；
- User-Agent 版本号必须与实际内核大版本对齐（KunKun 已"严格保留设备配置中的完整原始 userAgent"，需在配置设备库时加一致性校验提示）；
- **禁止**出现"Safari UA + Chromium 内核"这类组合，否则触发上游 JA3/UA 交叉校验。

### 1.3 已验证的代码事实（计划依据）

- `sdk/cliproxy/auth/types.go:85` — `Auth.ProxyURL` 字段已存在（账号级代理落地基础）。
- `sdk/cliproxy/executor/request_proxy.go` — 执行级代理覆盖已通过 context 传递（`WithRequestProxyURL`），Token 刷新沿用账号/全局代理的逻辑已存在。
- `sdk/cliproxy/auth/quota_signals.go` — 已有 Claude/Codex 配额信号被动采集（`Signals` map + `ObservedAt`），含 `anthropic-ratelimit-unified-reset`、`x-codex-primary-*` 等。
- `sdk/cliproxy/auth/selector.go` — `RoundRobinSelector` / `FillFirstSelector` / `WeightedRoundRobinSelector` 均实现 `Pick(ctx, provider, model, opts, auths)` 接口；`getSelectorAvailableAuths` 提供冷却过滤。
- `sdk/cliproxy/auth/scheduler.go` — `schedulerStrategy` 枚举 + 增量调度器，新策略需要同步扩展。
- `internal/config/config_types.go:419` — 路由策略真实配置键为 **`routing.strategy`**（值为 `round-robin` / `weighted-round-robin` / `fill-first`）。**勘误**：需求文档草稿中的 `scheduler.strategy` 键名不正确，实际以 `routing.strategy` 为准。
- `internal/runtime/executor/helps/utls_client.go` — 已引入 `refraction-networking/utls`，但当前**硬编码** `tls.HelloChrome_Auto`，且每次请求新建连接（改造为按账号选 preset 的切入点）。
- `internal/browser/browser.go` + `sdk/auth/claude.go:88-94` — OAuth 登录当前直接 `open.Run(url)` 调系统默认浏览器（KunKun 桥接的替换点）。
- `internal/cmd/openai_login.go` — `LoginOptions{NoBrowser, CallbackPort, Prompt}`，CLI 登录入口统一形态。
- `internal/api/handlers/management/auth_files_provider_oauth.go` — 网页端 OAuth 已支持 session-scoped 代理（commit 27f3abfb），KunKun 桥接可复用同一 session 代理传递链。
- KunKun 侧：`libs/KunScan.py:2101/2127` 已支持 `proxy={"server": ...}` 与 Firefox SOCKS5 prefs；`libs/Ruln.py:1907` `firefox_fingerprint_prefs` 指纹注入成熟；**当前无任何 TLS 相关代码**（符合方案 A 前提）。

---

## 2. 里程碑拆解（M1 → M5）

> 排序原则：先做纯 Go、可单测、零外部依赖的调度器；再做网络层；最后做跨进程桥接。每个里程碑独立成 PR，且按仓库规范附 `FEAT-*.md` 文档更新、`gofmt`、`go build -o test-output ./cmd/server && rm test-output` 编译验证、`go test ./...` 全绿。

### M1 — 额度重置观测与 ResetPrioritySelector（纯调度层）

**目标**：同模型多账号时，优先调用距离额度重置最近的账号，吃满即将作废的配额。

**任务**：
1. `sdk/cliproxy/auth/quota_signals.go`：新增 `QuotaResetTime(provider string, now time.Time) (time.Time, bool)` —— 从 `Signals` 中的重置类头（`anthropic-ratelimit-unified-reset`、`x-codex-primary-reset-at`、`x-codex-primary-reset-after-seconds`、`retry-after`）解析出绝对重置时刻；解析失败返回 false。
2. `Auth` 增加窗口回退锚点：`Metadata["quota_window_anchor"]`（首次成功调用时间）+ `Attributes["quota_window_seconds"]`（如 claude=18000）。无响应头信号时按 `anchor + window` 推导理论重置点。
3. 新建 `sdk/cliproxy/auth/selector_reset_priority.go`：
   - `ResetPrioritySelector` 实现 `Selector.Pick`；
   - 内嵌 fallback `Selector`（weighted 或 round-robin，可配置）；
   - 紧急阈值 `urgentThreshold`（默认 15min）与抖动分组 `jitterWindow`（默认 60s，组内微轮询防 429）；
   - 复用 `getSelectorAvailableAuths` 做冷却/禁用过滤，不重复造轮子。
4. `sdk/cliproxy/auth/scheduler.go`：新增 `schedulerStrategyResetPriority`，调度器增量状态兼容新策略（不参与平滑权重游标即可）。
5. `internal/config/config_types.go`：`RoutingConfig.Strategy` 增加 `"reset-priority"` 取值及子项（`reset-priority-urgent-seconds`、`reset-priority-jitter-seconds`、`reset-priority-fallback`），并在 `sdk/cliproxy` 的 Selector 构造处接线。
6. 单元测试（新增文件，不改既有测试）：
   - 3 账号不同 reset 时刻 → 恒选 ΔT 最小者；
   - 60s 内双账号 → 组内轮询分布；
   - 紧急账号注入 429 cooldown → 立即故障转移到次优；
   - 无信号账号回退窗口推导；
   - 全部走 mock clock，禁止 `time.Sleep`（符合仓库测试规约）。

**验收**：模拟场景下紧急账号在重置前被 100% 优先命中；`go test ./sdk/cliproxy/auth/...` 全绿。

### M2 — 集群代理池与账号级出网绑定

**目标**：账号固定/动态绑定代理，推理、WS、刷新、OAuth 换 token 全链路一致。

**任务**：
1. `internal/config`：新增 `ProxyPoolsConfig`（id / strategy: sticky|round-robin / nodes / health_check），热加载经既有 watcher 链路。
2. 新建 `internal/proxypool/`（放在 internal 顶层，executor 目录只放 executor 与其测试）：
   - `Manager`：按 pool id 解析账号 → 代理 URL；sticky 绑定持久化到 `Auth.Metadata["proxy_pool_node"]`；
   - 健康探针：周期拨测，失败节点摘除，恢复后回归；
   - 节点级熔断：连续 dial 失败标记故障。
   > 勘误：`Manager.ReportDialResult` 已提供，但真实请求链路尚未上报 dial 结果；目前节点健康主要由周期探针驱动，两次探针之间的请求失败不会立即触发熔断。
3. 接线点：在 Auth 解析/注册路径上，若 `ProxyURL` 为空且 `proxy_pool_id` 非空，由 Manager 解析出最终 `ProxyURL` 写回 Auth（复用现有 `Auth.ProxyURL` 消费链，**不改 executor 的代理消费逻辑**，这是 KISS 关键）。
   > 勘误：配置型 API-key（`claude-api-key` 等）没有 Auth 装载路径，不支持 `proxy_pool_id`；只有 `auths/*.json` 或运行时注册的 Auth 记录支持池绑定。
4. OAuth 会话代理（management 网页登录）若传的是 pool id，同样先解析为具体节点再走既有 `oauthConfigWithProxy` 链路。
5. 测试：sticky 绑定跨请求稳定、节点摘除与恢复、pool id 解析；代理健康探测用 httptest 本地代理，不依赖外网。

**验收**：日志/抓包验证同一账号全链路（推理 + 刷新）出网 IP 恒定；节点故障自动切换并告警。

> 勘误：OAuth 登录完成后代理会以显式 `proxy_url` 落到账号记录，显式代理优先于池重绑；因此登录所得的账号在节点故障时不会自动切换到池内其他节点。这是"登录 IP = 运行 IP"一致性优先的有意取舍。

### M3 — 账号级 uTLS 指纹多样化（CLIProxyAPI 侧指纹核心）

**目标**：不同账号的 API 请求呈现不同 JA3/JA4，且与该账号 KunKun 登录设备的 UA 家族一致。

**任务**：
1. `Auth.Attributes["tls_preset"]` 新增约定值：`chrome_120`（默认）、`chrome_auto`、`firefox_120`、`safari_16`、`ios_16`、`random_chrome`（从若干 Chrome 版本 preset 中稳定哈希选取，同账号恒定）。
   > 勘误：默认为空值（保持 `HelloChrome_Auto`，uTLS v1.8.2 实为 Chrome 133），不是 `chrome_120`；实现额外接受 `chrome_131` / `chrome_133`。`ios_16` 以 `HelloIOS_Auto`（iOS 14）近似并告警。账号 JSON 顶层 `tls_preset` 会写入该属性。
2. `internal/runtime/executor/helps/utls_client.go`：
   - `newUtlsRoundTripper(proxyURL string)` 扩展为 `newUtlsRoundTripper(proxyURL, preset string)`；
   - `tls.UClient(conn, cfg, presetToHello(preset))` 替换硬编码 `HelloChrome_Auto`；
   - preset 解析函数集中放在 helps 包（符合"executor 目录只放 executor 和测试"的规约）；
   - **必须保持与 UA 一致性**：从执行上下文取该 Auth 的 UA 家族，preset 与 UA 冲突时告警并回退与 UA 一致的默认值（防方案 A 红线被配置错误击穿）。
3. 调用链核实：梳理所有构造 `utlsRoundTripper` 的 executor 路径（含 WS 升级、token 刷新），统一从 Auth Attributes 取 preset，逐条补回归测试（请求构建路径变更加回归测试是仓库硬规约）。
   > 勘误：preset 仅作用于 Codex/ChatGPT 的 Chrome transport；Claude/Anthropic 路径固定使用 `tls.HelloCustom` + Claude Code ClientHello，不消费账号 preset。这是保护既有 Claude Code 兼容性的有意取舍。
4. 测试：本地 TLS server（`httptest.NewTLSServer` + 自签）抓 ClientHello 断言 preset 生效；参考既有 `utls_client_*_test.go` 风格。

**验收**：两个账号分别配 `chrome_120` / `firefox_120`，握手特征可区分；配置冲突时回退且不崩溃。

### M4 — KunKun OAuth 桥接（跨项目改造）

**目标**：`--claude-login` / `--codex-login` / 网页端 OAuth 时，拉起带独立代理与设备指纹的 KunKun 浏览器完成授权。

**任务**：
1. **KunKun 侧（最小改动）**：新增 OAuth 登录入口模式——
   - CLI 形式：`uv run python main.py --oauth-launch --url <auth_url> --proxy <proxy> --profile-id <id> --device-mode browser --user-data-dir <dir>`；
   - 复用 `KunScan.py` 现有 chromium/firefox launch + 代理参数 + `Ruln` 指纹注入，仅裁剪掉扫码/自动回复等任务逻辑，页面停留等待用户手动完成授权，监听回调后自动退出；
   - 新增文件建议 `libs/oauth_login.py` + `main.py` 参数分发，不改动既有任务链路（KunKun 仓库规约：改代码须同步版本号与 CHANGELOG）。
2. **CLIProxyAPI 侧**：
   - `internal/config` 新增 `kunkun_browser` 配置块（enabled / driver_mode: cli|service / project_root / python_bin / profiles_dir / headless / launch_timeout_seconds）；
   - 新建 `internal/browser/kunkun_driver.go`：构造命令、注入 `--proxy`（该账号解析后的专属代理）、超时回收（默认 5 分钟，属"凭证获取期"，符合仓库"仅凭证获取允许超时"的规约）、stdout 状态解析；
   - 接入点：`internal/browser/browser.go` 增加 `OpenURLWithLauncher` 抽象；`sdk/auth/claude.go` 及各 provider 登录入口优先走 KunKun driver（enabled 时），失败降级现有 `OpenURL` 并告警；
   - management 网页 OAuth（`auth_files_provider_oauth.go`）：session 已带 proxy，桥接时透传同一代理，保证"登录 IP = 运行 IP"；
   - Callback 链路不变：KunKun 内浏览器照常 302 回 `127.0.0.1:<callback-port>`，CLIProxyAPI 端口监听逻辑零改动。
   > 勘误：网页端启用 KunKun 时，服务端拉起 KunKun 后 Web UI 仍会打开返回的授权 URL，可能双开浏览器；关闭 `kunkun-browser` 即恢复旧行为。
3. Profile 沙盒：`profiles_dir/<account_id>/` 目录由 driver 首次登录时创建并复用；`profile_id` 与账号 ID 强绑定写进 `Auth.Attributes["kunkun_profile_id"]`。
   > 勘误：当前 profile_id 每次登录新生成（`provider-<uuid>`），未回写 `Auth.Attributes["kunkun_profile_id"]`，同一账号重登不会复用沙盒目录。
4. 测试：driver 的命令拼装/超时回收/降级逻辑单测（mock exec）；真实 OAuth 冒烟作为手动验收项。

**验收**：执行登录命令弹出带指定代理与指纹的 KunKun 浏览器，完成授权后 token 正常落盘 `auths/`；未启用或环境异常时优雅降级到系统浏览器。

### M5 — 端到端联调、文档与配置样例

**任务**：
1. `test/` 下补跨模块集成用例：账号(代理+TLS preset+指纹 id+窗口) → 调度选择 → 出网绑定 的全链路断言。
2. 更新 `config.example.yaml`（routing 新策略、proxy_pools、kunkun_browser 示例）。
3. 更新 `docs/requirements/` 下两份 FEAT 文档与实现差异对齐（仓库规约：文档与代码同 PR）。
4. 长稳回归：`go test ./...` + 编译验证 + `gofmt -w .`。

---

## 3. 关键设计决策记录（ADR 摘要）

| # | 决策 | 理由 |
| :--- | :--- | :--- |
| D1 | KunKun 不做 TLS 伪指纹 | 真实 Chromium/Firefox 的 JA3 信誉分最高；篡改需编译期补丁或本地 MITM 隧道，复杂度与风险远超收益 |
| D2 | 账号级 TLS preset 放 `Auth.Attributes` | Attributes 即"executor 所需的不可变配置"，天然随 Auth 快照分发，不引入新存储 |
| D3 | 代理池解析结果回写 `Auth.ProxyURL`，不改 executor 消费逻辑 | `WithRequestProxyURL` 消费链已覆盖推理/刷新，最小侵入 |
| D4 | 重置时间优先读响应头 Signals，回退窗口锚点推导 | 有头用头（精确），无头用锚（可预算），不引入主动探测请求 |
| D5 | 调度策略挂 `routing.strategy: "reset-priority"` | 与既有三策略同一配置面，热加载/watcher 免费获得 |
| D6 | OAuth 登录 IP 必须 = 运行 IP | 上游关联分析按 IP 聚类，登录与调用异 IP 是最常见封号诱因 |

## 4. 风险与对策

1. **preset 与 UA 配置冲突** → M3 的运行时一致性校验强制回退并告警。
2. **本地时钟漂移** → 解析重置头时用上游 `Date` 响应头计算 offset 修正 ΔT。
3. **KunKun 僵尸进程/端口占用** → driver 启动超时回收 + callback 端口复用现有 `ErrPortInUse` 处理。
4. **代理节点雪崩** → 节点熔断 + sticky 绑定元数据不删除（节点恢复自动回归，账号 IP 恒定优先于可用性）。
5. **KunKun 跨仓库依赖** → driver 对 KunKun 仅命令行/HTTP 契约耦合，KunKun 缺失时全功能降级，CLIProxyAPI 不硬依赖。

## 5. 里程碑顺序与预估规模

| 里程碑 | 预估新增/改动 | 主要落点 |
| :--- | :--- | :--- |
| M1 | ~600 行 + 测试 | `sdk/cliproxy/auth/`、`internal/config` |
| M2 | ~800 行 + 测试 | `internal/proxypool/`（新）、`internal/config` |
| M3 | ~300 行 + 测试 | `internal/runtime/executor/helps/` |
| M4 | Go 侧 ~500 行 + KunKun 侧 ~300 行 | `internal/browser/`、`internal/config`、KunKun `libs/oauth_login.py` |
| M5 | 集成测试 + 文档 | `test/`、`config.example.yaml`、`docs/requirements/` |

每个里程碑独立可交付、可回滚；M1/M3 可并行，M2 先于 M4（桥接需要解析后的具体代理节点）。

---

## 8. 实现对齐记录 (Implementation Alignment)

以下逐条记录计划与最终实现的偏差；"有意取舍"表示已权衡后保留，其余为后续补强方向。

1. **`HelloChrome_Auto` = Chrome 133**：uTLS v1.8.2 的 `HelloChrome_Auto` 实际指向 Chrome 133，未配置 preset 的账号因此呈现 Chrome 133 指纹。有意取舍：保持零配置兼容。影响：与早期"默认 Chrome 120"的表述不符。补救：需要 Chrome 120 时显式配置 `tls_preset: chrome_120`。
2. **`ios_16` 近似 iOS 14**：uTLS v1.8.2 无 iOS 16 profile，`ios_16` 使用 `HelloIOS_Auto` 并打印一次性告警。有意取舍：保持 iOS UA 家族一致。影响：版本号偏低。补救：等待 uTLS 新增 profile 后重映射。
3. **preset 不影响 Claude 路径**：Anthropic 固定 `HelloCustom` + Claude Code ClientHello，仅 Codex/ChatGPT 使用账号 preset。有意取舍：保护既有 Claude Code 兼容性。影响：Claude 账号无法通过该项差异化 TLS。补救：如确有需求，另建 Anthropic preset 机制并补兼容性回归。
4. **`anthropic-ratelimit-reset` 未采集**：解析器支持该头，但采集白名单仅含 `anthropic-ratelimit-unified-*`、`retry-after` 与 Codex 头。有意取舍：以实际观测头为准。影响：仅返回旧头的账号回退到窗口锚点推导。补救：加入采集白名单并补测试。
5. **旧 management 端点不识别 `reset-priority`**：`/v0/management/routing/strategy` 仍只接受三种旧策略，PUT `reset-priority` 返回 400。有意取舍：该组端点已废弃。影响：只能通过配置文件/热加载启用新策略。补救：迁移到受支持的 v8/新端点。
6. **网页端 OAuth 双开浏览器**：服务端拉起 KunKun 的同时 Web UI 仍会打开授权 URL。有意取舍：保留 Web UI 兼容逻辑。影响：可能出现两个窗口。补救：前端检测 launcher 状态或服务端返回抑制标志。
7. **profile_id 未持久化复用**：每次登录生成 `provider-<uuid>`，未写回 `Auth.Attributes["kunkun_profile_id"]`。有意取舍：当前登录阶段没有稳定账号 ID。影响：重登产生新沙盒目录。补救：登录成功后持久化 profile_id 并复用。已于后续 commit 解决：profile id 改为 provider+proxy 确定性派生并回写 Attributes（无代理 CLI 登录除外）。
8. **OAuth 账号显式代理优先**：登录代理作为 `proxy_url` 落盘，节点故障不会自动池重绑。有意取舍：保证登录/运行 IP 一致。影响：节点失效后不自动切换。补救：记录 pool 来源并在显式代理等于历史节点时允许重解析。
9. **请求 dial 失败不上报熔断**：`ReportDialResult` 存在但请求链路未调用，健康探针为主输入。有意取舍：避免热路径耦合。影响：探针间隔内仍可能选中故障节点。补救：在 executor/transport dial 错误处上报。
10. **配置型 API-key 不支持池绑定**：`claude-api-key` 等配置凭据无 Auth 装载路径，无法使用 `proxy_pool_id`。有意取舍：合成路径不携带账号元数据。影响：API-key 多账号无法池化出网。补救：合成 Auth 时映射 `proxy_pool_id` 并复用解析器。
