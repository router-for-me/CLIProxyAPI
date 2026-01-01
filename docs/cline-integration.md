# Cline Integration Guide

Integrate Cline authentication with CLIProxyAPI to use Cline models through OpenAI-compatible API.

## Prerequisites

- Cline VSCode extension with active subscription
- CLIProxyAPI installed

## Quick Start

### 1. Export Refresh Token from VSCode

You need to modify Cline extension to add token export functionality:

1. Clone Cline repository:
```bash
git clone https://github.com/cline/cline.git
cd cline
```

2. Add export command to `src/extension.ts`:

```typescript
context.subscriptions.push(
  vscode.commands.registerCommand(commands.ExportAuthToken, async () => {
    const authData = await context.secrets.get("cline:clineAccountId")
    if (!authData) return
    const refreshToken = JSON.parse(authData).refreshToken
    await vscode.env.clipboard.writeText(refreshToken)
    vscode.window.showInformationMessage("Refresh Token copied!")
  })
)
```

3. Add to `package.json` commands array:

```json
{
  "command": "cline.exportAuthToken",
  "title": "Export Auth Token",
  "category": "Cline"
}
```

4. Build and install extension:
```bash
npm install
npm run compile
npx @vscode/vsce package --allow-package-secrets sendgrid
```

5. Install the generated `.vsix` file in VSCode

6. Open Command Palette (`Ctrl+Shift+P` or `Cmd+Shift+P`) and run: **"Cline: Export Auth Token"**

7. Token will be copied to clipboard

### 2. Login to CLIProxyAPI

```bash
# Interactive login
cliproxyapi login cline
# Paste token when prompted

# Or direct provision
cliproxyapi login cline --metadata refresh_token=YOUR_REFRESH_TOKEN
```

### 3. Verify

```bash
cliproxyapi list
# Should show: cline-your-email@example.com.json
```

## Token Management

**Lifecycle:**
- Refresh Token: Long-lived (from VSCode)
- Access Token: ~10 minutes, auto-refreshed 2 minutes before expiration

**Storage:** `./auth/cline-your-email@example.com.json`

**Manual Refresh:**
```bash
cliproxyapi refresh cline-your-email@example.com.json
```

## Usage

### Python Example

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:11434/v1",
    api_key="your-cliproxyapi-key"
)

response = client.chat.completions.create(
    model="x-ai/grok-code-fast-1",  # or minimax/minimax-m2
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### cURL Example

```bash
curl http://localhost:11434/v1/chat/completions \
  -H "Authorization: Bearer your-key" \
  -d '{"model":"x-ai/grok-code-fast-1","messages":[{"role":"user","content":"Hello!"}]}'
```

### List Models

```bash
curl http://localhost:11434/v1/models -H "Authorization: Bearer your-key"
```

## Configuration

```yaml
auth-dir: "./auth"
debug: true
proxy-url: ""
request-retry: 3
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| "refresh token is required" | Export token from VSCode using `Cline: Export Auth Token` |
| "token refresh failed" | Re-export token from VSCode and login again |
| "Failed to read refresh response" | Check internet connection and proxy settings |
| Token expired during request | Auto-refreshed automatically. If fails, manually refresh or re-login |

## Security

- Never commit `auth/` directory to version control
- Add `auth/` to `.gitignore`
- Use file permissions 0600 for token files
- Keep CLIProxyAPI on localhost unless properly secured

## Programmatic Usage

```go
import "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cline"

authSvc := cline.NewClineAuth(cfg)
tokenData, err := authSvc.RefreshTokens(ctx, refreshToken)
storage := authSvc.CreateTokenStorage(tokenData)
```

## Support

- [GitHub Issues](https://github.com/router-for-me/CLIProxyAPI/issues)
- [Main Documentation](../README.md)
