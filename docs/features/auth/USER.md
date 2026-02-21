# User Guide: Enterprise Authentication

## Understanding Authentication in cliproxyapi++

cliproxyapi++ supports multiple authentication methods for different LLM providers. The authentication system handles credential management, automatic token refresh, and quota tracking seamlessly in the background.

## Quick Start: Adding Credentials

### Method 1: Manual Configuration

Create credential files in the `auths/` directory:

**Claude API Key** (`auths/claude.json`):
```json
{
  "type": "api_key",
  "token": "sk-ant-xxxxx",
  "priority": 1
}
```

**OpenAI API Key** (`auths/openai.json`):
```json
{
  "type": "api_key",
  "token": "sk-xxxxx",
  "priority": 2
}
```

**Gemini API Key** (`auths/gemini.json`):
```json
{
  "type": "api_key",
  "token": "AIzaSyxxxxx",
  "priority": 3
}
```

### Method 2: Interactive Setup (Web UI)

For providers with OAuth/device flow, use the web interface:

**GitHub Copilot**:
1. Visit `http://localhost:8317/v0/oauth/copilot`
2. Enter your GitHub credentials
3. Authorize the application
4. Token is automatically stored

**Kiro (AWS CodeWhisperer)**:
1. Visit `http://localhost:8317/v0/oauth/kiro`
2. Choose AWS Builder ID or Identity Center
3. Complete browser-based login
4. Token is automatically stored

### Method 3: CLI Commands

```bash
# Add API key
curl -X POST http://localhost:8317/v0/management/auths \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "claude",
    "type": "api_key",
    "token": "sk-ant-xxxxx"
  }'

# Add with priority
curl -X POST http://localhost:8317/v0/management/auths \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "claude",
    "type": "api_key",
    "token": "sk-ant-xxxxx",
    "priority": 10
  }'
```

## Authentication Methods

### API Key Authentication

**Best for**: Providers with static API keys that don't expire.

**Supported Providers**:
- Claude (Anthropic)
- OpenAI
- Gemini (Google)
- Mistral
- Groq
- DeepSeek
- And many more

**Setup**:
```json
{
  "type": "api_key",
  "token": "your-api-key-here",
  "priority": 1
}
```

**Priority**: Lower number = higher priority. Used when multiple credentials exist for the same provider.

### OAuth 2.0 Device Flow

**Best for**: Providers requiring user consent with token refresh capability.

**Supported Providers**:
- GitHub Copilot
- Kiro (AWS CodeWhisperer)

**Setup**: Use web UI - automatic handling of device code, user authorization, and token storage.

**How it Works**:
1. System requests a device code from provider
2. You're shown a user code and verification URL
3. Visit URL, enter code, authorize
4. System polls for token in background
5. Token stored and automatically refreshed

**Example: GitHub Copilot**:
```bash
# Visit web UI
open http://localhost:8317/v0/oauth/copilot

# Enter your GitHub credentials
# Authorize the application
# Done! Token is stored and managed automatically
```

### Custom Provider Authentication

**Best for**: Proprietary providers with custom auth flows.

**Setup**: Implement custom auth flow in embedded library (see DEV.md).

## Quota Management

### Understanding Quotas

Track usage per credential:

```json
{
  "type": "api_key",
  "token": "sk-ant-xxxxx",
  "quota": {
    "limit": 1000000,
    "used": 50000,
    "remaining": 950000
  }
}
```

**Automatic Quota Tracking**:
- Request tokens are deducted from quota after each request
- Multiple credentials are load-balanced based on remaining quota
- Automatic rotation when quota is exhausted

### Setting Quotas

```bash
# Update quota via API
curl -X PUT http://localhost:8317/v0/management/auths/claude/quota \
  -H "Content-Type: application/json" \
  -d '{
    "limit": 1000000
  }'
```

### Quota Reset

Quotas reset automatically based on provider billing cycles (configurable in `config.yaml`):

```yaml
auth:
  quota:
    reset_schedule:
      claude: "monthly"
      openai: "monthly"
      gemini: "daily"
```

## Automatic Token Refresh

### How It Works

The refresh worker runs every 5 minutes and:
1. Checks all credentials for expiration
2. Refreshes tokens expiring within 10 minutes
3. Updates stored credentials
4. Notifies applications of refresh (no downtime)

### Configuration

```yaml
auth:
  refresh:
    enabled: true
    check_interval: "5m"
    refresh_lead_time: "10m"
```

### Monitoring Refresh

```bash
# Check refresh status
curl http://localhost:8317/v0/management/auths/refresh/status
```

Response:
```json
{
  "last_check": "2026-02-19T23:00:00Z",
  "next_check": "2026-02-19T23:05:00Z",
  "credentials_checked": 5,
  "refreshed": 1,
  "failed": 0
}
```

## Multi-Credential Management

### Adding Multiple Credentials

```bash
# First Claude key
curl -X POST http://localhost:8317/v0/management/auths \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "claude",
    "type": "api_key",
    "token": "sk-ant-key1",
    "priority": 1
  }'

# Second Claude key
curl -X POST http://localhost:8317/v0/management/auths \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "claude",
    "type": "api_key",
    "token": "sk-ant-key2",
    "priority": 2
  }'
```

### Load Balancing Strategies

**Round-Robin**: Rotate through credentials evenly
```yaml
auth:
  selection_strategy: "round_robin"
```

**Quota-Aware**: Use credential with most remaining quota
```yaml
auth:
  selection_strategy: "quota_aware"
```

**Priority-Based**: Use highest priority first
```yaml
auth:
  selection_strategy: "priority"
```

### Monitoring Credentials

```bash
# List all credentials
curl http://localhost:8317/v0/management/auths
```

Response:
```json
{
  "auths": [
    {
      "provider": "claude",
      "type": "api_key",
      "priority": 1,
      "quota": {
        "limit": 1000000,
        "used": 50000,
        "remaining": 950000
      },
      "status": "active"
    },
    {
      "provider": "claude",
      "type": "api_key",
      "priority": 2,
      "quota": {
        "limit": 1000000,
        "used": 30000,
        "remaining": 970000
      },
      "status": "active"
    }
  ]
}
```

## Credential Rotation

### Automatic Rotation

When quota is exhausted or token expires:
1. System selects next available credential
2. Notifications sent (configured)
3. Load continues seamlessly

### Manual Rotation

```bash
# Remove exhausted credential
curl -X DELETE http://localhost:8317/v0/management/auths/claude?id=sk-ant-key1

# Add new credential
curl -X POST http://localhost:8317/v0/management/auths \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "claude",
    "type": "api_key",
    "token": "sk-ant-key3",
    "priority": 1
  }'
```

## Troubleshooting

### Token Not Refreshing

**Problem**: Token expired but not refreshed

**Solutions**:
1. Check refresh worker is enabled in config
2. Verify refresh token exists (OAuth only)
3. Check logs: `tail -f logs/auth.log`
4. Manual refresh: `POST /v0/management/auths/{provider}/refresh`

### Authentication Failed

**Problem**: 401 errors from provider

**Solutions**:
1. Verify token is correct
2. Check token hasn't expired
3. Verify provider is enabled in config
4. Test token with provider's API directly

### Quota Exhausted

**Problem**: Requests failing due to quota

**Solutions**:
1. Add additional credentials for provider
2. Check quota reset schedule
3. Monitor usage: `GET /v0/management/auths`
4. Adjust selection strategy

### OAuth Flow Stuck

**Problem**: Device flow not completing

**Solutions**:
1. Ensure you visited the verification URL
2. Check you entered the correct user code
3. Verify provider authorization wasn't denied
4. Check browser console for errors
5. Retry: refresh the auth page

### Credential Not Found

**Problem**: "No credentials for provider X" error

**Solutions**:
1. Add credential for provider
2. Check credential file exists in `auths/`
3. Verify file is valid JSON
4. Check provider is enabled in config

## Best Practices

### Security

1. **Never commit credentials** to version control
2. **Use file permissions**: `chmod 600 auths/*.json`
3. **Enable encryption** for sensitive environments
4. **Rotate credentials** regularly
5. **Use different credentials** for dev/prod

### Performance

1. **Use multiple credentials** for high-volume providers
2. **Enable quota-aware selection** for load balancing
3. **Monitor refresh logs** for issues
4. **Set appropriate priorities** for credential routing

### Monitoring

1. **Check auth metrics** regularly
2. **Set up alerts** for quota exhaustion
3. **Monitor refresh failures**
4. **Review credential usage** patterns

## Advanced: Encryption

Enable credential encryption:

```yaml
auth:
  encryption:
    enabled: true
    key: "YOUR_32_BYTE_ENCRYPTION_KEY_HERE"
```

Generate encryption key:
```bash
openssl rand -base64 32
```

## API Reference

### Auth Management

**List All Auths**
```http
GET /v0/management/auths
```

**Get Auth for Provider**
```http
GET /v0/management/auths/{provider}
```

**Add Auth**
```http
POST /v0/management/auths
Content-Type: application/json

{
  "provider": "claude",
  "type": "api_key",
  "token": "sk-ant-xxxxx",
  "priority": 1
}
```

**Update Auth**
```http
PUT /v0/management/auths/{provider}
Content-Type: application/json

{
  "token": "sk-ant-new-token",
  "priority": 2
}
```

**Delete Auth**
```http
DELETE /v0/management/auths/{provider}?id=credential-id
```

**Refresh Auth**
```http
POST /v0/management/auths/{provider}/refresh
```

**Get Quota**
```http
GET /v0/management/auths/{provider}/quota
```

**Update Quota**
```http
PUT /v0/management/auths/{provider}/quota
Content-Type: application/json

{
  "limit": 1000000
}
```

## Next Steps

- See [DEV.md](./DEV.md) for implementing custom auth flows
- See [../security/](../security/) for security features
- See [../operations/](../operations/) for operational guidance
