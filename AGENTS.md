# AGENTS.md — Known Pitfalls & Solutions

Auto-generated knowledge base for AI agents and developers.
Report unexpected issues here so future collaborators don't step into the same trap.

---

### [Go Toolchain] go1.26.4 auto-downloaded toolchain cache is incomplete (missing src/)

**Date**: 2026-06-14  
**Discovered by**: commit `6121199d` build attempt

**Problem Description**:

The project's `go.mod` specifies `go 1.26.0` with `toolchain go1.26.4`. When `GOTOOLCHAIN=auto` (default), Go attempts to auto-download `go1.26.4` to the module cache at:
```
%GOPATH%\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.4.windows-amd64
```

However, due to network restrictions (can't reach `proxy.golang.org`), the download is **incomplete** — the cache only contains `bin/go.exe` without the `src/` directory (standard library source files). This causes **all** compilation to fail with dozens of errors like:

```
package net/http is not in std (C:\Users\...\toolchain@v0.0.1-go1.26.4.windows-amd64\src\net\http)
package encoding/json is not in std (...)
package sync is not in std (...)
... (all standard library packages fail)
```

**Impact**: Any `go build`, `go test`, `go mod tidy`, etc. will fail. The system-installed Go at `C:\Program Files\Go` (go1.26.2) has the full standard library, but `GOTOOLCHAIN=auto` causes Go to prefer the broken cached toolchain.

**Root Cause Summary**:

| Component | Value |
|-----------|-------|
| go.mod `toolchain` directive | `go1.26.4` |
| System-installed Go | `go1.26.2` (at `C:\Program Files\Go`) |
| `GOTOOLCHAIN` env (Process) | `auto` (may override go env config) |
| `GOTOOLCHAIN` go env config | `local` (in `%APPDATA%\go\env`) |
| Network access to `proxy.golang.org` | Blocked |
| Cached toolchain | Incomplete — only `bin/`, no `src/` |

**Permanent Fix (applied)**:

1. **`build-optimized.ps1`** now sets `$env:GOTOOLCHAIN = "local"` at the top and validates `$GOROOT\src\net\http` exists before building.
2. **`go env -w GOTOOLCHAIN=local`** has been set in `%APPDATA%\go\env`. However, this can be overridden by a process-level `GOTOOLCHAIN` environment variable — the build script handles this explicitly.

**Quick Fix if it happens again**:

```powershell
# Delete the broken toolchain cache
Remove-Item -Recurse -Force "$env:GOPATH\pkg\mod\golang.org\toolchain@*"

# Force using system Go
$env:GOTOOLCHAIN = "local"
go build ./...
```

**Long-term fix** (when network is available):

```powershell
# Install go1.26.4 properly
go install golang.org/dl/go1.26.4@latest
go1.26.4 download

# Or download the MSI from https://go.dev/dl/ and install manually
```