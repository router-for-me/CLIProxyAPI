# Technical debt: the `-checklinkname=0` build flag

## 1. Current issue

On Go 1.23 and later, building the main service in `cmd/server` without
`-ldflags="-checklinkname=0"` can fail at the linking stage:

```text
link: github.com/wlynxg/anet: invalid reference to net.zoneCache
```

## 2. Root cause and dependency chain

`github.com/wlynxg/anet` contains the following directive:

```go
//go:linkname zoneCache net.zoneCache
```

It bypasses the standard library's encapsulation and links directly to the
unexported global `net.zoneCache`. The dependency chain is:

```text
github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/live
  -> github.com/pion/ice/v4
    -> github.com/pion/transport/v4/stdnet
      -> github.com/wlynxg/anet v0.0.5
```

## 3. Temporary workaround

The [Go 1.23 release notes](https://go.dev/doc/go1.23#linker) describe
`-checklinkname=0` as a way to disable the linker check for debugging and
experimentation, not as a normal production build option.

For now, the Makefile and Dockerfile retain the flag to preserve WebRTC / Codex
Live functionality without forcing a potentially incompatible `pion/ice`
dependency upgrade. This is temporary technical debt.

## 4. Removal plan (roadmap B-4)

1. Upgrade `pion/webrtc` / `pion/ice` to versions that no longer depend on the
   private `anet` linkname, or isolate and refactor the underlying network
   discovery in `internal/client/codex/live`.
2. Remove `-checklinkname=0` from every Makefile, Dockerfile, and build script.
   Acceptance requires a successful standard build on Go 1.23 and later
   without the workaround.
3. Find remaining references with:

   ```bash
   grep -rn "checklinkname" .
   ```
