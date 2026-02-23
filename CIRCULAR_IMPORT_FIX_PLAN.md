# Plan: Fix Circular Import Between Config Packages

## Problem Analysis

### Current State
```
pkg/llmproxy/config (struct SDKConfig, functions)
    ↑
    └── imports ──→ sdk/config (aliases to internal/config)
    ↓
internal/config (struct SDKConfig)

sdk/config (aliases to internal/config)
    ↑
    └── imports ──→ pkg/llmproxy/config
```

### Issue
- `pkg/llmproxy/config` and `sdk/config` both try to alias to `internal/config`
- But internal code imports one or the other expecting specific struct definitions
- Creates type mismatches when functions expect struct vs alias

### Files Affected
1. `pkg/llmproxy/config/config.go` - Has full Config/SDKConfig structs
2. `pkg/llmproxy/config/sdk_config.go` - Aliases to internal/config
3. `internal/config/config.go` - Full structs + functions
4. `internal/config/sdk_config.go` - Struct definitions
5. `sdk/config/config.go` - Aliases to internal/config
6. ~50+ files importing these packages get type mismatches

---

## Solution: Unified Config Package

### Approach
Make `internal/config` the **single source of truth** for all config types. Both `pkg/llmproxy/config` and `sdk/config` re-export from internal.

### Step-by-Step Plan

#### Step 1: Verify internal/config has everything
- [ ] Check `internal/config/config.go` has all types (Config, SDKConfig, etc.)
- [ ] Check `internal/config/` has all helper functions (LoadConfig, NormalizeHeaders, etc.)
- [ ] List all type definitions needed

#### Step 2: Update pkg/llmproxy/config to re-export from internal
- [ ] Create `pkg/llmproxy/config/config_internal.go` that re-exports all types from internal
- [ ] Keep only type aliases, no struct definitions
- [ ] Remove duplicate function definitions
- [ ] Add imports re-export wrapper

#### Step 3: Update sdk/config to re-export from internal  
- [ ] Verify sdk/config uses aliases to internal/config
- [ ] Add missing type re-exports

#### Step 4: Fix any remaining import issues
- [ ] Find packages importing wrong config package
- [ ] Update imports to use internal/config for internal code, sdk/config for external SDK users

#### Step 5: Verify build and tests
- [ ] `go build ./...`
- [ ] `go vet ./...`
- [ ] Run key test suites

---

## Detailed File Changes

### pkg/llmproxy/config/config.go
Current: Has full struct definitions + functions
Target: Re-export everything from internal/config

```go
// Add at top of file:
import internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"

// Replace all type definitions with aliases:
type SDKConfig = internalconfig.SDKConfig
type Config = internalconfig.Config
// ... all other types

// Replace function implementations with re-exports:
func LoadConfig(...) = internalconfig.LoadConfig
func LoadConfigOptional(...) = internalconfig.LoadConfigOptional
// ... all other functions
```

### pkg/llmproxy/config/sdk_config.go
Current: Aliases to internal/config
Target: Delete this file (merge into config.go)

### sdk/config/config.go  
Current: Aliases to internal/config
Target: Already correct, verify no changes needed

---

## Risk Mitigation

### If Issues Arise
1. **Type mismatch in function signatures** - Add explicit type casts
2. **Missing methods on aliased types** - Methods on embedded types should work via alias
3. **YAML tags** - Type aliases preserve tags from source struct

### Rollback Plan
- Keep backup branch
- Can revert to pre-plan state if issues occur

---

## Timeline Estimate
- Step 1-2: 15 min
- Step 3: 10 min  
- Step 4: 15 min
- Step 5: 10 min
- **Total: ~50 minutes**

---

## Success Criteria
- `go build ./...` passes
- `go vet ./...` passes  
- SDK tests pass
- Internal tests pass
- No runtime behavior changes
