# WebSocket Tunnel 客户端与服务端对接技术文档

CLIProxyAPI 内置支持多租户隔离、多对多全双工复用的 **WebSocket Tunnel**（`GET /v1/tunnel`），用于在已认证的**客户端（Client）**与**服务端（Server）**设备之间透明交换任意数据包。CLIProxyAPI 自身不解析、不修改任何业务载荷内容，仅负责基于 `access.api-keys` 的租户隔离、在线设备发现、源身份防伪造校验以及高速数据包路由转发。

---

## 1. 架构模型与核心约束

```text
                +-----------------------------------------+
                |       CLIProxyAPI  (GET /v1/tunnel)     |
                |                                         |
  [Client C1] <===>  租户隔离域 (access.api-keys[0])    <===> [Server S1]
  [Client C2] <===>    - 单设备仅维持 1 条 WS 连接      <===> [Server S2]
                |      - N x M 全双工多路复用交换         |
                +-----------------------------------------+
                |    租户隔离域 (access.api-keys[1])      |
  [Client C3] <===>    (与 [0] 完全隔离、互不可见)        <===> [Server S3]
                +-----------------------------------------+
```

1. **强制 `access.api-keys` 认证与严格租户隔离**：
   - 所有连接必须使用 `config.yaml` 中 `access.api-keys` 配置的密钥完成握手认证。
   - 与普通 AI 代理路由不同，当 `access.api-keys` 未配置（为空）时，`/v1/tunnel` **一律拒绝所有连接**（返回 HTTP `401 Unauthorized`），绝不匿名放行。
   - 不同 `access.api-keys` 下的客户端与服务端**完全互不可见**：不共享在线服务端列表、禁止跨密钥发包、也不共享设备 `uuid` 唯一性命名空间。
2. **设备身份声明（`uuid` + `name` + 服务端 `type`）与唯一性**：
   - 客户端与服务端在连接时均必须声明 `role`、设备唯一标识 `uuid` 与设备名称 `name`；当 `role=server` 时，还必须声明非空的服务端类型 `type`（支持任意自定义输入，至少包含 1 个非空白字符）。
   - 在**同一个 `access.api-key`** 租户域内，同时在线的所有设备（跨 `client` 与 `server`）**不允许 `uuid` 重复**。若某 `uuid` 当前已在线，重复发起的新连接会在 HTTP 握手阶段直接收到 `409 Conflict` 拒绝响应（不会踢掉原有连接）；原连接断开后，相同 `uuid` 可立即重连。
   - 不同设备允许使用相同的设备名称（`name` 可重复）与相同的服务端类型（`type` 可重复）。
3. **单设备单 WebSocket 连接多路复用**：
   - 每个设备仅建立并复用一条 WebSocket 长连接，通过原生帧类型隔离控制面与数据面：
     - **文本帧（`TextMessage`，Opcode `0x1`）**：承载 UTF-8 JSON 控制信令（服务端列表同步、上下线通知及非致命错误反馈）。
     - **二进制帧（`BinaryMessage`，Opcode `0x2`）**：承载二次封装后的业务数据包（4 字节定长包头 + 发送方 `uuid` + 接收方 `uuid` + 原始业务载荷）。
4. **业务载荷完全透明**：
   - 原始数据包可以是任意二进制字节流、加密流、Protobuf、文件分片、UTF-8/JSON 文本或 `0` 字节空信号包，单帧原始载荷上限为 **64 MiB**。

---

## 2. 连接建立与握手鉴权规范

### 2.1 接入端点

```http
GET /v1/tunnel HTTP/1.1
Host: <cliproxyapi-host>:<port>
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Key: <base64-nonce>
Sec-WebSocket-Version: 13
```

未启用 TLS 时使用 `ws://`，启用 `server.tls.enable: true`（或部署在 HTTPS 反向代理后）时使用 `wss://`。

### 2.2 `access.api-keys` 凭证传递方式

CLIProxyAPI 按以下优先级顺序提取候选凭证，并将连接绑定到**第一个非空且命中 `access.api-keys` 配置项的密钥**：

| 优先级 | 凭证来源 | 示例 |
| --- | --- | --- |
| 1 | `Authorization` 请求头 | `Authorization: Bearer sk-your-access-key`（或直接传 `Authorization: sk-your-access-key`） |
| 2 | `X-Goog-Api-Key` 请求头 | `X-Goog-Api-Key: sk-your-access-key` |
| 3 | `X-Api-Key` 请求头 | `X-Api-Key: sk-your-access-key` |
| 4 | URL Query 参数 `key` | `/v1/tunnel?key=sk-your-access-key&...`（适合浏览器原生 `WebSocket` API） |
| 5 | URL Query 参数 `auth_token` | `/v1/tunnel?auth_token=sk-your-access-key&...` |

> **安全说明**：CLIProxyAPI 访问日志已自动对 Query 中的 `key` 与 `auth_token` 脱敏，但在支持自定义握手请求头的 SDK 中仍优先推荐使用 `Authorization: Bearer ...` 或 `X-Api-Key` 请求头。

### 2.3 角色与设备身份声明参数

各字段均优先读取 **URL Query 参数**；若对应 Query 参数缺失或为空白，则自动回退读取同名 **`X-Tunnel-*` 请求头**：

| 参数含义 | Query 参数名 | 回退请求头名 | 校验规则 |
| --- | --- | --- | --- |
| 连接角色 | `role` | `X-Tunnel-Role` | 必填。去除首尾空白后不区分大小写，仅允许 `"client"` 或 `"server"`。 |
| 设备唯一标识 | `uuid` | `X-Tunnel-UUID` | 必填。去除首尾空白后字节长度为 `1..128`，且所有字节必须为可打印无空白 ASCII 字符（`0x21..0x7E`）。同一 `api-key` 下在线唯一。 |
| 设备名称 | `name` | `X-Tunnel-Name` | 必填。去除首尾空白后 UTF-8 字节长度为 `1..256`，必须为合法 UTF-8 且不含 ASCII 控制字符（`0x00..0x1F` 或 `0x7F`）。允许重名。 |
| 服务端类型 | `type` | `X-Tunnel-Type` | 当 `role=server` 时必填（去除首尾空白后至少 1 个字符）；当 `role=client` 时忽略。支持任意自定义输入，去除首尾空白后 UTF-8 字节长度为 `1..256`，必须为合法 UTF-8 且不含 ASCII 控制字符（`0x00..0x1F` 或 `0x7F`）。允许重复。 |

**客户端连接 URL 示例**：

```text
wss://proxy.example.com/v1/tunnel?role=client&uuid=550e8400-e29b-41d4-a716-446655440000&name=MacBook-Pro
```

**服务端连接 URL 示例**：

```text
wss://proxy.example.com/v1/tunnel?role=server&uuid=6ba7b810-9dad-11d1-80b4-00c04fd430c8&name=Build-Node-01&type=build-agent
```

### 2.4 HTTP 握手阶段错误响应码

所有鉴权、参数合法性与 `uuid` 冲突检查均在 WebSocket Upgrade **之前**完成。这些检查失败或 Hub 已关闭时，隧道处理器返回 `{"error":"<message>"}` 形式的 HTTP JSON 错误。全局中间件可能在请求到达隧道处理器之前拒绝请求，并返回不同格式的响应体（包括空响应体），具体区别如下：

| HTTP 状态码 | 触发场景 | 响应体示例 |
| --- | --- | --- |
| `400 Bad Request` | `role`、`uuid`、`name` 缺失或不符合格式限制，或 `role=server` 时 `type` 缺失/不符合格式限制 | `{"error":"invalid or missing role: must be client or server"}` / `{"error":"invalid or missing uuid"}` / `{"error":"invalid or missing name"}` / `{"error":"invalid or missing type"}` |
| `401 Unauthorized` | 系统未配置 `access.api-keys`、未传凭证或所有候选凭证均不匹配 | `{"error":"invalid or missing api key"}` |
| `403 Forbidden` | `access.api-keys` 仍包含示例模板密钥触发系统 Safe-Mode 保护 | `{"error":"unsafe_example_api_key","message":"..."}` |
| `405 Method Not Allowed` | `POST`、`PUT`、`PATCH`、`DELETE`、`HEAD`、`TRACE` 或 `CONNECT`（响应头附带 `Allow: GET`） | `{"error":"method not allowed"}` |
| `409 Conflict` | 同一 `api-key` 下已存在相同 `uuid` 的在线客户端或服务端（或正在握手中） | `{"error":"device uuid already connected"}` |
| `503 Service Unavailable` | 服务停机时 Tunnel Hub 已关闭 | `{"error":"tunnel hub is closed"}` |
| `503 Service Unavailable` | Home 模式下全局 Home 心跳门禁未就绪，请求在到达隧道处理器之前被拒绝 | 空响应体 |

`HEAD` 不返回响应体。`OPTIONS /v1/tunnel` 是 CORS 例外：全局 CORS 中间件对预检请求返回 `204 No Content`，不升级连接，也不执行隧道连接鉴权。

---

## 3. 控制通道协议规范（WebSocket Text JSON 帧）

控制通道统一使用 **WebSocket 文本帧（`Opcode 0x1`）**，内容为 UTF-8 编码的 JSON 对象。整条消息（包括未知字段、嵌套值和字段名）都必须是合法 UTF-8；存在非法字节时返回非致命 `invalid_control_frame`，不执行命令。入站控制消息在解析 JSON 前按所有分片合计限制为 **4,096 字节**（`MaxControlFrameBytes`）；超过时以 **`1009 Message Too Big`** 终止连接，不返回 `error` 帧。入站 `type` 最多 **64 字节**，`request_id` 最多 **128 字节 UTF-8**；在消息总长度限制内违反字段限制时返回非致命 `invalid_control_frame`。未知类型不会被回显到错误消息。出站服务端列表可以超过 4 KiB，但受 §5 的发送字节预算约束。

### 3.1 `server_list`：全量在线服务端列表（CLIProxyAPI → Client）

客户端首次握手注册成功后**收到的第 1 条应用消息为 `server_list`**（若首连列表无法计入发送预算，则改为以 `1008` 关闭，不先发送其他应用消息）；此外客户端主动发送 `list_servers` 查询时也会返回该消息。`servers` 始终为数组（无在线服务端时为 `[]`），按 `uuid` 字典序升序排列。

```json
{
  "type": "server_list",
  "request_id": "sync-01",
  "servers": [
    {
      "uuid": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
      "name": "Build-Node-01",
      "type": "build-agent"
    },
    {
      "uuid": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "name": "Build-Node-02",
      "type": "build-agent"
    }
  ]
}
```

- `servers[].uuid`、`servers[].name`、`servers[].type`（字符串）：在线服务端的唯一标识、设备名称与握手声明的服务端类型。
- `request_id`（可选字符串）：首连自动推送时不包含此字段；响应 `list_servers` 查询且请求带有非空 `request_id` 时原样回显。
- **因果顺序保证**：首连 `server_list` 快照在租户写锁内生成并率先写入客户端发送队列槽位 `0` 后，该客户端才对后续广播可见，因此后续收到的任何 `server_online` / `server_offline` 均严格发生在首连快照之后，不会乱序。

### 3.2 `list_servers`：主动查询在线服务端列表（Client → CLIProxyAPI）

客户端可在连接存续期间随时发送该消息主动刷新列表：

```json
{
  "type": "list_servers",
  "request_id": "sync-01"
}
```

- `request_id`（可选字符串）：`0..128` 字节 UTF-8 字符串，不含 ASCII 控制字符。
- 仅限 `client` 角色调用；若 `server` 角色发送该消息，将收到 `error` 控制帧（`code: "unsupported_role_action"`）。

### 3.3 `server_online` 与 `server_offline`：服务端上下线实时推送（CLIProxyAPI → 同租户所有在线 Client）

当同一 `access.api-key` 下有服务端新接入或断开时，CLIProxyAPI 实时向该密钥下所有在线客户端推送增量通知：

```json
{
  "type": "server_online",
  "server": {
    "uuid": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
    "name": "Build-Node-01",
    "type": "build-agent"
  }
}
```

```json
{
  "type": "server_offline",
  "server": {
    "uuid": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
    "name": "Build-Node-01",
    "type": "build-agent"
  }
}
```

- `server_online` 与 `server_offline` 均携带完整的 `server.uuid`、`server.name` 与 `server.type` 字段。

### 3.4 `error`：非致命控制与路由错误通知（CLIProxyAPI → 发送方 Peer）

消息**未超过传输层大小限制**但格式有误或目标不可达时，CLIProxyAPI 向发送方返回 `error` 文本帧，不会断开当前复用的 WebSocket 长连接。超大消息改为以 `1009` 关闭（见 §4.3）。错误通知为尽力投递；发送方自己的队列或字节预算耗尽时可能丢弃：

```json
{
  "type": "error",
  "code": "target_not_found",
  "target_uuid": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
  "message": "target peer is not connected"
}
```

| 错误码 `code` | 触发原因 |
| --- | --- |
| `invalid_control_frame` | 文本帧任意位置包含非法 UTF-8、不是合法 JSON 对象、`type` 未知/超长、`request_id` 超长/含控制字符。 |
| `unsupported_role_action` | `server` 角色发送了仅限客户端使用的控制指令（如 `list_servers`）。 |
| `invalid_data_frame` | 二进制帧长度小于 4 字节、`SrcUUIDLen`/`DstUUIDLen` 为 `0` 或 `> 128`、UUID 字节截断/含非法字符，或 `RawPayload` 超过 `64 MiB` 但消息总长仍不超过 `64 MiB + 512`；超过后者则以 `1009` 关闭。 |
| `unsupported_version` | 二进制帧第 `0` 字节 `Version` 不等于 `0x01`。 |
| `source_uuid_mismatch` | 二进制帧声明的 `SrcUUID` 与当前连接握手认证绑定的真实 `uuid` 不一致（防伪造拦截）。 |
| `target_not_found` | 目标 `DstUUID` 在当前 `access.api-key` 租户下不存在或已离线。 |
| `invalid_target_role` | 尝试在相同角色设备间发包（禁止 `client → client` 或 `server → server`）。 |
| `target_buffer_full` | 目标设备发送队列帧槽位或单连接/单租户字节预算耗尽。 |

---

## 4. 数据通道二次封装协议规范（WebSocket Binary 帧）

所有客户端与服务端之间的业务数据包均使用 **WebSocket 二进制帧（`Opcode 0x2`）** 传输，采用零 Base64 膨胀的紧凑二进制二次封装头。

### 4.1 二进制帧字节布局

```text
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Version (0x01)|  PayloadType  |  SrcUUIDLen   |  DstUUIDLen   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   SrcUUID (SrcUUIDLen bytes)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   DstUUID (DstUUIDLen bytes)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                RawPayload (0 .. 67,108,864 bytes)             |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| 字节偏移 | 字段名 | 类型 | 说明 |
| --- | --- | --- | --- |
| `0` | `Version` | `uint8` | 协议版本号，固定为 `0x01`。 |
| `1` | `PayloadType` | `uint8` | 应用层载荷类型提示（`0x00` = 二进制，`0x01` = UTF-8 文本，`0x02..0xFF` = 业务自定义扩展子类型）。CLIProxyAPI 原样透传该字节，不校验载荷内容。 |
| `2` | `SrcUUIDLen` | `uint8` | 发送方 `SrcUUID` 的字节长度（合法范围 `1..128`）。 |
| `3` | `DstUUIDLen` | `uint8` | 接收方 `DstUUID` 的字节长度（合法范围 `1..128`）。 |
| `4 .. 4+SrcUUIDLen-1` | `SrcUUID` | ASCII | **客户端 → 服务端**：填入客户端自身 `client_uuid`。<br>**服务端 → 客户端**：填入服务端自身 `server_uuid`。 |
| `4+SrcUUIDLen .. 4+SrcUUIDLen+DstUUIDLen-1` | `DstUUID` | ASCII | **客户端 → 服务端**：填入目标服务端 `server_uuid`。<br>**服务端 → 客户端**：填入目标客户端 `client_uuid`。 |
| `4+SrcUUIDLen+DstUUIDLen .. 帧尾` | `RawPayload` | `[]byte` | 原始业务数据包字节（`0` 字节至最大 `64 MiB` / `67,108,864` 字节）。 |

### 4.2 双向封装与多对多路由规则

1. **客户端 → 服务端方向**：
   - 客户端在发送原始业务数据包时，封装 `SrcUUID = <client_uuid>`、`DstUUID = <server_uuid>`；
   - CLIProxyAPI 校验 `SrcUUID` 必须等于当前客户端连接认证的 `uuid`，并在同一 `api-key` 租户域内确认 `DstUUID` 为在线 `server`，随后直接将该二进制帧原样转发至目标服务端连接；
   - 服务端解封装后即可拿到可信的来源客户端 `client_uuid`（`SrcUUID`）与原始数据包 `RawPayload`。
2. **服务端 → 客户端方向**：
   - 服务端无论是响应请求还是主动向客户端推送流式数据，均封装 `SrcUUID = <server_uuid>`、`DstUUID = <client_uuid>`；
   - CLIProxyAPI 校验 `SrcUUID` 等于当前服务端连接认证的 `uuid`，并在同一 `api-key` 租户域内确认 `DstUUID` 为在线 `client`，随后直接将该二进制帧转发至目标客户端连接；
   - 客户端解封装后即可拿到可信的来源服务端 `server_uuid`（`SrcUUID`）与原始数据包 `RawPayload`。
3. **多对多单连接复用**：
   - 任意客户端可在同一条 WebSocket 连接上并发向多个服务端（`S1`, `S2`, ..., `Sm`）交错收发数据包；
   - 任意服务端可在同一条 WebSocket 连接上并发与多个客户端（`C1`, `C2`, ..., `Cn`）交错收发数据包。

### 4.3 消息大小精确边界

以下长度均为重组后的 WebSocket 消息体（隧道包头与载荷），不含 RFC 6455 帧头/掩码字节；分片不能绕过限制。

| 边界 | 行为 |
| --- | --- |
| 二进制原始载荷 `0..64 MiB`（`67,108,864` 字节）且包头合法 | 通过大小校验，仍受路由与发送预算限制。 |
| 二进制原始载荷 `>64 MiB`，但消息总长 `<=MaxInboundFrameBytes`（`67,109,376` 字节） | 返回非致命 `invalid_data_frame`，连接保持；例如源/目标 UUID 各 1 字节时，`64 MiB + 1` 字节载荷加 6 字节包头属于此情形。 |
| 二进制消息总长 `>MaxInboundFrameBytes = 64 MiB + 512` | 有界读取检测超限后，通过隧道单写协程的统一关闭流程以 **`1009 Message Too Big`** 终止连接，不保证返回 JSON 错误；恰好等于上限时仍进入协议校验（超大载荷被非致命拒绝）。 |
| 入站文本控制消息 `>4,096` 字节 | JSON 解析前以 **`1009 Message Too Big`** 终止连接，不返回 `invalid_control_frame`；恰好 4,096 字节且 JSON/字段合法时允许。 |

### 4.4 传输层致命关闭码

| 关闭码 | 触发条件 |
| --- | --- |
| `1002 Protocol Error` | 非法、未知或保留的 WebSocket opcode、非法关闭码及包含非法 UTF-8 的关闭原因，在隧道消息分发前即被 RFC 6455 传输层拒绝。连接直接终止，不返回非致命 `invalid_control_frame` JSON 错误。 |
| `1009 Message Too Big` | 重组后的消息超过 §4.3 所述传输大小限制。连接直接终止，不返回 JSON 错误。 |

---

## 5. 连接生命周期、保活与热重载行为

- **Ping / Pong 心跳保活**：
  - CLIProxyAPI 默认每 **30 秒** 向在线连接发送一次标准 WebSocket `Ping` 控制帧，且不对空闲长连接施加人为读写超时中断。主流 WebSocket 客户端库均会自动回复 `Pong`。对于收到的 Ping，CLIProxyAPI 使用有界、非阻塞的单槽位，仅保留最新待发送的 Pong 载荷（最多 125 字节），交由单写协程在无写 deadline 的情况下应答。对端主动发送的 Close 帧也通过相同关闭状态机与单写协程处理，以收到的关闭码和空原因应答，不设置写 deadline。
- **传输层 deadline 防护**：
  - 升级成功后，连接包装器会拦截所有有限 `SetDeadline`、`SetReadDeadline` 与 `SetWriteDeadline`，确保它们不会传递到底层连接；空 deadline 仍正常透传。流式检查器在 Gorilla 收到字节前校验入站 RFC 6455 帧头与掩码 Close 载荷，仅保留定长帧头和最多 125 字节的 Close 载荷，不复制数据载荷。致命帧错误立即注销设备、释放 UUID、为服务端广播 `server_offline` 并启动有界本地回收，不等待 Gorilla 写令牌或其 1 秒协议错误写入计时器。若套接字写入已经活跃，则立即关闭原始传输以中断写入；否则交由单写协程发送 `1002`，仅在该关闭写入停滞时使用 1 秒本地回收宽限期。被中断的对端可能看到 `1006`，整个流程不设置套接字 deadline。Gorilla v1.5.3 在发送 `101` 前拒绝 HTTP Hijack 缓冲区中的所有提前数据，因此已接受的升级后入站字节不会绕过检查器。二进制与文本大小限制采用重组消息的有界读取，而非 Gorilla 的 `SetReadLimit`，因此超限消息统一交由隧道单写协程以 `1009` 关闭。
- **配置热重载与密钥吊销（`1008 Policy Violation`）**：
  - 当管理员修改 `config.yaml` 或通过 Management API v8 更新 `access.api-keys` 时，若某个正在使用的 `api-key` 被删除，CLIProxyAPI 会立即注销该密钥下的所有客户端与服务端，并异步请求 WebSocket Close **`1008`（`"api key revoked"`）**；单写协程完成正在进行的健康写入后优先发送关闭帧，丢弃其余排队帧并回收套接字；其他未变更密钥下的活跃连接完全不受影响。
  - 吊销还会立即清除并使该密钥下所有待完成握手的 UUID 预约失效。重新添加相同密钥后，可立即使用相同 UUID 建立新连接；吊销前的旧握手即使随后升级成功，仍收到 `1008`（`"api key revoked"`），其提交或升级失败清理不会删除新的预约或覆盖新连接。
- **服务停机通知（`1001 Going Away`）**：
  - 当 CLIProxyAPI 停机时，立即注销所有在线连接，并通过相同关闭状态机异步请求 WebSocket Close **`1001`（`"server shutting down"`）**。
  - 吊销与停机调用均不等待网络写入。关闭/当前写入阻塞时，**1 秒本地关闭宽限期**结束后强制回收套接字；该机制不设置读写 deadline，也不限制正常活跃连接。健康接收方即使有排队/正在写入的帧仍会收到指定关闭码；真正停滞的对端可能只能看到异常关闭（`1006`），但不能拖延其他连接的吊销或停机。
- **慢消费者背压隔离**：
  - 每个连接拥有独立的 256 帧发送队列。**所有出站 JSON 控制与二进制数据字节（包括正在写入的帧）**共同计入 **单连接 `64 MiB + 512` 字节**与**每个 API Key 租户 `256 MiB`**预算。API 服务使用这些内置默认值构建内部 Hub。`internal/wstunnel.Options`（`SendQueueSize`、`MaxPeerQueuedBytes`、`MaxTenantQueuedBytes`）仅供 CLIProxyAPI 内部构建 Hub 使用，外部 SDK 消费者无法导入或配置，也不暴露为 YAML 配置项。仅在内部构建时，未指定租户预算才取 256 MiB 与单连接预算的较大值。不存在跨租户共享预算，写入完成或帧被丢弃时归还预算。断连与重连期间，正在写入的字节仍计入同一 API Key 的 `256 MiB` 租户预算，直到底层套接字写入完成或套接字被回收。
  - 单连接预算耗尽仅拒绝该目标；同租户仍有总预算时，其他健康节点继续交换。租户预算耗尽仅影响该租户的后续入队，其他租户不受影响。数据入队失败时，仅在发送方自身还有空间时投递 `target_buffer_full`。首连/主动查询 `server_list` 或拓扑广播无法计入接收方帧/字节预算时，请求 `1008`（`"control queue overflow"`）关闭，促使重连重建列表，避免静默失步。

### 客户端推荐对接状态机

1. 使用 `role=client`、`uuid`、`name` 与 `api-key` 连接 `GET /v1/tunnel`。
2. 连接打开后读取首条文本帧 `server_list`，用其初始化本地在线服务端路由表 `Map<uuid, { name, type }>`。
3. 收到 `server_online` 时写入或更新 `server.uuid -> { name: server.name, type: server.type }`；收到 `server_offline` 时移除 `server.uuid` 并快速失败或切换正在等待该服务端的业务请求。
4. 连接断开时：
   - 若收到 WebSocket Close `1008` 且原因为 `"api key revoked"`，停止盲目重连并提示更换有效密钥；
   - 若握手返回 HTTP `409 Conflict`，检查是否存在相同 `uuid` 的重复进程实例，退避后重试；
   - 其他网络闪断或 `1001` 停机重启场景下，按指数退避重连并在连接恢复后以新的首条 `server_list` 重建本地服务端列表。

---

## 6. 客户端与服务端对接参考代码

### 6.1 TypeScript / JavaScript 对接示例（浏览器与 Node.js 通用）

```typescript
const PROTOCOL_VERSION = 0x01;
export const PAYLOAD_BINARY = 0x00;
export const PAYLOAD_TEXT = 0x01;

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

export interface DecodedTunnelPacket {
  version: number;
  payloadType: number;
  srcUUID: string;
  dstUUID: string;
  payload: Uint8Array;
}

export function encodeTunnelPacket(
  payloadType: number,
  srcUUID: string,
  dstUUID: string,
  payload: Uint8Array,
): Uint8Array {
  const srcBytes = textEncoder.encode(srcUUID);
  const dstBytes = textEncoder.encode(dstUUID);
  if (srcBytes.length < 1 || srcBytes.length > 128) {
    throw new Error("invalid srcUUID length");
  }
  if (dstBytes.length < 1 || dstBytes.length > 128) {
    throw new Error("invalid dstUUID length");
  }
  const frame = new Uint8Array(4 + srcBytes.length + dstBytes.length + payload.length);
  frame[0] = PROTOCOL_VERSION;
  frame[1] = payloadType & 0xff;
  frame[2] = srcBytes.length;
  frame[3] = dstBytes.length;
  frame.set(srcBytes, 4);
  frame.set(dstBytes, 4 + srcBytes.length);
  frame.set(payload, 4 + srcBytes.length + dstBytes.length);
  return frame;
}

export function decodeTunnelPacket(raw: ArrayBuffer | Uint8Array): DecodedTunnelPacket {
  const bytes = raw instanceof Uint8Array ? raw : new Uint8Array(raw);
  if (bytes.length < 4) {
    throw new Error("binary frame shorter than 4-byte header");
  }
  const version = bytes[0];
  if (version !== PROTOCOL_VERSION) {
    throw new Error(`unsupported protocol version: ${version}`);
  }
  const payloadType = bytes[1];
  const srcLen = bytes[2];
  const dstLen = bytes[3];
  const headerLen = 4 + srcLen + dstLen;
  if (srcLen < 1 || dstLen < 1 || bytes.length < headerLen) {
    throw new Error("malformed tunnel packet header");
  }
  return {
    version,
    payloadType,
    srcUUID: textDecoder.decode(bytes.subarray(4, 4 + srcLen)),
    dstUUID: textDecoder.decode(bytes.subarray(4 + srcLen, headerLen)),
    payload: bytes.subarray(headerLen),
  };
}

// Client connection and multi-server multiplexing example:
export function connectTunnelClient(baseWsUrl: string, apiKey: string, clientUUID: string, clientName: string) {
  const params = new URLSearchParams({
    key: apiKey,
    role: "client",
    uuid: clientUUID,
    name: clientName,
  });
  const ws = new WebSocket(`${baseWsUrl}/v1/tunnel?${params.toString()}`);
  ws.binaryType = "arraybuffer";

  const servers = new Map<string, { name: string; type: string }>();

  ws.onmessage = (event) => {
    if (typeof event.data === "string") {
      const ctrl = JSON.parse(event.data);
      switch (ctrl.type) {
        case "server_list":
          servers.clear();
          for (const srv of ctrl.servers) {
            servers.set(srv.uuid, { name: srv.name, type: srv.type });
          }
          break;
        case "server_online":
          servers.set(ctrl.server.uuid, { name: ctrl.server.name, type: ctrl.server.type });
          break;
        case "server_offline":
          servers.delete(ctrl.server.uuid);
          break;
        case "error":
          console.warn("Tunnel error:", ctrl.code, ctrl.message, ctrl.target_uuid);
          break;
      }
      return;
    }

    const pkt = decodeTunnelPacket(event.data as ArrayBuffer);
    // pkt.srcUUID is the verified server UUID; pkt.payload is the raw response payload.
    if (pkt.payloadType === PAYLOAD_TEXT) {
      console.log(`收到服务端 ${pkt.srcUUID} 消息:`, textDecoder.decode(pkt.payload));
    }
  };

  return {
    ws,
    servers,
    sendText(serverUUID: string, text: string) {
      ws.send(encodeTunnelPacket(PAYLOAD_TEXT, clientUUID, serverUUID, textEncoder.encode(text)));
    },
    sendBinary(serverUUID: string, data: Uint8Array) {
      ws.send(encodeTunnelPacket(PAYLOAD_BINARY, clientUUID, serverUUID, data));
    },
    refreshServers(requestId = "manual-refresh") {
      ws.send(JSON.stringify({ type: "list_servers", request_id: requestId }));
    },
  };
}
```

### 6.2 Go 服务端节点对接示例

```go
package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
)

const (
	ProtocolVersion1  byte = 0x01
	PayloadTypeBinary byte = 0x00
	PayloadTypeText   byte = 0x01
)

func EncodeFrame(payloadType byte, srcUUID, dstUUID string, payload []byte) ([]byte, error) {
	if len(srcUUID) == 0 || len(srcUUID) > 128 || len(dstUUID) == 0 || len(dstUUID) > 128 {
		return nil, errors.New("uuid length must be between 1 and 128 bytes")
	}
	buf := make([]byte, 0, 4+len(srcUUID)+len(dstUUID)+len(payload))
	buf = append(buf, ProtocolVersion1, payloadType, byte(len(srcUUID)), byte(len(dstUUID)))
	buf = append(buf, srcUUID...)
	buf = append(buf, dstUUID...)
	buf = append(buf, payload...)
	return buf, nil
}

func DecodeFrame(raw []byte) (payloadType byte, srcUUID, dstUUID string, payload []byte, err error) {
	if len(raw) < 4 {
		return 0, "", "", nil, errors.New("short header")
	}
	if raw[0] != ProtocolVersion1 {
		return 0, "", "", nil, fmt.Errorf("unsupported version: %d", raw[0])
	}
	srcLen, dstLen := int(raw[2]), int(raw[3])
	headerLen := 4 + srcLen + dstLen
	if srcLen < 1 || dstLen < 1 || len(raw) < headerLen {
		return 0, "", "", nil, errors.New("malformed header lengths")
	}
	return raw[1], string(raw[4 : 4+srcLen]), string(raw[4+srcLen : headerLen]), raw[headerLen:], nil
}

func RunEchoTunnelServer(proxyWSBaseURL, apiKey, serverUUID, serverName, serverType string) error {
	q := url.Values{
		"role": {"server"},
		"uuid": {serverUUID},
		"name": {serverName},
		"type": {serverType},
	}
	headers := http.Header{
		"Authorization": {"Bearer " + apiKey},
	}
	conn, _, errDial := websocket.DefaultDialer.Dial(proxyWSBaseURL+"/v1/tunnel?"+q.Encode(), headers)
	if errDial != nil {
		return errDial
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			fmt.Printf("websocket close error: %v\n", errClose)
		}
	}()

	var writeMu sync.Mutex
	for {
		msgType, raw, errRead := conn.ReadMessage()
		if errRead != nil {
			return errRead
		}
		if msgType == websocket.TextMessage {
			// Non-fatal control or error notification from CLIProxyAPI
			fmt.Printf("control frame: %s\n", string(raw))
			continue
		}
		if msgType != websocket.BinaryMessage {
			continue
		}

		pType, clientUUID, dstUUID, body, errDec := DecodeFrame(raw)
		if errDec != nil || dstUUID != serverUUID {
			continue
		}

		// Encapsulate serverUUID and reply to the originating client over the same multiplexed connection
		reply, errEnc := EncodeFrame(pType, serverUUID, clientUUID, body)
		if errEnc != nil {
			continue
		}
		writeMu.Lock()
		errWrite := conn.WriteMessage(websocket.BinaryMessage, reply)
		writeMu.Unlock()
		if errWrite != nil {
			return errWrite
		}
	}
}
```
