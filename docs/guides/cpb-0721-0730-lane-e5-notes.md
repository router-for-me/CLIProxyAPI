# CPB-0721..0730 Lane E5 Notes

## CPB-0721 - Antigravity API 400 Compatibility (`$ref` / `$defs`)

### Regression checks

```bash
# Executor build request sanitization for tool schemas

go test ./pkg/llmproxy/executor -run TestAntigravityBuildRequest_RemovesRefAndDefsFromToolSchema -count=1

go test ./pkg/llmproxy/runtime/executor -run TestAntigravityBuildRequest_RemovesRefAndDefsFromToolSchema -count=1
```

### Shared utility guardrails

```bash
# Verifies recursive key-drop in JSON schema payloads
go test ./pkg/llmproxy/util -run TestDeleteKeysByName -count=1
```

### Quickstart probe (manual)

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"claude-opus-4-6",
    "messages":[{"role":"user","content":"ping"}],
    "tools":[
      {
        "type":"function",
        "function":{
          "name":"test_tool",
          "description":"test tool schema",
          "parameters":{
            "type":"object",
            "properties":{
              "payload": {
                "$defs": {"Address":{"type":"object"}},
                "$ref": "#/schemas/Address",
                "city": {"type":"string"}
              }
            }
          }
        }
      }
    ]
  }' | jq '.'
```

Expected:
- Request completes and returns an object under `choices` or a valid provider error.
- No request-rejection specifically indicating `Invalid JSON`, `$ref`, or `$defs` payload incompatibility in upstream logs.
