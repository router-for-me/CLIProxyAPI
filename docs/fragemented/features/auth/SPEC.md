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
