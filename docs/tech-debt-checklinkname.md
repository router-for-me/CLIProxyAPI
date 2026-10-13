# 技术债务说明：`-checklinkname=0` 构建标志

## 1. 现状与问题描述

在 Go 1.23+（包括当前 Go 1.27.1）环境下，若不显式传入 `-ldflags="-checklinkname=0"`，直接编译主服务 `cmd/server` 会触发以下链接阶段错误并终止构建：

```text
link: github.com/wlynxg/anet: invalid reference to net.zoneCache
```

## 2. 根因追溯与依赖链路

- **直接问题符号**：`github.com/wlynxg/anet` 源码中存在硬编码指令：
  ```go
  //go:linkname zoneCache net.zoneCache
  ```
  该指令绕过 Go 标准库封装，直接链接 `net` 包内部未导出的私有全局符号 `net.zoneCache`。
- **引入依赖链路**：
  ```text
  github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/live
    -> github.com/pion/ice/v4
      -> github.com/pion/transport/v4/stdnet
        -> github.com/wlynxg/anet v0.0.5
  ```

## 3. Go 官方定性与妥协依据

根据 Go 官方对 `go:linkname` 约束强化的说明文档：

> "-checklinkname=0 can be used to disable this check, for debugging and experimenting purposes"

Go 团队明确指出该参数仅用于调试与过渡，**绝非正规生产构建参数**。

在当前阶段，为了保持 WebRTC / Codex Live 实时音视频通信功能完整、同时不强行升级可能带来破坏性变更的 `pion/ice` 依赖，我们**暂时**在 `Makefile` 及相关构建脚本中保留了 `-checklinkname=0`。

## 4. 后续清偿计划（路线图 B-4）

本标志被定性为**必须移除的临时技术债务**：
1. **清偿路径**：在路线图阶段 B-4 中，通过将 `pion/webrtc` / `pion/ice` 升级至移除了 `anet` 私有 linkname 的官方版本，或剥离/重构 `internal/client/codex/live` 的底层网络探测实现。
2. **验收标准**：彻底移除所有 Makefile、Dockerfile 及脚本中的 `-checklinkname=0`，在 Go 1.23+ 标准编译器下原生无报错编译。
3. **全局检索定位**：后续开发者可通过以下命令快速定位所有关联点：
   ```bash
   grep -rn "checklinkname" .
   ```
