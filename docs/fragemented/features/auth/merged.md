# Merged Fragmented Markdown

## Source: features/auth/DEV.md

# Developer Guide: Authentication

This page captures extension guidance for auth-related changes.

## Core tasks

- Add or update auth provider implementations.
- Verify token refresh behavior and error handling.
- Validate quota tracking and credential rotation behavior.

## Related docs

- [User Guide](./USER.md)
- [Technical Spec](./SPEC.md)
- [Operations Feature](../operations/index.md)
- [Security Feature](../security/index.md)


---

## Source: features/auth/SPEC.md

# Technical Specification: Enterprise Authentication & Lifecycle

## Overview

**cliproxyapi++** implements enterprise-grade authentication management with full lifecycle automation, supporting multiple authentication flows (API keys, OAuth, device authorization) and automatic token refresh capabilities.

## Authentication Architecture

### Core Components

```
Auth System
├── Auth Manager (coreauth.Manager)
│   ├── Token Store (File-based)
│   ├── Refresh Worker (Background)
│   ├── Health Checker
│   └── Quota Tracker
├── Auth Flows
│   ├── API Key Flow
│   ├── OAuth 2.0 Flow
│   ├── Device Authorization Flow
│   └── Custom Provider Flows
└── Credential Management
    ├── Multi-credential support
    ├── Per-credential quota tracking
    └── Automatic rotation
```

## Authentication Flows

### 1. API Key Authentication

**Purpose**: Simple token-based authentication for providers with static API keys.

**Implementation**:
```go
type APIKeyAuth struct {
    Token string `json:"token"`
}

func (a *APIKeyAuth) GetHeaders() map[string]string {
    return map[string]string{
        "Authorization": fmt.Sprintf("Bearer %s", a.Token),
    }
}
```

**Supported Providers**: Claude, Gemini, OpenAI, Mistral, Groq, DeepSeek

**Storage Format** (`auths/{provider}.json`):
```json
{
  "type": "api_key",
  "token": "sk-ant-xxx",
  "priority": 1,
  "quota": {
    "limit": 1000000,
    "used": 50000
  }
}
```

### 2. OAuth 2.0 Flow

**Purpose**: Standard OAuth 2.0 authorization code flow for providers requiring user consent.

**Flow Sequence**:
```
1. User initiates auth
2. Redirect to provider auth URL
3. User grants consent
4. Provider redirects with authorization code
5. Exchange code for access token
6. Store access + refresh token
```

**Implementation**:
```go
type OAuthFlow struct {
    clientID     string
    clientSecret string
    redirectURL  string
    authURL      string
    tokenURL     string
}

func (f *OAuthFlow) Start(ctx context.Context) (*AuthResult, error) {
    state := generateSecureState()
    authURL := fmt.Sprintf("%s?response_type=code&client_id=%s&redirect_uri=%s&state=%s",
        f.authURL, f.clientID, f.redirectURL, state)

    return &AuthResult{
        Method:  "oauth",
        AuthURL: authURL,
        State:   state,
    }, nil
}

func (f *OAuthFlow) Exchange(ctx context.Context, code string) (*AuthToken, error) {
    // Exchange authorization code for tokens
    resp, err := http.PostForm(f.tokenURL, map[string]string{
        "client_id":     f.clientID,
        "client_secret": f.clientSecret,
        "code":          code,
        "redirect_uri":  f.redirectURL,
        "grant_type":    "authorization_code",
    })

    // Parse and return tokens
}
```

**Supported Providers**: GitHub Copilot (partial)

### 3. Device Authorization Flow

**Purpose**: OAuth 2.0 device authorization grant for headless/batch environments.

**Flow Sequence**:
```
1. Request device code
2. Display user code and verification URL
3. User visits URL, enters code
4. Background polling for token
5. Receive access token
```

**Implementation**:
```go
type DeviceFlow struct {
    deviceCodeURL string
    tokenURL      string
    clientID      string
}

func (f *DeviceFlow) Start(ctx context.Context) (*AuthResult, error) {
    resp, err := http.PostForm(f.deviceCodeURL, map[string]string{
        "client_id": f.clientID,
    })

    var dc struct {
        DeviceCode              string `json:"device_code"`
        UserCode               string `json:"user_code"`
        VerificationURI        string `json:"verification_uri"`
        VerificationURIComplete string `json:"verification_uri_complete"`
        ExpiresIn              int    `json:"expires_in"`
        Interval               int    `json:"interval"`
    }

    // Parse and return device code info
    return &AuthResult{
        Method:              "device_flow",
        UserCode:            dc.UserCode,
        VerificationURL:     dc.VerificationURI,
        DeviceCode:          dc.DeviceCode,
        Interval:            dc.Interval,
        ExpiresAt:           time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second),
    }, nil
}

func (f *DeviceFlow) Poll(ctx context.Context, deviceCode string) (*AuthToken, error) {
    ticker := time.NewTicker(time.Duration(f.Interval) * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-ticker.C:
            resp, err := http.PostForm(f.tokenURL, map[string]string{
                "client_id":   f.clientID,
                "grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
                "device_code": deviceCode,
            })

            var token struct {
                AccessToken string `json:"access_token"`
                ExpiresIn   int    `json:"expires_in"`
                Error       string `json:"error"`
            }

            if token.Error == "" {
                return &AuthToken{
                    AccessToken: token.AccessToken,
                    ExpiresAt:   time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
                }, nil
            }

            if token.Error != "authorization_pending" {
                return nil, fmt.Errorf("device flow error: %s", token.Error)
            }
        }
    }
}
```

**Supported Providers**: GitHub Copilot (Full), Kiro (AWS CodeWhisperer)

## Provider-Specific Authentication

### GitHub Copilot (Full OAuth Device Flow)

**Authentication Flow**:
1. Device code request to GitHub
2. User authorizes via browser
3. Poll for access token
4. Refresh token management

**Token Storage** (`auths/copilot.json`):
```json
{
  "type": "oauth_device_flow",
  "access_token": "ghu_xxx",
  "refresh_token": "ghr_xxx",
  "expires_at": "2026-02-20T00:00:00Z",
  "quota": {
    "limit": 10000,
    "used": 100
  }
}
```

**Unique Features**:
- Per-credential quota tracking
- Automatic quota rotation
- Multi-credential load balancing

### Kiro (AWS CodeWhisperer)

**Authentication Flow**:
1. Browser-based AWS Builder ID login
2. Interactive web UI (`/v0/oauth/kiro`)
3. SSO integration with AWS Identity Center
4. Token persistence and refresh

**Token Storage** (`auths/kiro.json`):
```json
{
  "type": "oauth_device_flow",
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_at": "2026-02-20T00:00:00Z",
  "identity_id": "us-east-1:12345678-1234-1234-1234-123456789012"
}
```

**Web UI Integration**:
```go
// Route handler for /v0/oauth/kiro
func HandleKiroAuth(c *gin.Context) {
    // Generate device code
    deviceCode, err := kiro.GetDeviceCode()

    // Render interactive HTML page
    c.HTML(200, "kiro_auth.html", gin.H{
        "UserCode":      deviceCode.UserCode,
        "VerificationURL": deviceCode.VerificationURL,
    })
}
```

## Background Token Refresh

### Refresh Worker Architecture

```go
type RefreshWorker struct {
    manager *AuthManager
    interval time.Duration
    leadTime time.Duration
    stopChan chan struct{}
}

func (w *RefreshWorker) Run(ctx context.Context) {
    ticker := time.NewTicker(w.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.checkAndRefresh()
        }
    }
}

func (w *RefreshWorker) checkAndRefresh() {
    now := time.Now()

    for _, auth := range w.manager.ListAll() {
        if auth.ExpiresAt.Sub(now) <= w.leadTime {
            log.Infof("Refreshing token for %s", auth.Provider)

            newToken, err := w.manager.Refresh(auth)
            if err != nil {
                log.Errorf("Failed to refresh %s: %v", auth.Provider, err)
                continue
            }

            if err := w.manager.Update(auth.Provider, newToken); err != nil {
                log.Errorf("Failed to update %s: %v", auth.Provider, err)
            }
        }
    }
}
```

**Configuration**:
```yaml
auth:
  refresh:
    enabled: true
    check_interval: "5m"
    refresh_lead_time: "10m"
```

**Refresh Lead Time**: Tokens are refreshed 10 minutes before expiration to ensure zero downtime.

### Refresh Strategies

#### OAuth Refresh Token Flow
```go
func (m *AuthManager) Refresh(auth *Auth) (*AuthToken, error) {
    if auth.RefreshToken == "" {
        return nil, fmt.Errorf("no refresh token available")
    }

    req := map[string]string{
        "client_id":     m.clientID,
        "client_secret": m.clientSecret,
        "refresh_token": auth.RefreshToken,
        "grant_type":    "refresh_token",
    }

    resp, err := http.PostForm(m.tokenURL, req)
    // ... parse and return new token
}
```

#### Device Flow Re-authorization
```go
func (m *AuthManager) Refresh(auth *Auth) (*AuthToken, error) {
    // For device flow, we need full re-authorization
    // Trigger notification to user
    m.notifyReauthRequired(auth.Provider)

    // Wait for new authorization (with timeout)
    return m.waitForNewAuth(auth.Provider, 30*time.Minute)
}
```

## Credential Management

### Multi-Credential Support

```go
type CredentialPool struct {
    mu       sync.RWMutex
    creds    map[string][]*Auth // provider -> credentials
    strategy SelectionStrategy
}

type SelectionStrategy interface {
    Select(creds []*Auth) *Auth
}

// Round-robin strategy
type RoundRobinStrategy struct {
    counters map[string]int
}

func (s *RoundRobinStrategy) Select(creds []*Auth) *Auth {
    // Increment counter and select next credential
}

// Quota-aware strategy
type QuotaAwareStrategy struct{}

func (s *QuotaAwareStrategy) Select(creds []*Auth) *Auth {
    // Select credential with most remaining quota
}
```

### Quota Tracking

```go
type Quota struct {
    Limit     int64 `json:"limit"`
    Used      int64 `json:"used"`
    Remaining int64 `json:"remaining"`
}

func (q *Quota) Consume(tokens int) error {
    if q.Remaining < int64(tokens) {
        return fmt.Errorf("quota exceeded")
    }
    q.Used += int64(tokens)
    q.Remaining = q.Limit - q.Used
    return nil
}

func (q *Quota) Reset() {
    q.Used = 0
    q.Remaining = q.Limit
}
```

### Per-Request Quota Decuction

```go
func (m *AuthManager) ConsumeQuota(provider string, tokens int) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    for _, auth := range m.creds[provider] {
        if err := auth.Quota.Consume(tokens); err == nil {
            return nil
        }
    }

    return fmt.Errorf("all credentials exhausted for %s", provider)
}
```

## Security Considerations

### Token Storage

**File Permissions**:
- Auth files: `0600` (read/write by owner only)
- Directory: `0700` (access by owner only)

**Encryption** (Optional):
```yaml
auth:
  encryption:
    enabled: true
    key: "ENCRYPTION_KEY_32_BYTES_LONG"
```

### Token Validation

```go
func (m *AuthManager) Validate(auth *Auth) error {
    now := time.Now()

    if auth.ExpiresAt.Before(now) {
        return fmt.Errorf("token expired")
    }

    if auth.Token == "" {
        return fmt.Errorf("empty token")
    }

    return nil
}
```

### Device Fingerprinting

Generate unique device identifiers to satisfy provider security checks:

```go
func GenerateDeviceID() string {
    mac := getMACAddress()
    hostname := getHostname()
    timestamp := time.Now().Unix()

    h := sha256.New()
    h.Write([]byte(mac))
    h.Write([]byte(hostname))
    h.Write([]byte(fmt.Sprintf("%d", timestamp)))

    return hex.EncodeToString(h.Sum(nil))
}
```

## Error Handling

### Authentication Errors

| Error Type | Retryable | Action |
|------------|-----------|--------|
| Invalid credentials | No | Prompt user to re-authenticate |
| Expired token | Yes | Trigger refresh |
| Rate limit exceeded | Yes | Implement backoff |
| Network error | Yes | Retry with exponential backoff |

### Retry Logic

```go
func (m *AuthManager) ExecuteWithRetry(
    ctx context.Context,
    auth *Auth,
    fn func() error,
) error {
    maxRetries := 3
    backoff := time.Second

    for i := 0; i < maxRetries; i++ {
        err := fn()
        if err == nil {
            return nil
        }

        if !isRetryableError(err) {
            return err
        }

        time.Sleep(backoff)
        backoff *= 2
    }

    return fmt.Errorf("max retries exceeded")
}
```

## Monitoring

### Auth Metrics

```go
type AuthMetrics struct {
    TotalCredentials     int
    ExpiredCredentials   int
    RefreshCount         int
    FailedRefreshCount   int
    QuotaUsage           map[string]float64
}
```

### Health Checks

```go
func (m *AuthManager) HealthCheck(ctx context.Context) error {
    for _, auth := range m.ListAll() {
        if err := m.Validate(auth); err != nil {
            return fmt.Errorf("invalid auth for %s: %w", auth.Provider, err)
        }
    }
    return nil
}
```

## API Reference

### Management Endpoints

#### Get All Auths
```
GET /v0/management/auths
```

Response:
```json
{
  "auths": [
    {
      "provider": "claude",
      "type": "api_key",
      "quota": {"limit": 1000000, "used": 50000}
    }
  ]
}
```

#### Add Auth
```
POST /v0/management/auths
```

Request:
```json
{
  "provider": "claude",
  "type": "api_key",
  "token": "sk-ant-xxx"
}
```

#### Delete Auth
```
DELETE /v0/management/auths/{provider}
```

#### Refresh Auth
```
POST /v0/management/auths/{provider}/refresh
```


---

## Source: features/auth/USER.md

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


---
