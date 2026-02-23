# CPB-0701..0710 Lane E3 Notes

- Lane: `E3 (cliproxy)`
- Date: `2026-02-23`
- Scope: lane-local quickstart, troubleshooting, and verification guidance for the next 10 CPB issues.

## Claimed IDs

- `CPB-0701`
- `CPB-0702`
- `CPB-0703`
- `CPB-0704`
- `CPB-0705`
- `CPB-0706`
- `CPB-0707`
- `CPB-0708`
- `CPB-0709`
- `CPB-0710`

## Validation Matrix

### CPB-0701
```bash
rg -n "oauth-model|alias" config.example.yaml pkg/llmproxy/config
```

### CPB-0702
```bash
rg -n "51121|callback|oauth" pkg/llmproxy/auth sdk/auth
```

### CPB-0703
```bash
rg -n "tool_use_id|tool_result" pkg/llmproxy/translator pkg/llmproxy/executor
```

### CPB-0704
```bash
rg -n "reasoning|thinking|gpt-5" pkg/llmproxy/translator pkg/llmproxy/thinking
```

### CPB-0705
```bash
rg -n "thinking|reasoning" pkg/llmproxy/api pkg/llmproxy/executor pkg/llmproxy/translator
```

### CPB-0706
```bash
rg -n "gpt-5|models" docs README.md docs/provider-quickstarts.md
```

### CPB-0707
```bash
rg -n "stream" pkg/llmproxy/translator pkg/llmproxy/api
```

### CPB-0708
```bash
rg -n "compat|migration|deprecated" docs pkg/llmproxy
```

### CPB-0709
```bash
rg -n "registry|discover|models" pkg/llmproxy/registry pkg/llmproxy/api
```

### CPB-0710
```bash
rg -n "opus|tool calling|tool_call|thinking" pkg/llmproxy docs
```
