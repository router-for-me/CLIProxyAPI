# CLIProxyAPI Parameter Filtering System - Architecture Documentation

## Executive Summary

CLIProxyAPI implements a configuration-driven **payload filtering system** that allows administrators to:
- Set default parameters (only if missing)
- Override existing parameters
- **Filter/remove parameters** that incompatible providers don't support
- Apply these rules selectively based on model name patterns and target protocol

The filtering happens **after request translation** and **before sending to upstream providers**, ensuring that provider-specific constraints are respected.

---

## 1. Architecture Overview

### High-Level Request Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ Client Request (OpenAI/Gemini/Claude Format)                                 │
└──────────────────────────────────────────────────────────────────────────────┘
                                    ↓
                    ┌───────────────────────────────┐
                    │ API Handler (route-specific)   │
                    │ - Authenticate                 │
                    │ - Extract model name           │
                    │ - Determine source protocol    │
                    └───────────────────────────────┘
                                    ↓
                    ┌───────────────────────────────┐
                    │ Request Translation             │
                    │ source_format → target_format  │
                    └───────────────────────────────┘
                                    ↓
    ╔═══════════════════════════════════════════════════════════════╗
    ║ *** PAYLOAD FILTERING (ApplyPayloadConfigWithRoot) ***       ║
    ║                                                               ║
    ║ 1. Apply Default Rules (first-write-wins)                   ║
    ║ 2. Apply DefaultRaw Rules (JSON values)                     ║
    ║ 3. Apply Override Rules (last-write-wins)                  ║
    ║ 4. Apply OverrideRaw Rules (JSON values)                   ║
    ║ 5. Apply Filter Rules (REMOVE JSON paths)   ← KEY FEATURE ║
    ║                                                               ║
    ║ All rules match based on:                                   ║
    ║ - Model name patterns (wildcards: * = zero+ chars)         ║
    ║ - Target protocol (e.g., "gemini", "claude", "openai")    ║
    ║ - Requested model (pre-alias resolution)                   ║
    ╚═══════════════════════════════════════════════════════════════╝
                                    ↓
                    ┌───────────────────────────────┐
                    │ Provider-Specific Executor      │
                    │ - Claude                        │
                    │ - Gemini                        │
                    │ - Codex                         │
                    │ - Antigravity                   │
                    │ - OpenAI Compatibility         │
                    └───────────────────────────────┘
                                    ↓
┌──────────────────────────────────────────────────────────────────────────────┐
│ Upstream Provider API Call (with filtered parameters)                         │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Key Implementation File

**Location:** `/internal/runtime/executor/helps/payload_helpers.go`  
**Function:** `ApplyPayloadConfigWithRoot()`

---

## 2. Detailed Filtering Mechanism

### 2.1 Rule Execution Order (Sequential)

The system applies rules in a strict sequence. **This order is critical** because:
- Defaults use "first-write-wins" (first matching rule that sets a field wins)
- Overrides use "last-write-wins" (last matching rule overwrites previous)
- Filters always remove matching paths

```
1. Default Rules (PayloadConfig.Default)
   └─ Check if path missing in ORIGINAL payload
   └─ If missing, set value from rule
   └─ FIRST RULE WINS per field (subsequent rules skip)

2. DefaultRaw Rules (PayloadConfig.DefaultRaw)
   └─ Same as Default, but treats value as raw JSON
   └─ Value must be valid JSON string/bytes

3. Override Rules (PayloadConfig.Override)
   └─ Always overwrite path, regardless if it exists
   └─ LAST RULE WINS per field (later rules override earlier)

4. OverrideRaw Rules (PayloadConfig.OverrideRaw)
   └─ Same as Override, but treats value as raw JSON

5. Filter Rules (PayloadConfig.Filter) ← REMOVES PARAMETERS
   └─ Delete matching JSON paths from payload
   └─ Executed last, after all additions/modifications
   └─ Target providers that don't support certain parameters
```

### 2.2 Rule Matching Logic

Each rule matches based on three criteria:

```go
struct PayloadFilterRule {
    Models []PayloadModelRule  // WHO to match
    Params []string            // WHAT to remove (JSON paths)
}

struct PayloadModelRule {
    Name     string  // Model name pattern (with * wildcards)
    Protocol string  // Target protocol: "openai", "gemini", "claude", "codex", "antigravity"
}
```

**Matching Algorithm:**
```
For each rule in PayloadConfig[rules]:
  IF rule.Models match the current model AND protocol:
    Apply the rule to the payload
```

**Model Matching:**
- Uses simple glob-style wildcard matching (`*` = zero or more characters)
- Examples:
  - `"gpt-*"` matches `"gpt-5"`, `"gpt-4-turbo"`, `"gpt-4.5"`
  - `"*-pro"` matches `"gemini-2.5-pro"`, `"claude-3-5-sonnet-20250512"`
  - `"gemini-*-flash"` matches `"gemini-2.5-flash"`, `"gemini-3-flash"`
  - `"*"` matches any model

**Protocol Matching:**
- If rule specifies a protocol (e.g., `"gemini"`), it only applies when translating TO that protocol
- If rule's protocol is empty, it applies to all protocols

**Candidates Checked:**
The system checks THREE model candidates in order:
1. The base model name (e.g., `"claude-opus-4-5"`)
2. The base from the requested model (strips thinking budget suffix)
3. The full requested model if it has a thinking suffix (e.g., `"claude-opus-4-5:thinking-5m"`)

This allows rules to target specific thinking budget variants.

### 2.3 JSON Path Syntax

All parameter paths use **gjson/sjson syntax** (Go's popular JSON path library):
- Nested paths: `"generationConfig.thinkingConfig.thinkingBudget"`
- Array indices: `"tools.0.name"`
- Wildcard arrays: `"tools.*.description"`

**Examples of filter paths:**
```yaml
filter:
  - models:
      - name: "gemini-2.5-pro"
        protocol: "gemini"
    params:
      - "generationConfig.thinkingConfig.thinkingBudget"
      - "generationConfig.responseJsonSchema"
      - "tools.0.input_schema"
```

### 2.4 Root Path Support (for Gemini CLI)

Some protocols nest payloads under a root path. For example, **Gemini CLI** wraps payloads in:
```json
{
  "request": {
    "contents": [...],
    "generationConfig": {...}
  }
}
```

The `ApplyPayloadConfigWithRoot()` function handles this:
- `root = "request"` → parameter `"generationConfig.thinkingBudget"` becomes `"request.generationConfig.thinkingBudget"`
- `root = ""` → parameter paths used as-is

---

## 3. Configuration Structure

### 3.1 Configuration File Format

**Location:** `config.yaml` (lines 366-398)

```yaml
payload:
  # Rules that only set parameters if missing (first-write-wins)
  default:
    - models:
        - name: "gemini-2.5-pro"
          protocol: "gemini"
      params:
        "generationConfig.thinkingConfig.thinkingBudget": 32768

  # Rules that set raw JSON values if missing
  default-raw:
    - models:
        - name: "gemini-2.5-pro"
          protocol: "gemini"
      params:
        "generationConfig.responseJsonSchema": '{"type":"object","properties":{"answer":{"type":"string"}}}'

  # Rules that always overwrite parameters (last-write-wins)
  override:
    - models:
        - name: "gpt-*"
          protocol: "codex"
      params:
        "reasoning.effort": "high"

  # Rules that always set raw JSON (last-write-wins)
  override-raw:
    - models:
        - name: "gpt-*"
          protocol: "codex"
      params:
        "response_format": '{"type":"json_schema",...}'

  # Rules that REMOVE parameters (filtering)
  filter:
    - models:
        - name: "gemini-2.5-pro"
          protocol: "gemini"
      params:
        - "generationConfig.thinkingConfig.thinkingBudget"
        - "generationConfig.responseJsonSchema"
```

### 3.2 Data Structure Definitions

**File:** `/internal/config/config.go` (lines 288-325)

```go
// Container for all payload rules
type PayloadConfig struct {
    Default     []PayloadRule         // Conditionally set if missing
    DefaultRaw  []PayloadRule         // Conditionally set (raw JSON) if missing
    Override    []PayloadRule         // Always overwrite
    OverrideRaw []PayloadRule         // Always overwrite (raw JSON)
    Filter      []PayloadFilterRule   // REMOVE parameters
}

// Rule targeting specific models
type PayloadRule struct {
    Models []PayloadModelRule       // Model name patterns + protocol
    Params map[string]any           // JSON path → value
}

// Rule to remove parameters
type PayloadFilterRule struct {
    Models []PayloadModelRule       // Model name patterns + protocol
    Params []string                 // JSON paths to delete
}

// Model name pattern matcher
type PayloadModelRule struct {
    Name     string  // e.g., "gpt-*", "gemini-2.5-pro", "*-pro"
    Protocol string  // e.g., "gemini", "claude", "openai", "codex", "antigravity"
}
```

---

## 4. Parameters that Need Filtering by Provider

### 4.1 Common Unsupported Parameters (Research Gap)

Based on the codebase exploration, these are parameters that **might need filtering** for various providers:

#### Parameters Claude Sends That Others May Not Support:
- `reasoning` / `reasoning.effort` - Claude thinking feature
- `budget_tokens` - Claude thinking budget (newer format)
- `thinking_enabled` - Anthropic-specific

#### Parameters Gemini Sends That Others May Not Support:
- `generationConfig.thinkingConfig.thinkingBudget` - Gemini's thinking feature
- `generationConfig.responseJsonSchema` - Gemini-specific JSON schema handling

#### Parameters OpenAI/Codex Sends That Others May Not Support:
- `response_format.json_schema` - OpenAI-specific structured output
- `reasoning.effort` - Codex-specific

#### Parameters That Need Protocol-Specific Adaptation:
- Tool definitions format (Claude vs OpenAI vs Gemini)
- System prompt structure
- Temperature ranges and semantics
- Stop sequences format

### 4.2 Current Filtering Rules in config.example.yaml

Currently, the example shows:
```yaml
filter:
  - models:
      - name: "gemini-2.5-pro"
        protocol: "gemini"
    params:
      - "generationConfig.thinkingConfig.thinkingBudget"
      - "generationConfig.responseJsonSchema"
```

This suggests that when Gemini models receive requests with these parameters, they should be filtered out (likely because the upstream doesn't support them).

---

## 5. Integration Points: Where Filtering Happens

### 5.1 Usage in Executors

All provider executors call `ApplyPayloadConfigWithRoot()` **after request translation** and **before sending upstream**:

**Claude Executor** (`/internal/runtime/executor/claude_executor.go:168`)
```go
requestedModel := helps.PayloadRequestedModel(opts, req.Model)
body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
```

**Gemini Executor** (`/internal/runtime/executor/gemini_executor.go`)
```go
// Similar pattern: apply after translation, before upstream call
body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, "gemini", "request", body, originalTranslated, requestedModel)
// Note: "request" is the root path for Gemini CLI wrapping
```

**Codex Executor** (`/internal/runtime/executor/codex_executor.go`)
```go
// Similar pattern for OpenAI-compatible codex
```

**Antigravity Executor** (`/internal/runtime/executor/antigravity_executor.go`)
```go
// Similar pattern for Antigravity (Gemini-based)
```

### 5.2 Calling Signature

```go
func ApplyPayloadConfigWithRoot(
    cfg *config.Config,           // Main application config
    model string,                 // Base model name (after alias resolution)
    protocol string,              // Target protocol ("claude", "gemini", "openai", etc.)
    root string,                  // Root JSON path ("request" for Gemini CLI, "" for others)
    payload []byte,               // Payload to filter (after translation)
    original []byte,              // Original payload before translation (for default checks)
    requestedModel string,        // Client-requested model name (pre-alias)
) []byte
```

---

## 6. Expected vs Actual Behavior

### 6.1 Expected Behavior

1. **Configuration is loaded** from `config.yaml`'s `payload` section
2. **Request arrives** at handler in client-specified format (OpenAI/Claude/Gemini)
3. **Model name resolved** and protocol determined
4. **Request translated** to target provider format
5. **Payload filtering applied** based on:
   - Current model name and its aliases
   - Target protocol
   - Configured rules
6. **Filtered payload sent** to upstream provider
7. **Provider accepts request** because unsupported parameters removed
8. **Response translated** back to client format

### 6.2 Potential Failure Points

| Issue | Symptom | Root Cause |
|-------|---------|-----------|
| Filter rules don't match | Parameters sent to provider; provider rejects | Model pattern doesn't match or protocol mismatch |
| Wrong parameter removed | Client loses important parameter | JSON path typo or overly broad filter |
| Filter applied at wrong stage | Parameters still reach upstream | Filtering called before translation (wrong stage) |
| Protocol string mismatched | Rules never trigger | Protocol string case-sensitive or wrong value |
| Model alias not expanded | Filters targeting alias don't work | `requestedModel` not passed correctly |

---

## 7. Design Decisions & Assumptions

### 7.1 Why Filter is Applied AFTER Translation

**Decision:** Filtering happens after request translation to target format, not before.

**Rationale:**
- Parameter names differ between protocols (e.g., `"budget_tokens"` in Claude vs `"thinkingBudget"` in Gemini)
- JSON structure differs (e.g., nested in `"generationConfig"` for Gemini, flat for Claude)
- Filtering after translation ensures paths are correct for the target format

### 7.2 Why Model Matching Uses Wildcards

**Decision:** Use glob-style wildcards (`*`) for pattern matching instead of regex.

**Rationale:**
- Simpler, less error-prone for administrators
- Faster matching (linear character scan vs regex engine)
- Easier to understand configuration (no regex escaping needed)

### 7.3 Why "First-Write-Wins" for Defaults

**Decision:** Defaults use "first-write-wins"; Overrides use "last-write-wins".

**Rationale:**
- **Defaults:** Respects most-specific rule first (e.g., `"gpt-4.5"` rule before `"gpt-*"`)
- **Overrides:** Allows later rules to have precedence (configuration order matters for policy application)

### 7.4 Why Separate Filter Rules

**Decision:** Separate `filter` rules from `override` (which could set to null/empty).

**Rationale:**
- **Explicit intent:** Filtering is a specific use case (provider incompatibility)
- **Implementation:** `filter` uses `sjson.DeleteBytes()` (proper deletion), not setting to null
- **Clarity:** Configuration is self-documenting about what the system does

### 7.5 Protocol-Scoped Rules

**Decision:** Rules can specify a protocol to only apply when translating TO that protocol.

**Rationale:**
- Same model may be accessed via different formats
- Only certain parameter combinations are incompatible with specific targets
- Allows fine-grained control per translation path

---

## 8. Architectural Gaps & Potential Issues

### 8.1 Missing Parameter Documentation

**Gap:** There's no comprehensive list of which parameters Claude sends and which providers don't support them.

**Impact:**
- Administrators must manually discover incompatibilities (test and fail)
- Filter rules may be incomplete or miss edge cases

**Mitigation:**
- Create a provider compatibility matrix (params × providers)
- Add comments in example config showing common filtering patterns

### 8.2 No Validation of Filter Rules

**Gap:** The system doesn't validate that filter rules are:
- Syntactically correct JSON paths
- Targeting parameters that actually exist in requests
- Not filtering out critical required fields

**Impact:**
- Silent failures (filter rule silently doesn't match anything)
- Accidentally over-filtering (removing too many parameters)

**Mitigation:**
- Add validation on config load
- Log which rules match which requests (debug mode)

### 8.3 Limited Debugging/Observability

**Gap:** There's no built-in way to see:
- Which filter rules matched for a given request
- What the payload looked like before and after filtering
- Why a particular filter didn't apply

**Impact:**
- Hard to debug misconfigurations
- Troubleshooting requires reading code and adding logs

**Mitigation:**
- Add debug logging in `ApplyPayloadConfigWithRoot()`
- Include matched rules in request context

### 8.4 Model Alias Resolution Timing

**Gap:** The system passes `requestedModel` (client-requested, pre-alias) separately from `model` (resolved).

**Impact:**
- Filter rules must match against BOTH for comprehensive coverage
- Some rules might target aliases, others the resolved name

**Note:** The code handles this by checking both:
```go
candidates := payloadModelCandidates(model, requestedModel)
```

### 8.5 No "Force Filter" or Whitelist Mode

**Gap:** System only has "remove these parameters" (blacklist). No "only allow these parameters" (whitelist).

**Impact:**
- If a new unsupported parameter is added to Claude, filter rules must be updated
- No conservative "allow minimal set" mode for compatibility

**Mitigation:**
- Could add `whitelist` rule type if needed
- Currently, filter rules cover the common cases

---

## 9. Recommended Implementation for Filtering

### 9.1 Example: Filter ReasoningEffort for OpenAI-Compatible Providers

```yaml
payload:
  filter:
    # Claude's reasoning.effort is not supported by OpenAI-compatible providers
    - models:
        - name: "gpt-*"
          protocol: "openai"
        - name: "*"
          protocol: "codex"
      params:
        - "reasoning.effort"
        - "thinking_enabled"
        - "budget_tokens"

    # Gemini-specific parameters not supported elsewhere
    - models:
        - name: "gpt-*"
          protocol: "openai"
      params:
        - "generationConfig.thinkingConfig"
        - "generationConfig.responseJsonSchema"
```

### 9.2 Determining Which Parameters to Filter

**Approach:**
1. **Start with Claude's request format** (what Claude Code sends)
2. **Identify provider-specific parameters:**
   - `reasoning.*` → Claude-only
   - `generationConfig.thinkingConfig.*` → Gemini-only
   - `response_format` → OpenAI-specific structure
3. **Test with provider's API docs** to confirm unsupported fields
4. **Add filter rules** for translation paths where incompatibility exists
5. **Monitor upstream errors** for new incompatibilities

---

## 10. Summary: How Parameters Flow

```
Client Request (any format)
  ↓
[Handler] Extract: model, client_format
  ↓
[Translate] client_format → provider_format
  ↓
[Filter]  ← APPLIES HERE
  Match: model name pattern + protocol
  Remove: JSON paths that provider doesn't support
  ↓
[Execute] Send filtered payload to provider
  ↓
Provider Response
  ↓
[Translate] provider_format → client_format
  ↓
Client Response
```

The **filter step** is the gatekeeper ensuring downstream providers never see unsupported parameters.

---

## 11. Files to Review

- **Core filtering logic:** `/internal/runtime/executor/helps/payload_helpers.go`
- **Config structures:** `/internal/config/config.go` (lines 288-325)
- **Example configuration:** `/config.example.yaml` (lines 366-398)
- **Claude executor integration:** `/internal/runtime/executor/claude_executor.go` (line 168)
- **Gemini executor integration:** `/internal/runtime/executor/gemini_executor.go`
- **Codex executor integration:** `/internal/runtime/executor/codex_executor.go`
- **Antigravity executor integration:** `/internal/runtime/executor/antigravity_executor.go`
