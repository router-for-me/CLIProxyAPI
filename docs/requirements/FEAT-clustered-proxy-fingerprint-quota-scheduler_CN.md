# FEAT-clustered-proxy-fingerprint-quota-scheduler

# 多账号集群化代理、KunKun 指纹浏览器 OAuth 防关联与额度重置就近优先调度系统 需求与架构设计说明书

## 1. 文档概述

### 1.1 背景与目标
在多账号、多供应商的高并发 AI API 代理场景下，当前系统面临三大核心痛点：
1. **多账号风控关联与封禁风险**：不同账号共用宿主机出网 IP；OAuth 交互式登录时直接调用系统默认浏览器，共享系统级 Cookie、LocalStorage、Canvas、WebGL 以及操作系统特征，极易被 Cloudflare / OpenAI / Anthropic 等上游多维度关联判定为“一人多号”或“批量自动化机器人”并实施批量封禁。
2. **TLS 握手特征单调**：HTTP 客户端层面的 TLS ClientHello（JA3/JA4 指纹）缺乏账号粒度的多样性与真实浏览器指纹拟真，无法针对不同账号分配独立的网络协议栈特征。
3. **配额利用率不均与浪费**：部分账号拥有按时间滚动的配额窗口（如 Claude 5 小时滚动窗口、ChatGPT Plus 3 小时限制）。现有轮询（Round-Robin）或填充（Fill-First）调度策略未感知账号配额重置倒计时。若某账号距离额度重置仅剩数分钟且仍有剩余配额，未能优先将其用完便跨入新周期，导致历史窗口剩余额度彻底作废。

为解决上述问题，本项目引入三位一体的综合能力升级：
- **集群化代理与多账号独立出网（Clustered & Per-Account Egress Proxy）**
- **KunKun 指纹浏览器深度桥接与 TLS 指纹防关联 OAuth 登录（Anti-Detect Browser & TLS Fingerprint System）**
- **同模型多供应账号“额度即将重置优先”智能调度器（Smart Quota Reset-First Scheduler）**

---

## 2. 总体系统架构设计

```
                            [ Client HTTP / WebSocket Requests ]
                                             │
                                             ▼
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                             CLIProxyAPI Core Conductor                                 │
│                                                                                        │
│   ┌────────────────────────────────────────────────────────────────────────────────┐   │
│   │             智能调度引擎 (ResetPrioritySelector - 额度重置优先算法)              │   │
│   │                                                                                │   │
│   │   候选账号列表 ──► 过滤 Cooldown/熔断 ──► 评估各账号重置时间差 ΔT               │   │
│   │                                                │                               │   │
│   │   ┌────────────────────────────────────────────┴───────────────────────────┐   │   │
│   │   │ 梯队 1: 紧急重置区 (0 < ΔT ≤ 15 min) ──► 按 ΔT 升序 (优先吃满即将清零配额) │   │   │
│   │   │ 梯队 2: 常规可用区 (ΔT > 15 min)     ──► 平滑加权轮询 / 权重分配               │   │   │
│   │   └────────────────────────────────────────────────────────────────────────┘   │   │
│   └────────────────────────────────────────────────────────────────────────────────┘   │
│                                            │ (选定目标 Auth 凭证)                       │
│                                            ▼                                           │
│   ┌────────────────────────────────────────────────────────────────────────────────┐   │
│   │                 账号元数据与运行时隔离层 (Auth Record Metadata)                  │   │
│   │   - 账号标识: id, provider, label                                              │   │
│   │   - 绑定代理: proxy_url / proxy_pool_id (独立专属出网代理)                     │   │
│   │   - TLS 签名: HelloChrome_120 / HelloFirefox_120 / HelloSafari                 │   │
│   │   - 浏览器指纹: kunkun_profile_id (关联独立硬件设备配置文件)                    │   │
│   │   - 额度重置状态: QuotaState (next_reset_at, window_duration, signals)         │   │
│   └────────────────────────────────────────────────────────────────────────────────┘   │
└────────────────────────────────────────────┬───────────────────────────────────────────┘
                                             │
                     ┌───────────────────────┴───────────────────────┐
                     ▼                                               ▼
┌──────────────────────────────────────────┐   ┌──────────────────────────────────────────┐
│   上游运行时请求 (Runtime Egress)         │   │   交互式 OAuth 登录 (KunKun 指纹浏览器)    │
│                                          │   │                                          │
│  - 独立 Egress Proxy (SOCKS5/HTTP)       │   │  - 调起 KunKun 实例 (CLI 驱动或 API 桥接) │
│  - uTLS ClientHello 随机拟真签名         │   │  - 注入账号专属 Proxy 出网               │
│  - 严格保持 WebSocket / SSE 独立通道     │   │  - 注入 DeviceData 独立硬件指纹          │
│  - 实时捕获 upstream 响应头重置标记      │   │  - 独立沙盒目录 profiles/{account_id}/   │
│    (anthropic-ratelimit-*, x-codex-*)    │   │  - 登录完成后回调回到 CLIProxyAPI 端口    │
└──────────────────────────────────────────┘   └──────────────────────────────────────────┘
```

---

## 3. 功能需求详细规范

### 3.1 模块一：集群化代理与多账号独立代理 (Clustered & Per-Account Proxy)

#### 3.1.1 需求要点
1. **单账号专属代理静态绑定**：
   - 允许在各个账号的认证文件（`auths/auth_*.json`）中直接指定 `proxy_url`（支持 `http://`, `https://`, `socks5://`, `socks5h://` 协议，含用户名密码鉴权）。
2. **集群代理池管理（Proxy Pool）**：
   - 支持在系统配置 `config.yaml` 中声明多个代理池（例如 `us-residential`, `jp-datacenter`）。
   - 代理池支持节点自动健康探测（定时 HEAD/GET 上游探测地址）。
   - 账号支持绑定代理池 ID（`proxy_pool_id`）。对于未指定固定 `proxy_url` 的账号，系统支持以下分配策略：
     - `sticky`（粘性绑定）：账号首次分配到某个可用节点后，持久化绑定，后续所有请求（包括 Refresh 和 API）保持 IP 一致。
     - `round-robin` / `least-connections`：动态负载分配。
3. **全链路严格出网保护**：
   - **推理请求（Inference）**：文本、SSE 流式、图像生成等所有上游请求经由对应代理发出。
   - **长连接（WebSocket）**：Codex WebSocket 通道建立前必须配置 DialContext 指向该代理。
   - **凭证续期（Token Refresh）**：OAuth 刷新过程强制走绑定的代理，禁止直连。
   - **OAuth 授权码交换（Code Exchange）**：网页重定向回调拿到 `code` 后，向认证中心换取 `access_token` 的 POST 请求必须走绑定的代理。

---

### 3.2 模块二：KunKun 指纹浏览器桥接与 TLS 指纹防关联体系

#### 3.2.1 需求要点
1. **现有 KunKun 项目联动**：
   - 利用本地已存在的 KunKun 指纹浏览器框架（`/Users/crymang/Works/KunKun`）。
   - KunKun 具备基于 PySide6 + Playwright 的移动端/桌面端设备伪装能力，涵盖 Canvas 2D 噪点、WebGL 硬件解掩码、AudioContext 频率扰动、Screen 物理视口、完整原始 User-Agent 保持、以及防 WebRTC 真实内网 IP 泄露等 50+ 维度指纹。
2. **双重集成通信模式（KunKun Bridge Adapter）**：
   - **模式 A：CLI 驱动模式（Command Driver Mode，首选免后台常驻方式）**：
     CLIProxyAPI 通过子进程启动 KunKun 入口脚本，传递独立参数：
     ```bash
     uv run python main.py --oauth-mode \
       --url "<OAuth授权URL>" \
       --proxy "<专属ProxyURL>" \
       --profile-id "<账号唯一标识ID>" \
       --device-mode "browser" \
       --user-data-dir "./auths/kunkun_profiles/<账号唯一标识ID>"
     ```
     > 勘误：实际 driver 参数为 `--oauth-launch`（不是 `--oauth-mode`），并追加 `--callback-prefix <redirect_uri>`；`--user-data-dir` 以绝对路径传入。模式 B 尚未实现，配置 `driver-mode: "service"` 会在启动时返回明确错误。
   - **模式 B：Service API 模式（HTTP/RPC 驱动）**：
     KunKun 在本机后台运行服务（如探针服务或控制服务 `probe_service`），暴露标准 REST 接口：
     `POST /api/v1/browser/launch-session`，CLIProxyAPI 发送 JSON 负载驱动浏览器拉起。
3. **沙盒与存储隔离**：
   - 每个账号对应专用的 `user-data-dir` 沙盒目录。
   - 账号之间的 Cookie、SessionStorage、LocalStorage、IndexedDB 物理隔离，互不串扰。
4. **底层 TLS 指纹多样化（Go uTLS 层面）**：
   - 在运行时 API 调用层，利用 `github.com/refraction-networking/utls` 实现 ClientHello 指纹多样化。
   - 账号可在元数据中指定或随机分配 TLS 预设（如 `HelloChrome_120`, `HelloFirefox_120`, `HelloSafari_16_0`），使得网络层 JA3/JA4 签名与 KunKun 模拟的 User-Agent 保持逻辑一致。
   > 勘误：实际取值为 `chrome_120` / `chrome_auto` / `firefox_120` / `safari_16` / `ios_16` / `random_chrome` / `chrome_131` / `chrome_133`，存放在账号 JSON 顶层 `tls_preset` 并读入 `Auth.Attributes["tls_preset"]`。空值等价 `HelloChrome_Auto`（uTLS v1.8.2 实为 Chrome 133）；`ios_16` 以 iOS 14 近似；Claude/Anthropic 路径固定 `HelloCustom`，不受该配置影响。

---

### 3.3 模块三：同模型多账号“额度即将重置优先”调度算法 (Smart Quota Reset-First Scheduler)

#### 3.3.1 核心算法目标
当一个模型（例如 `claude-3-7-sonnet` 或 `gpt-4o`）配置了多个可用账号时，调度器的核心优化目标为：**最大化集群整体有效吞吐量，杜绝因周期重置而失效的剩余配额**。

#### 3.3.2 额度重置感知引擎 (Quota Reset Observer)
系统多维度采集与推导各账号的重置时间戳 $T_{\text{reset}}$：
1. **响应头被动感知（Passive Header Observation）**：
   - **Claude / Anthropic**：
     - 解析 `anthropic-ratelimit-unified-reset`（Unix 时间戳或 RFC3339 时间）。
     - 解析 `anthropic-ratelimit-reset` / `retry-after`。
     > 勘误：解析器可识别 `anthropic-ratelimit-reset`，但现有被动采集白名单只采集 `anthropic-ratelimit-unified-*`、`retry-after` 与 Codex 头；普通 `anthropic-ratelimit-reset` 不会进入 `QuotaState.Signals`。解析器只接受 Unix 秒（不支持 RFC3339），也未使用 `x-ratelimit-reset-*`。
   - **Codex / OpenAI**：
     - 解析 `x-codex-primary-reset-at`（Unix 时间戳）。
     - 解析 `x-codex-primary-reset-after-seconds`（相对剩余秒数）。
     - 解析 `x-ratelimit-reset-requests` / `x-ratelimit-reset-tokens`。
   - 每一次请求返回后，原子更新账号内存中的 `QuotaState.NextRecoverAt` 或 `QuotaState.QuotaResetAt`。
2. **主动时间窗口推导（Active Window Fallback）**：
   - 针对未满载或未提供精确 reset 头的账号，支持按供应商规则（如 Claude Pro 5 小时滚动窗口、ChatGPT Plus 3 小时滚动窗口）依据账号的首次使用时间锚点推导预测重置时间。

#### 3.3.3 调度策略实现逻辑 (`ResetPrioritySelector`)
设当前时间为 $T_{\text{now}}$，候选账号集合为 $\mathcal{A}$：
1. **可用性初筛**：
   - 过滤掉被禁用（Disabled）或处于熔断/硬性冷却状态（In Cooldown，$T_{\text{recover}} > T_{\text{now}}$）的账号。
2. **计算重置倒计时**：
   - 对每个可用账号 $a_i \in \mathcal{A}$，计算 $\Delta T_i = T_{\text{reset}}(a_i) - T_{\text{now}}$。
3. **分层优先级调度**：
   - **紧急重置层（Urgent Layer，定义阈值 $0 < \Delta T \le T_{\text{urgent}}$，默认 15 分钟）**：
     - 若存在账号处于此区间，**绝对优先在此区间内选择**。
     - 排序规则：按 $\Delta T_i$ 升序排列（最快要重置的排第一）。
     - 防惊群与微轮询机制：若有多个账号重置时间非常接近（如差值在 60 秒内），在该小分组内执行平滑轮询（Round-Robin）或加权分流，防止并发突发请求将单账号瞬间打入 429。
   - **常规层（Normal Layer，$\Delta T > T_{\text{urgent}}$ 或未知时间）**：
     - 若没有账号即将重置，退化为常规平滑加权轮询（Weighted Round-Robin）或轮询（Round-Robin），保证多账号均匀消耗。
4. **429 容错快速降级**：
   - 当正在被优先调用的账号遇到并发速率上限返回 429 时，调度器捕获错误并在内存中为该账号施加短暂 Cooldown，随后立即选出次优候选账号进行自动重试，确保客户端请求无感成功。

---

## 4. 详细配置与数据结构契约

### 4.1 配置文件规范 (`config.yaml`)

```yaml
# 全局调度器设置
scheduler:
  # 可选: "round-robin", "fill-first", "weighted", "reset-priority"
  strategy: "reset-priority"
  reset_priority:
    urgent_threshold_seconds: 900    # 15分钟以内为即将重置区间
    clustering_jitter_seconds: 60    # 60秒内重置的账号进行组内微轮询防打崩
    fallback_strategy: "weighted"    # 常规区间退化策略: weighted 或 round-robin

# 代理池集群配置
proxy_pools:
  - id: "pool-us-res"
    strategy: "sticky"              # sticky(粘性持久绑定) / round-robin / random
    health_check:
      enabled: true
      test_url: "https://api.anthropic.com"
      interval_seconds: 60
      timeout_seconds: 5
    nodes:
      - "socks5://user:pass@us-node-1.proxy.net:1080"
      - "socks5://user:pass@us-node-2.proxy.net:1080"
      - "socks5://user:pass@us-node-3.proxy.net:1080"
  - id: "pool-jp-dc"
    strategy: "round-robin"
    nodes:
      - "http://user:pass@jp-node-1.proxy.net:8080"
      - "http://user:pass@jp-node-2.proxy.net:8080"

# KunKun 指纹浏览器适配配置
kunkun_browser:
  enabled: true
  # 驱动模式: "cli" (执行本地 python 进程) 或 "service" (调用 HTTP 服务)
  driver_mode: "cli"
  # KunKun 项目主目录路径
  project_root: "/Users/crymang/Works/KunKun"
  # Python 运行可执行文件 (如 uv, python3)
  python_bin: "uv"
  # 沙盒 Profile 存放路径
  profiles_dir: "./auths/kunkun_profiles"
  # Service 模式下的端点地址 (若启用 service 模式)
  service_endpoint: "http://127.0.0.1:8088"
  # 默认设备指纹模拟模式 (browser / wechat / qq)
  default_device_mode: "browser"
  # 窗口可见模式 (true 为弹出 GUI 供人工点击, false 为无头模式)
  headless: false
```

> 勘误：实际配置键为 `routing.strategy`（不是 `scheduler.strategy`）与 `routing.reset-priority-urgent-seconds` / `routing.reset-priority-jitter-seconds` / `routing.reset-priority-fallback`（默认 900 / 60 / `weighted`）；代理池键为 `proxy-pools`（`health-check` / `test-url` / `interval-seconds` / `timeout-seconds`），策略仅实现 `sticky` 与 `round-robin`（无 `random` / `least-connections`）；KunKun 键为 `kunkun-browser`（`enabled` / `driver-mode` / `project-root` / `python-bin` / `profiles-dir` / `default-device-mode` / `headless` / `launch-timeout-seconds`），`service-endpoint` 不存在且 service 模式未实现。

### 4.2 账号凭据数据结构规范 (`auths/*.json`)

```json
{
  "id": "claude-team-node01",
  "provider": "claude",
  "label": "US West Dedicated Claude Account",
  "disabled": false,
  "proxy_url": "socks5://user:pass@us-node-1.proxy.net:1080",
  "proxy_pool_id": "pool-us-res",
  "fingerprint": {
    "tls_preset": "HelloChrome_120",
    "kunkun_profile_id": "prof_us_west_01",
    "device_data_preset": "desktop_macos_sonoma",
    "user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
  },
  "quota_state": {
    "exceeded": false,
    "next_recover_at": "0001-01-01T00:00:00Z",
    "quota_reset_at": "2026-10-26T22:30:00Z",
    "window_duration_seconds": 18000,
    "signals": {
      "anthropic-ratelimit-unified-reset": "1793053800",
      "anthropic-ratelimit-unified-status": "allowed"
    }
  }
}
```

> 勘误：运行时实际读取账号 JSON 顶层 `proxy_url`、`proxy_pool_id`、`tls_preset`、`quota_window_seconds`，不存在 `fingerprint.*` 嵌套结构与 `quota_state.*` 配置读取；`tls_preset` 与 `proxy_pool_id` 会写入 `Auth.Attributes`。`quota_state.signals` 仅是被动采集的运行态快照，不会从账号文件回填；`kunkun_profile_id` 当前不会写入。窗口锚点由首次成功调用写入 `Metadata["quota_window_anchor"]`。

---

## 5. 核心算法伪代码实现规范

### 5.1 额度就近重置调度器 (`ResetPrioritySelector.Pick`)

```go
func (s *ResetPrioritySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
    now := time.Now()

    // 1. 获取所有非冷却、非禁用的可用账号
    available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
    if err != nil || len(available) == 0 {
        return nil, err
    }
    if len(available) == 1 {
        return available[0], nil
    }

    // 2. 将候选账号划分为: 紧急即将重置区 (Urgent) 与 常规区 (Normal)
    var urgentCandidates []*AuthWithDelta
    var normalCandidates []*Auth

    urgentThreshold := s.urgentThreshold // 默认 15 分钟
    for _, candidate := range available {
        resetAt := candidate.ResolveQuotaResetTime(provider, model, now)
        if !resetAt.IsZero() && resetAt.After(now) {
            delta := resetAt.Sub(now)
            if delta <= urgentThreshold {
                urgentCandidates = append(urgentCandidates, &AuthWithDelta{
                    Auth:  candidate,
                    Delta: delta,
                })
                continue
            }
        }
        normalCandidates = append(normalCandidates, candidate)
    }

    // 3. 优先从紧急重置层出选 (按 Delta 升序，最先重置的最优先)
    if len(urgentCandidates) > 0 {
        sort.Slice(urgentCandidates, func(i, j int) bool {
            return urgentCandidates[i].Delta < urgentCandidates[j].Delta
        })

        // 若排在首位和次位的账号重置时间相差在 Jitter 窗口内（如 60s 内），执行微轮询分流
        bestGroup := extractNearTies(urgentCandidates, s.clusteringJitter)
        return s.microRoundRobinPick(bestGroup, provider, model), nil
    }

    // 4. 若无紧急重置账号，退化为平滑加权轮询或普通轮询
    return s.fallbackSelector.Pick(ctx, provider, model, opts, normalCandidates)
}
```

---

## 6. 实施路线图与验收标准 (Milestones & Acceptance Criteria)

### 6.1 里程碑规划

| 阶段 | 交付目标 | 核心工作内容 | 验收标准 |
| :--- | :--- | :--- | :--- |
| **M1** | 额度重置感知与智能优先调度器 | 1. 扩充 `QuotaSignals` 解析统一存储 `QuotaResetAt`<br>2. 编写 `ResetPrioritySelector` 算法实现<br>3. 实现临近重置账号分组与排序 | 单元测试模拟 3 个账号不同 reset 时间，100% 优先命中剩余时间最短的账号；遇到 429 顺滑故障转移 |
| **M2** | 集群代理管理与多账号绑定 | 1. 在 `config.yaml` 增加 `proxy_pools` 解析<br>2. 增加 `ProxyPoolManager` 与健康检查心跳<br>3. 保证账号无论 API 请求还是 Token 刷新全部绑定代理 | 抓包或日志验证各个账号严格使用绑定的独立代理 IP 出网，代理节点故障时能感知并切换 |
| **M3** | KunKun 指纹浏览器桥接适配器 | 1. 开发 `internal/browser/kunkun_driver.go`<br>2. 对接 KunKun 命令行启动协议与独立沙盒目录<br>3. OAuth 流程串通与 Token 换取闭环 | 执行 `go run ./cmd/server --claude-login` 能拉起带独立指纹和代理的 KunKun 浏览器，成功完成登录并写入认证文件 |
| **M4** | uTLS 签名多样化与跨模块联调 | 1. 扩展 `utlsRoundTripper` 账号级指纹套件<br>2. 完成端到端长周期回归测试<br>3. 编写用户配置手册与排障指南 | 全量 `go test ./...` 绿灯，系统长期运行平稳无内存泄漏或死锁 |

---

## 7. 风险评估与防御对策

1. **时钟漂移风险（Clock Skew）**：
   - 依赖本地系统时钟与上游响应头 `Date` 计算 $\Delta T$。若本地服务器时钟偏差较大，可能导致调度时间窗计算失真。
   - **对策**：在捕获上游响应头时，计算本地时间与上游 `Date` 的差值偏差（Server Offset），在评估 $T_{\text{reset}}$ 时自动修正偏差。
2. **代理节点失效与瞬时雪崩**：
   - 若某账号绑定的代理节点突然离线，可能导致该账号全部请求报错。
   - **对策**：增加代理级熔断器。当出网网络连续遇到 Dial 错误或 Connection Refused 时，自动标记代理故障并临时降级至备用代理或报警。
3. **KunKun 浏览器进程生命周期管理**：
   - 用户若在 OAuth 页面长时间未完成操作或意外关闭窗口，可能导致僵尸进程或端口占用。
   - **对策**：设置浏览器启动超时时间（如 5 分钟），超时后由父进程安全回收并释放资源。

---

## 8. 实现对齐记录 (Implementation Alignment)

本节如实记录当前实现与上文设计之间的偏差。标注"有意取舍"的条目是已经过权衡的设计决定；其余条目是待后续里程碑补齐的缺口。

1. **`HelloChrome_Auto` 实为 Chrome 133**：uTLS v1.8.2 中 `HelloChrome_Auto = HelloChrome_133`，因此未配置 `tls_preset` 的账号默认呈现 Chrome 133 指纹，而不是早期草稿中的 Chrome 120。有意取舍：保持零配置部署的既有 `HelloChrome_Auto` 行为不变。影响：默认账号的 JA3/JA4 版本高于文档描述。补救方向：需要固定在 Chrome 120 的账号显式设置 `tls_preset: chrome_120`；文档与示例已同步标注。
2. **`ios_16` 以 iOS 14 近似**：uTLS v1.8.2 没有 iOS 16 profile，`ios_16` 映射到 `HelloIOS_Auto`（iOS 14），并在首次使用时输出一次告警。有意取舍：留在 iOS UA 家族内，避免跨族指纹冲突。影响：iOS 设备指纹版本偏低。补救方向：uTLS 提供更新的 iOS profile 后再切换映射。
3. **`tls_preset` 对 Claude 账号不生效**：Anthropic 上游路径固定使用 `tls.HelloCustom` + Claude Code ClientHello 规格，只有 Codex/ChatGPT 路径消费账号 preset。有意取舍：保护既有 Claude Code 兼容性与请求头顺序。影响：Claude 账号之间无法通过该配置实现 TLS 指纹多样化。补救方向：若确需，为 Anthropic 增加独立 preset 机制并做 Claude Code 兼容性回归。
4. **`anthropic-ratelimit-reset` 解析器支持但未采集**：`QuotaResetTime` 能解析该头，但被动采集白名单只收集 `anthropic-ratelimit-unified-*`、`retry-after` 与 Codex 头，普通 `anthropic-ratelimit-reset` 不会进入 `QuotaState.Signals`。有意取舍：以实际观测到的上游头为准。影响：仅返回旧头的账号无法获得精确重置时间，退化为窗口锚点推导。补救方向：把该头加入 `isQuotaSignalHeaderForProvider` 并补采集/解析回归测试。
5. **management `/v0/management/routing/strategy` 不接受 `reset-priority`**：该端点仍只识别 `round-robin` / `weighted-round-robin` / `fill-first`，设置 `reset-priority` 返回 400，读取时原样回显。有意取舍：该组 `/v0/management` 端点已废弃且不再维护。影响：无法通过该旧端点启用或查看新策略；配置文件与 watcher 热加载路径正常。补救方向：迁移到受支持的 v8/新管理端点，或在废弃文档中明确说明。
6. **网页端 OAuth 可能双开浏览器**：启用 `kunkun-browser` 时，服务端会拉起 KunKun，同时 Web UI 仍会打开后端返回的授权 URL，可能出现两个浏览器窗口/标签。有意取舍：保留 Web UI 既有打开逻辑的兼容性。影响：交互上可能重复打开，但 KunKun 会话仍独立运行。补救方向：前端识别 launcher 已启用并抑制自身打开，或服务端返回显式标志；关闭 `kunkun-browser` 即恢复旧行为。
7. **profile_id 每次登录新生成**：当前 profile 标识为 `provider-<uuid>`，未回写 `Auth.Attributes["kunkun_profile_id"]`，同一账号重新登录不会复用沙盒。有意取舍：登录阶段尚无稳定的账号 ID 绑定时机。影响：重复登录产生新的 profile 目录，磁盘增长且浏览器指纹历史不连续。补救方向：登录成功后把实际 profile_id 落盘到账号属性，并在下次登录时复用。
8. **OAuth 登录后的显式代理优先于代理池**：登录使用的代理会作为 `proxy_url` 落到账号记录，因此该账号持有显式代理；代理池节点故障时不会自动重绑（显式代理永远不被覆盖）。有意取舍：遵守"登录 IP = 运行 IP"的一致性红线。影响：通过代理池登录的账号在节点失效后不会自动切换。补救方向：记录账号的 pool 来源，并在显式代理仍等于历史池节点时允许故障重解析。
9. **真实请求 dial 失败不自动上报熔断**：`proxypool.Manager.ReportDialResult` 已实现，但请求链路尚未调用；健康探针是节点健康的主要输入。有意取舍：避免在请求热路径引入池状态耦合。影响：节点可能在两次探针之间继续被选中。补救方向：在 executor/transport 的 dial 错误处按 pool + node 上报熔断结果。
10. **配置型 API-key 不支持代理池绑定**：`claude-api-key`、`codex-api-key` 等配置型凭据没有 Auth 装载路径，因此无法使用 `proxy_pool_id`；仅 `auths/*.json` 或运行时注册的 Auth 记录支持。有意取舍：配置型凭据的合成路径不携带账号级元数据。影响：多 API-key 账号无法享受池化出网与粘性节点。补救方向：在配置 API-key 合成 Auth 时映射 `proxy_pool_id` 并接入同一解析器。
