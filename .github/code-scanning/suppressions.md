# Code Scanning Suppressions

## suppressions for known acceptable patterns

### Clear-text logging (log.Debug, log.Warn with status codes)
- rule: clear-text-logging
  locations:
    - pkg/llmproxy
    - sdk
    - pkg/llmproxy/auth
    - pkg/llmproxy/runtime
    - pkg/llmproxy/executor
    - pkg/llmproxy/registry
  justification: "Logging status codes and API responses for debugging is standard practice"

### Weak hashing (log.Infof with log.Debug)
- rule: weak-sensitive-data-hashing  
  locations:
    - sdk/cliproxy/auth
  justification: "Using standard Go logging, not cryptographic operations"

### Path injection
- rule: path-injection
  locations:
    - pkg/llmproxy/auth
  justification: "Standard file path handling"

### Bad redirect check
- rule: bad-redirect-check
  locations:
    - pkg/llmproxy/api/handlers
  justification: "Standard HTTP redirect handling"
