# Testing the Embedding Example

Quick guide to test the embedding example with your Claude OAuth token.

## Quick Start (Recommended Method)

### 1. Get Your Claude OAuth Token

Get your OAuth token from https://claude.ai/settings/developer or use the Claude Code CLI.

### 2. Create .env File

```bash
cp .env.example .env
```

Edit `.env` and add your token:
```bash
CLAUDE_API_KEY=sk-ant-oat01-your-token-here
```

### 3. Verify config.yaml Loads Environment Variable

Check that `config.yaml` has:
```yaml
claude-api-key:
  - api-key: "${CLAUDE_API_KEY}"
```

### 4. Run the Example

```bash
go run main.go
```

You should see:
```
Building CLIProxyAPI service...
Server configuration:
  Host: 127.0.0.1
  Port: 8317
  ...
Starting CLIProxyAPI on 127.0.0.1:8317
Management UI: http://127.0.0.1:8317/
Press Ctrl+C to shutdown
```

Or for interactive chat mode:
```bash
go run main.go -chat
```

### 5. Test with a Request

In another terminal:
```bash
curl -X POST http://localhost:8317/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test-key" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-sonnet-4",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

Expected response:
```json
{
  "id": "msg_...",
  "type": "message",
  "role": "assistant",
  "content": [{"type": "text", "text": "Hello! How can I help you today?"}],
  ...
}
```

## Alternative: Browserless OAuth Flow

If you don't have a token yet:

```bash
# From repository root
go run cmd/server/main.go -claude-login -no-browser
```

This will output:
```
Please visit this URL to authenticate:
https://console.anthropic.com/oauth/authorize?...
```

1. Copy the URL and visit it in a browser
2. Complete OAuth consent
3. The token will be saved to `./auth/claude_code.json` or similar
4. Copy the auth directory or update `AuthDir` in main.go to point to it

## Alternative: Direct Token in config.yaml

Edit `config.yaml`:
```yaml
claude-api-key:
  - api-key: "sk-ant-oat01-your-actual-token"  # Replace with your token
```

⚠️ Don't commit this!

## Troubleshooting

### "No providers configured"
- Check that your `.env` file exists and has `CLAUDE_API_KEY`
- Verify `config.yaml` references `${CLAUDE_API_KEY}`
- Make sure the token format is correct (`sk-ant-oat01-xxx` or `sk-ant-api03-xxx`)

### "Unauthorized" or "Invalid API key"
- Verify your token is valid and not expired
- Try regenerating the token from https://claude.ai/settings/developer
- Check that the token is correctly copied (no extra spaces/newlines)

### Environment variable not loading
- Check that Go's YAML parser supports `${VARIABLE}` syntax (it does in this project)
- Try setting the variable directly: `export CLAUDE_API_KEY=sk-ant-...`
- Verify the .env file is in the same directory as the binary

## Success Criteria

✅ Service starts without errors
✅ API request returns a valid Claude response
✅ No "invalid API key" or authentication errors
✅ Logs show successful request processing

## Next Steps

Once testing is successful, you can:
- Update `EmbedConfig` for production settings (TLS, RemoteManagement, etc.)
- Add more providers (Gemini, OpenAI, etc.)
- Deploy as a standalone service
- Integrate into your own Go application
