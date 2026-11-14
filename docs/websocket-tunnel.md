# WebSocket Tunnel Integration Guide

CLIProxyAPI provides a built-in, multi-tenant, many-to-many **WebSocket Tunnel** (`GET /v1/tunnel`) that relays opaque packets between authenticated **Client** and **Server** devices. CLIProxyAPI acts strictly as a zero-inspection packet switch: it enforces `access.api-keys` tenant isolation, tracks online device topology, verifies sender identity headers, and forwards payloads without inspecting or mutating business content.

---

## 1. Architecture & Core Guarantees

```text
                +-----------------------------------------+
                |       CLIProxyAPI  (GET /v1/tunnel)     |
                |                                         |
  [Client C1] <===>  Tenant Realm (access.api-keys[0])  <===> [Server S1]
  [Client C2] <===>    - Single WS connection per peer  <===> [Server S2]
                |      - Full-duplex N x M multiplexing   |
                +-----------------------------------------+
                |    Tenant Realm (access.api-keys[1])    |
  [Client C3] <===>    (Completely invisible to [0])    <===> [Server S3]
                +-----------------------------------------+
```

1. **Mandatory `access.api-keys` Authentication & Tenant Isolation**:
   - Every connection must authenticate against an entry configured under `access.api-keys` in `config.yaml`.
   - Unlike AI proxy endpoints that may allow unauthenticated access when `access.api-keys` is empty, `/v1/tunnel` **always rejects** connections (`401 Unauthorized`) if `access.api-keys` is empty or unmatched.
   - Peers authenticated under different `access.api-keys` are **completely invisible** to one another. They do not share server lists, cannot route packets across keys, and do not share UUID uniqueness namespaces.
2. **Device Identity (`uuid` + `name` + Server `type`) & Uniqueness**:
   - Every connecting peer (both `client` and `server`) must declare its `role`, `uuid`, and `name`. Connecting `server` peers must additionally declare a non-empty `type` (arbitrary free-form input, at least 1 non-whitespace character).
   - Within the same `access.api-key` realm, **`uuid` is strictly unique across all connected clients and servers**. Duplicate online UUIDs are rejected during the HTTP handshake with `409 Conflict` (existing connections are never kicked). Once the previous connection closes, the same `uuid` may reconnect immediately.
   - **`name` and server `type` may be duplicated** across multiple devices.
3. **Single Multiplexed WebSocket Connection per Device**:
   - Each device maintains exactly one WebSocket connection and multiplexes all control events and multi-peer data streams over it:
     - **Text frames (`Opcode 0x1`)** carry UTF-8 JSON control messages (discovery & non-fatal routing errors).
     - **Binary frames (`Opcode 0x2`)** carry secondary-encapsulated business packets (4-byte fixed header + source UUID + destination UUID + opaque raw payload).
4. **Payload Agnosticism**:
   - Raw payloads can be arbitrary binary streams, encrypted frames, Protobuf messages, UTF-8/JSON text, or empty (`0`-byte) signals up to **64 MiB** per frame.

---

## 2. Connecting & Handshake Authentication

### 2.1 Endpoint

```http
GET /v1/tunnel HTTP/1.1
Host: <cliproxyapi-host>:<port>
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Key: <base64-nonce>
Sec-WebSocket-Version: 13
```

Use `ws://` when TLS is disabled on CLIProxyAPI and `wss://` when `server.tls.enable: true` (or behind a TLS reverse proxy).

### 2.2 Supplying the `access.api-keys` Credential

CLIProxyAPI inspects candidate credentials in the following priority order and binds the connection to the **first non-empty candidate that matches a configured `access.api-keys` entry**:

| Priority | Source | Example |
| --- | --- | --- |
| 1 | `Authorization` header | `Authorization: Bearer sk-your-access-key` (or raw `Authorization: sk-your-access-key`) |
| 2 | `X-Goog-Api-Key` header | `X-Goog-Api-Key: sk-your-access-key` |
| 3 | `X-Api-Key` header | `X-Api-Key: sk-your-access-key` |
| 4 | URL Query `key` | `/v1/tunnel?key=sk-your-access-key&...` (recommended for browser `WebSocket` clients) |
| 5 | URL Query `auth_token` | `/v1/tunnel?auth_token=sk-your-access-key&...` |

> **Security Note**: Query parameters `key` and `auth_token` are automatically masked in CLIProxyAPI access logs, but HTTP headers (`Authorization: Bearer ...` or `X-Api-Key`) remain preferred whenever the client WebSocket SDK supports custom handshake headers.

### 2.3 Declaring Role, UUID, Device Name, and Server Type

Each parameter is read from the **URL Query** first; if omitted or blank in the query string, CLIProxyAPI falls back to the corresponding **`X-Tunnel-*` HTTP header**:

| Parameter | Query Key | Fallback Header | Constraints |
| --- | --- | --- | --- |
| Role | `role` | `X-Tunnel-Role` | Required. Case-insensitive `"client"` or `"server"` (surrounding whitespace trimmed). |
| Device UUID | `uuid` | `X-Tunnel-UUID` | Required. `1..128` printable non-whitespace ASCII bytes (`0x21..0x7E`) after trimming surrounding whitespace. Must be unique among active connections under the same `api-key`. |
| Device Name | `name` | `X-Tunnel-Name` | Required. `1..256` UTF-8 bytes after trimming surrounding whitespace. Must be valid UTF-8 and must not contain ASCII control characters (`0x00..0x1F` or `0x7F`). Duplicate names are allowed. |
| Server Type | `type` | `X-Tunnel-Type` | Required when `role=server` (at least 1 non-whitespace character); ignored when `role=client`. Arbitrary free-form input, `1..256` UTF-8 bytes after trimming surrounding whitespace. Must be valid UTF-8 and must not contain ASCII control characters (`0x00..0x1F` or `0x7F`). Duplicate types are allowed. |

**Example Handshake URL (Client)**:

```text
wss://proxy.example.com/v1/tunnel?role=client&uuid=550e8400-e29b-41d4-a716-446655440000&name=MacBook-Pro
```

**Example Handshake URL (Server)**:

```text
wss://proxy.example.com/v1/tunnel?role=server&uuid=6ba7b810-9dad-11d1-80b4-00c04fd430c8&name=Build-Node-01&type=build-agent
```

### 2.4 HTTP Handshake Error Responses

All authentication, validation, and conflict checks run **before** upgrading the connection. The tunnel handler returns HTTP JSON errors in the form `{"error":"<message>"}` for these checks and for a closed hub. Global middleware can reject the request earlier with a different response body (including an empty body), as distinguished below:

| HTTP Status | Condition | Response Body |
| --- | --- | --- |
| `400 Bad Request` | Invalid/missing `role`, `uuid`, `name`, or (when `role=server`) `type` | `{"error":"invalid or missing role: must be client or server"}` / `{"error":"invalid or missing uuid"}` / `{"error":"invalid or missing name"}` / `{"error":"invalid or missing type"}` |
| `401 Unauthorized` | `access.api-keys` is empty, credential missing, or no candidate matches | `{"error":"invalid or missing api key"}` |
| `403 Forbidden` | Safe-mode active because `access.api-keys` still contains template placeholder keys | `{"error":"unsafe_example_api_key","message":"..."}` |
| `405 Method Not Allowed` | `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, `TRACE`, or `CONNECT` (includes `Allow: GET` header) | `{"error":"method not allowed"}` |
| `409 Conflict` | Another client or server with the same `uuid` is already online (or upgrading) under the same `api-key` | `{"error":"device uuid already connected"}` |
| `503 Service Unavailable` | Tunnel hub is closed during server shutdown | `{"error":"tunnel hub is closed"}` |
| `503 Service Unavailable` | Global Home heartbeat gate is unhealthy in Home mode; request is rejected before reaching the tunnel handler | Empty body |

`HEAD` returns no response body. `OPTIONS /v1/tunnel` is the CORS exception: global CORS middleware returns `204 No Content` for preflight requests without upgrading or authenticating a tunnel connection.

---

## 3. Control Channel Protocol (WebSocket Text Frames)

All control frames use **WebSocket Text Messages (`Opcode 0x1`)** encoded as UTF-8 JSON objects. The entire message, including unknown fields, nested values, and field names, must be valid UTF-8; invalid bytes return non-fatal `invalid_control_frame` without executing the command. Inbound control messages are limited to **4,096 bytes** (`MaxControlFrameBytes`), across all fragments, before JSON parsing. Larger controls terminate the connection with **`1009 Message Too Big`**, not an `error` frame. The inbound `type` is limited to **64 bytes** and `request_id` to **128 UTF-8 bytes**; field violations within the message-size limit return non-fatal `invalid_control_frame`. Unknown types are not echoed into error messages. Outbound server snapshots may exceed 4 KiB and are governed by the outbound budgets in §5.

### 3.1 `server_list` (CLIProxyAPI → Client)

Delivered **as the very first application message** when a `client` connects (if the initial snapshot cannot fit the outbound budgets, the connection instead closes with `1008` before other application messages), and also sent in response to a client's `list_servers` request. The `servers` array is always non-null (`[]` when empty) and sorted lexicographically by `uuid`.

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

- `servers[].uuid`, `servers[].name`, `servers[].type` (`string`): Each online server's unique identifier, display name, and declared server type.
- `request_id` (`string`, optional): Omitted on the automatic initial push; echoed verbatim when replying to `list_servers` if the request included a non-empty `request_id`.
- **Causal Ordering Guarantee**: The initial `server_list` snapshot is enqueued atomically under the tenant lock before the client becomes visible to subsequent broadcasts. Any `server_online` or `server_offline` event received after the initial `server_list` strictly reflects state transitions occurring after that snapshot.

### 3.2 `list_servers` (Client → CLIProxyAPI)

Clients may query the latest server list at any time over the existing connection:

```json
{
  "type": "list_servers",
  "request_id": "sync-01"
}
```

- `request_id` (`string`, optional): `0..128` UTF-8 bytes without ASCII control characters.
- Only `client` peers may send `list_servers`. If a `server` peer sends `list_servers`, CLIProxyAPI replies with an `error` frame (`code: "unsupported_role_action"`).

### 3.3 `server_online` & `server_offline` (CLIProxyAPI → All Online Clients in Same Tenant)

Broadcast in real time whenever a `server` connects to or disconnects from the same `access.api-key` realm:

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

- Both `server_online` and `server_offline` carry `server.uuid`, `server.name`, and `server.type`.

### 3.4 `error` (CLIProxyAPI → Sender Peer)

Reports a non-fatal protocol or routing error for an individual message **within transport size limits**, without closing the multiplexed WebSocket connection. Oversized messages instead close with `1009` (see §4.3). Errors are best-effort: they may be dropped if the sender's own outbound queue or byte budget is exhausted:

```json
{
  "type": "error",
  "code": "target_not_found",
  "target_uuid": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
  "message": "target peer is not connected"
}
```

| Error `code` | Trigger Condition |
| --- | --- |
| `invalid_control_frame` | Text frame contains invalid UTF-8 anywhere, is not valid JSON, is not an object, has an unknown/oversized `type`, or has an invalid `request_id`. |
| `unsupported_role_action` | A `server` peer sent a client-only control command (`list_servers`). |
| `invalid_data_frame` | Binary frame is shorter than 4 bytes, `SrcUUIDLen`/`DstUUIDLen` is `0` or `> 128`, UUID bytes are truncated or contain invalid ASCII, or `RawPayload` exceeds `64 MiB` while total message bytes remain at or below `64 MiB + 512`. Larger messages close with `1009`. |
| `unsupported_version` | Binary frame byte `0` (`Version`) is not `0x01`. |
| `source_uuid_mismatch` | Binary frame `SrcUUID` does not match the sender connection's authenticated `uuid`. |
| `target_not_found` | `DstUUID` is not currently connected under the same `access.api-key` realm. |
| `invalid_target_role` | Attempted same-role routing (`client → client` or `server → server`). |
| `target_buffer_full` | Target peer's outbound frame slots or per-peer/per-tenant byte budget is exhausted. |

---

## 4. Data Channel Secondary Encapsulation Protocol (WebSocket Binary Frames)

All Client ↔ Server business packets are exchanged as **WebSocket Binary Messages (`Opcode 0x2`)** using a compact, zero-Base64 binary header:

### 4.1 Wire Frame Layout

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

| Byte Offset | Field | Type | Description |
| --- | --- | --- | --- |
| `0` | `Version` | `uint8` | Must be `0x01`. |
| `1` | `PayloadType` | `uint8` | Application-level payload hint (`0x00` = binary, `0x01` = UTF-8 text, `0x02..0xFF` = custom application sub-types). Forwarded verbatim without inspection. |
| `2` | `SrcUUIDLen` | `uint8` | Byte length of `SrcUUID` (`1..128`). |
| `3` | `DstUUIDLen` | `uint8` | Byte length of `DstUUID` (`1..128`). |
| `4 .. 4+SrcUUIDLen-1` | `SrcUUID` | ASCII | **Client → Server**: sender's `client_uuid`.<br>**Server → Client**: sender's `server_uuid`. |
| `4+SrcUUIDLen .. 4+SrcUUIDLen+DstUUIDLen-1` | `DstUUID` | ASCII | **Client → Server**: target `server_uuid`.<br>**Server → Client**: target `client_uuid`. |
| `4+SrcUUIDLen+DstUUIDLen .. end` | `RawPayload` | `[]byte` | Opaque business packet (`0` bytes up to `64 MiB` / `67,108,864` bytes). |

### 4.2 Routing & Anti-Spoofing Rules

1. **Client → Server Encapsulation**:
   - The client encapsulates its raw payload with `SrcUUID = <client_uuid>` and `DstUUID = <server_uuid>`.
   - CLIProxyAPI verifies `SrcUUID == authenticated_client.uuid` and confirms `DstUUID` is an online `server` in the same `api-key` realm, then forwards the binary frame to that server.
   - The server decapsulates the frame, obtaining the verified `client_uuid` (`SrcUUID`) and `RawPayload`.
2. **Server → Client Encapsulation**:
   - To reply or push unsolicited data to any connected client, the server encapsulates its raw payload with `SrcUUID = <server_uuid>` and `DstUUID = <client_uuid>`.
   - CLIProxyAPI verifies `SrcUUID == authenticated_server.uuid` and confirms `DstUUID` is an online `client` in the same `api-key` realm, then forwards the binary frame to that client.
   - The client decapsulates the frame, obtaining the verified `server_uuid` (`SrcUUID`) and `RawPayload`.
3. **Many-to-Many Multiplexing**:
   - A single client can interleave frames to `S1`, `S2`, ..., `Sm` on its single WebSocket connection.
   - A single server can interleave frames to `C1`, `C2`, ..., `Cn` on its single WebSocket connection.

### 4.3 Message Size Boundaries

Sizes refer to the reassembled WebSocket message body (the tunnel header and payload), excluding RFC 6455 framing/masking bytes. Fragmentation does not bypass limits.

| Boundary | Behavior |
| --- | --- |
| Binary raw payload `0..64 MiB` (`67,108,864` bytes), valid header | Accepted, subject to routing and outbound budgets. |
| Binary raw payload `>64 MiB`, total message `<=MaxInboundFrameBytes` (`67,109,376` bytes) | Non-fatal `invalid_data_frame`; connection stays open. For 1-byte source/destination UUIDs, a `64 MiB + 1` payload has a 6-byte header and falls here. |
| Any binary message `>MaxInboundFrameBytes = 64 MiB + 512` | Bounded reading detects the excess and requests **`1009 Message Too Big`** through the tunnel’s single-writer close flow; no JSON error is promised. At exactly the limit the message reaches protocol validation (and an oversized payload is rejected non-fatally). |
| Inbound text control message `>4,096` bytes | Terminates with **`1009 Message Too Big`** before JSON parsing; no `invalid_control_frame` response. Exactly 4,096 bytes is allowed if valid. |

### 4.4 Fatal Transport Close Codes

| Close Code | Trigger Condition |
| --- | --- |
| `1002 Protocol Error` | Invalid, unknown, or reserved WebSocket opcodes, invalid close codes, and invalid UTF-8 close reasons are rejected by the RFC 6455 transport layer before tunnel message dispatch. The connection terminates; no non-fatal `invalid_control_frame` JSON response is sent. |
| `1009 Message Too Big` | A reassembled message exceeds a transport size limit described in §4.3. The connection terminates instead of returning a JSON error. |

---

## 5. Connection Lifecycle, Keepalive & Hot-Reload Behavior

- **Heartbeat (Ping / Pong)**:
  - CLIProxyAPI sends a WebSocket `Ping` control frame every **30 seconds** and imposes no artificial read/write timeouts on idle connections. Standard WebSocket libraries (browser `WebSocket`, `gorilla/websocket`, `ws`, URLSession, Tungstite) automatically reply with `Pong`. For incoming Pings, CLIProxyAPI keeps only the latest pending Pong payload (at most 125 bytes) in a bounded, non-blocking slot; its single writer sends Pongs with no write deadline. Peer-initiated Close frames use the same close state machine and single writer, replying with the received code and an empty reason without a write deadline.
- **Transport Deadline Protection**:
  - After a successful upgrade, a connection wrapper suppresses all finite `SetDeadline`, `SetReadDeadline`, and `SetWriteDeadline` calls before they reach the underlying connection. Zero deadlines still pass through. A streaming inspector checks incoming RFC 6455 headers and masked Close payloads before Gorilla receives them, retaining only a fixed-size header and at most 125 Close payload bytes, without copying data payloads. Fatal framing errors immediately unregister the peer, free its UUID, broadcast `server_offline` for a server, and start bounded local reclamation, without waiting for Gorilla’s write token or its 1-second protocol-error write timer. An already active socket write is interrupted by immediately closing the raw transport; otherwise, the single writer sends `1002`, with a 1-second local grace period if that close stalls. Interrupted peers may observe `1006`; no socket deadline is installed. Gorilla v1.5.3 rejects any pipelined bytes buffered by HTTP Hijack before sending `101`, so accepted post-upgrade input cannot bypass the inspector. Binary and text size limits use bounded reassembled-message reads rather than Gorilla’s `SetReadLimit`, so oversized messages close with `1009` through the tunnel’s single writer.
- **Configuration Hot-Reload (`access.api-keys` Revocation)**:
  - When `config.yaml` is reloaded or updated via Management API v8, if an `access.api-key` is removed, CLIProxyAPI immediately unregisters all client and server connections under that key and requests WebSocket Close **`1008` (`ClosePolicyViolation`, `"api key revoked"`)** asynchronously. The single writer finishes any active healthy write, prioritizes the close frame, discards remaining queued frames, and closes the socket. Connections under unchanged keys continue uninterrupted.
  - Revocation also immediately invalidates and clears all pending UUID reservations. Re-adding the same key allows a fresh connection with the same UUID immediately. Any pre-revocation handshake that later finishes upgrading still receives `1008` (`"api key revoked"`); its commit or failed-upgrade cleanup cannot remove a newer reservation or replace the new connection.
- **Server Shutdown**:
  - When CLIProxyAPI stops, it immediately unregisters active tunnel connections and asynchronously requests WebSocket Close **`1001` (`CloseGoingAway`, `"server shutting down"`)** using the same close state machine.
  - Revocation and shutdown never wait for socket writes. A **1-second local close grace period** forces socket reclamation if the close/active write stalls; this installs no read/write deadlines and does not limit active connections. Healthy readers receive the requested close code even with queued/active writes; genuinely stalled peers may observe abnormal closure (`1006`) instead. This fallback cannot delay revocation or shutdown of other peers.
- **Slow-Consumer Protection**:
  - Each connection has an isolated 256-frame outbound queue. **All outbound JSON control and binary data bytes, including in-flight writes**, count toward a **per-peer budget of `64 MiB + 512` bytes** and a **per-API-key tenant budget of `256 MiB`**. The API server constructs its internal Hub with these built-in defaults. `internal/wstunnel.Options` (`SendQueueSize`, `MaxPeerQueuedBytes`, `MaxTenantQueuedBytes`) is only for internal Hub construction within CLIProxyAPI; it is not importable or configurable by external SDK consumers, nor exposed as YAML settings. For internal construction only, an unspecified tenant limit defaults to the larger of 256 MiB and the per-peer limit. There is no shared cross-tenant budget. Reservations are released after writing or discarding frames. In-flight write bytes remain charged to the API key's `256 MiB` tenant budget across disconnects and reconnects until the underlying socket write drains or is reclaimed.
  - Per-peer exhaustion rejects only that target, so healthy peers in the same tenant continue exchanging while tenant budget remains available. Tenant-wide exhaustion rejects further enqueues only within that tenant; other tenants remain unaffected. Rejected data sends produce `{"type":"error","code":"target_buffer_full","target_uuid":"..."}` when the sender has room for the error. If an initial/queried `server_list` or a topology broadcast cannot fit its recipient's frame/byte budgets, CLIProxyAPI requests `1008` (`"control queue overflow"`), forcing resynchronization after reconnect rather than silently losing topology state.

### Recommended Client State Machine

1. Connect to `GET /v1/tunnel` with `role=client`, `uuid`, `name`, and `api-key`.
2. On open, wait for the first text frame (`type == "server_list"`) to initialize the local `Map<uuid, { name, type }>` of online servers.
3. On `server_online`, insert `msg.server.uuid -> { name: msg.server.name, type: msg.server.type }`.
4. On `server_offline`, remove `msg.server.uuid` and fail/retry any pending application requests targeting that server.
5. On disconnect:
   - If close code is `1008` (`api key revoked`), stop retrying until credentials are updated;
   - If HTTP handshake returned `409 Conflict`, ensure no duplicate instance shares the same `uuid`, then retry with exponential backoff;
   - Otherwise, reconnect with exponential backoff and rebuild local server state from the new initial `server_list`.

---

## 6. Reference Implementation Examples

### 6.1 TypeScript / JavaScript (Browser & Node.js)

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

// Browser / Node 22+ Client Usage Example:
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
    // pkt.srcUUID is the verified server UUID; pkt.payload is the raw response bytes.
    if (pkt.payloadType === PAYLOAD_TEXT) {
      console.log(`Message from server ${pkt.srcUUID}:`, textDecoder.decode(pkt.payload));
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

### 6.2 Go Server Peer Example

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
			// Non-fatal control/error notifications from CLIProxyAPI
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

		// Reply to the originating client over the same multiplexed connection.
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
