# Technical Specification: Security Hardening ("Defense in Depth")

## Overview

**cliproxyapi++** implements a comprehensive "Defense in Depth" security philosophy with multiple layers of protection: CI-enforced code integrity, hardened container images, device fingerprinting, and secure credential management.

## Security Architecture

### Defense Layers

```
Layer 1: Code Integrity
├── Path Guard (CI enforcement)
├── Signed releases
└── Multi-arch builds

Layer 2: Container Hardening
├── Minimal base image (Alpine 3.22.0)
├── Non-root user
├── Read-only filesystem
└── Seccomp profiles

Layer 3: Credential Security
├── Encrypted storage
├── Secure file permissions
├── Token refresh isolation
└── Device fingerprinting

Layer 4: Network Security
├── TLS only
├── Request validation
├── Rate limiting
└── IP allowlisting

Layer 5: Operational Security
├── Audit logging
├── Secret scanning
├── Dependency scanning
└── Vulnerability management
```

## Layer 1: Code Integrity

### Path Guard CI Enforcement

**Purpose**: Prevent unauthorized changes to critical translation logic during pull requests.

**Implementation** (`.github/workflows/pr-path-guard.yml`):
```yaml
name: Path Guard
on:
  pull_request:
    paths:
      - 'pkg/llmproxy/translator/**'
      - 'pkg/llmproxy/auth/**'

jobs:
  guard:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Check path protection
        run: |
          # Only allow changes from trusted maintainers
          if ! git log --format="%an" ${{ github.event.pull_request.base.sha }}..${{ github.sha }} | grep -q "KooshaPari"; then
            echo "::error::Unauthorized changes to protected paths"
            exit 1
          fi

      - name: Verify no translator logic changes
        run: |
          # Ensure core translation logic hasn't been tampered
          if git diff ${{ github.event.pull_request.base.sha }}..${{ github.sha }} --name-only | grep -q "pkg/llmproxy/translator/.*\.go$"; then
            echo "::warning::Translator logic changed - requires maintainer review"
          fi
```

**Protected Paths**:
- `pkg/llmproxy/translator/` - Core translation logic
- `pkg/llmproxy/auth/` - Authentication flows
- `pkg/llmproxy/provider/` - Provider execution

**Authorization Rules**:
- Only repository maintainers can modify
- All changes require at least 2 maintainer approvals
- Must pass security review

### Signed Releases

**Purpose**: Ensure released artifacts are authentic and tamper-proof.

**Implementation** (`.goreleaser.yml`):
```yaml
signs:
  - artifacts: checksum
    args:
      - "--batch"
      - "--local-user"
      - "${GPG_FINGERPRINT}"
```

**Verification**:
```bash
# Download release
wget https://github.com/KooshaPari/cliproxyapi-plusplus/releases/download/v6.0.0/cliproxyapi-plusplus_6.0.0_checksums.txt

# Download signature
wget https://github.com/KooshaPari/cliproxyapi-plusplus/releases/download/v6.0.0/cliproxyapi-plusplus_6.0.0_checksums.txt.sig

# Import GPG key
gpg --keyserver keyserver.ubuntu.com --recv-keys XXXXXXXX

# Verify signature
gpg --verify cliproxyapi-plusplus_6.0.0_checksums.txt.sig cliproxyapi-plusplus_6.0.0_checksums.txt

# Verify checksum
sha256sum -c cliproxyapi-plusplus_6.0.0_checksums.txt
```

### Multi-Arch Builds

**Purpose**: Provide consistent security across architectures.

**Platforms**:
- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`

**CI Build Matrix**:
```yaml
strategy:
  matrix:
    goos: [linux, darwin]
    goarch: [amd64, arm64]
```

## Layer 2: Container Hardening

### Minimal Base Image

**Base**: Alpine Linux 3.22.0

**Dockerfile**:
```dockerfile
FROM alpine:3.22.0 AS builder

# Install build dependencies
RUN apk add --no-cache \
    ca-certificates \
    gcc \
    musl-dev

# Build application
COPY . .
RUN go build -o cliproxyapi cmd/server/main.go

# Final stage - minimal runtime
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /cliproxyapi /cliproxyapi

# Non-root user
USER 65534:65534

# Read-only filesystem
VOLUME ["/config", "/auths", "/logs"]

ENTRYPOINT ["/cliproxyapi"]
```

**Security Benefits**:
- Minimal attack surface (no shell, no package manager)
- No unnecessary packages
- Static binary linking
- Reproducible builds

### Security Context

**docker-compose.yml**:
```yaml
services:
  cliproxy:
    image: KooshaPari/cliproxyapi-plusplus:latest
    security_opt:
      - no-new-privileges:true
    read_only: true
    tmpfs:
      - /tmp:noexec,nosuid,size=100m
    cap_drop:
      - ALL
    cap_add:
      - NET_BIND_SERVICE
    user: "65534:65534"
```

**Explanation**:
- `no-new-privileges`: Prevent privilege escalation
- `read_only`: Immutable filesystem
- `tmpfs`: Noexec on temporary files
- `cap_drop:ALL`: Drop all capabilities
- `cap_add:NET_BIND_SERVICE`: Only allow binding ports
- `user:65534:65534`: Run as non-root (nobody)

### Seccomp Profiles

**Custom seccomp profile** (`seccomp-profile.json`):
```json
{
  "defaultAction": "SCMP_ACT_ERRNO",
  "architectures": ["SCMP_ARCH_X86_64", "SCMP_ARCH_AARCH64"],
  "syscalls": [
    {
      "names": ["read", "write", "open", "close", "stat", "fstat", "lstat"],
      "action": "SCMP_ACT_ALLOW"
    },
    {
      "names": ["socket", "bind", "listen", "accept", "connect"],
      "action": "SCMP_ACT_ALLOW"
    },
    {
      "names": ["execve", "fork", "clone"],
      "action": "SCMP_ACT_DENY"
    }
  ]
}
```

**Usage**:
```yaml
security_opt:
  - seccomp:/path/to/seccomp-profile.json
```

## Layer 3: Credential Security

### Encrypted Storage

**Purpose**: Protect credentials at rest.

**Implementation**:
```go
type CredentialEncryptor struct {
    key []byte
}

func NewCredentialEncryptor(key string) (*CredentialEncryptor, error) {
    if len(key) != 32 {
        return nil, fmt.Errorf("key must be 32 bytes")
    }

    return &CredentialEncryptor{
        key: []byte(key),
    }, nil
}

func (e *CredentialEncryptor) Encrypt(data []byte) ([]byte, error) {
    block, err := aes.NewCipher(e.key)
    if err != nil {
        return nil, err
    }

    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }

    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return nil, err
    }

    return gcm.Seal(nonce, nonce, data, nil), nil
}

func (e *CredentialEncryptor) Decrypt(data []byte) ([]byte, error) {
    block, err := aes.NewCipher(e.key)
    if err != nil {
        return nil, err
    }

    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }

    nonceSize := gcm.NonceSize()
    if len(data) < nonceSize {
        return nil, fmt.Errorf("ciphertext too short")
    }

    nonce, ciphertext := data[:nonceSize], data[nonceSize:]
    return gcm.Open(nil, nonce, ciphertext, nil)
}
```

**Configuration**:
```yaml
auth:
  encryption:
    enabled: true
    key: "YOUR_32_BYTE_ENCRYPTION_KEY_HERE"
```

### Secure File Permissions

**Automatic enforcement**:
```go
func SetSecurePermissions(path string) error {
    // File: 0600 (rw-------)
    // Directory: 0700 (rwx------)
    if info, err := os.Stat(path); err == nil {
        if info.IsDir() {
            return os.Chmod(path, 0700)
        }
        return os.Chmod(path, 0600)
    }
    return fmt.Errorf("file not found: %s", path)
}
```

**Verification**:
```go
func VerifySecurePermissions(path string) error {
    info, err := os.Stat(path)
    if err != nil {
        return err
    }

    mode := info.Mode().Perm()
    if info.IsDir() && mode != 0700 {
        return fmt.Errorf("directory has insecure permissions: %o", mode)
    }

    if !info.IsDir() && mode != 0600 {
        return fmt.Errorf("file has insecure permissions: %o", mode)
    }

    return nil
}
```

### Token Refresh Isolation

**Purpose**: Prevent credential leakage during refresh.

**Implementation**:
```go
type RefreshWorker struct {
    isolatedMemory bool
}

func (w *RefreshWorker) RefreshToken(auth *Auth) (*AuthToken, error) {
    // Use isolated goroutine
    result := make(chan *RefreshResult)
    go w.isolatedRefresh(auth, result)

    select {
    case res := <-result:
        if res.Error != nil {
            return nil, res.Error
        }
        // Clear memory after use
        defer w.scrubMemory(res.Token)
        return res.Token, nil
    case <-time.After(30 * time.Second):
        return nil, fmt.Errorf("refresh timeout")
    }
}

func (w *RefreshWorker) scrubMemory(token *AuthToken) {
    // Zero out sensitive data
    for i := range token.AccessToken {
        token.AccessToken = ""
    }
    token.RefreshToken = ""
}
```

### Device Fingerprinting

**Purpose**: Generate unique, immutable device identifiers for provider security checks.

**Implementation**:
```go
func GenerateDeviceFingerprint() (string, error) {
    mac, err := getMACAddress()
    if err != nil {
        return "", err
    }

    hostname, err := os.Hostname()
    if err != nil {
        return "", err
    }

    // Create stable fingerprint
    h := sha256.New()
    h.Write([]byte(mac))
    h.Write([]byte(hostname))
    h.Write([]byte("cliproxyapi++")) // Salt

    fingerprint := hex.EncodeToString(h.Sum(nil))

    // Store for persistence
    return fingerprint, nil
}

func getMACAddress() (string, error) {
    interfaces, err := net.Interfaces()
    if err != nil {
        return "", err
    }

    for _, iface := range interfaces {
        if iface.Flags&net.FlagUp == 0 {
            continue
        }
        if len(iface.HardwareAddr) == 0 {
            continue
        }

        return iface.HardwareAddr.String(), nil
    }

    return "", fmt.Errorf("no MAC address found")
}
```

**Usage**:
```go
fingerprint, _ := GenerateDeviceFingerprint()

// Send with requests
headers["X-Device-Fingerprint"] = fingerprint
```

## Layer 4: Network Security

### TLS Enforcement

**Configuration**:
```yaml
server:
  port: 8317
  tls:
    enabled: true
    cert_file: "/config/tls.crt"
    key_file: "/config/tls.key"
    min_version: "1.2"
    cipher_suites:
      - "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"
      - "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"
```

**HTTP Strict Transport Security (HSTS)**:
```go
func addSecurityHeaders(c *gin.Context) {
    c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
    c.Header("X-Content-Type-Options", "nosniff")
    c.Header("X-Frame-Options", "DENY")
    c.Header("X-XSS-Protection", "1; mode=block")
    c.Header("Content-Security-Policy", "default-src 'self'")
}
```

### Request Validation

**Schema validation**:
```go
type ChatRequestValidator struct {
    validator *validator.Validate
}

func (v *ChatRequestValidator) Validate(req *openai.ChatCompletionRequest) error {
    return v.validator.Struct(req)
}

// Max tokens limits
func (v *ChatRequestValidator) ValidateMaxTokens(maxTokens int) error {
    if maxTokens > 4096 {
        return fmt.Errorf("max_tokens exceeds limit of 4096")
    }
    return nil
}
```

### Rate Limiting

**Token bucket implementation**:
```go
type RateLimiter struct {
    limiters map[string]*rate.Limiter
    mu       sync.RWMutex
}

func NewRateLimiter() *RateLimiter {
    return &RateLimiter{
        limiters: make(map[string]*rate.Limiter),
    }
}

func (r *RateLimiter) Allow(ip string) bool {
    r.mu.Lock()
    defer r.mu.Unlock()

    limiter, exists := r.limiters[ip]
    if !exists {
        limiter = rate.NewLimiter(rate.Limit(10), 20) // 10 req/s, burst 20
        r.limiters[ip] = limiter
    }

    return limiter.Allow()
}
```

**Per-provider rate limiting**:
```yaml
providers:
  claude:
    rate_limit:
      requests_per_minute: 100
      tokens_per_minute: 100000
```

### IP Allowlisting

**Configuration**:
```yaml
server:
  security:
    ip_allowlist:
      enabled: true
      allowed_ips:
        - "10.0.0.0/8"
        - "192.168.1.100"
    ip_denylist:
      - "0.0.0.0/0"  # Block all except allowed
```

**Implementation**:
```go
type IPFilter struct {
    allowed []*net.IPNet
    denied  []*net.IPNet
}

func (f *IPFilter) IsAllowed(ip net.IP) bool {
    // Check denylist first
    for _, deny := range f.denied {
        if deny.Contains(ip) {
            return false
        }
    }

    // Check allowlist
    if len(f.allowed) == 0 {
        return true // No allowlist = allow all
    }

    for _, allow := range f.allowed {
        if allow.Contains(ip) {
            return true
        }
    }

    return false
}
```

## Layer 5: Operational Security

### Audit Logging

**Structured logging**:
```go
type AuditLogger struct {
    logger *slog.Logger
}

func (a *AuditLogger) LogAuthEvent(event AuthEvent) {
    a.logger.LogAttrs(
        context.Background(),
        slog.LevelInfo,
        "auth_event",
        slog.String("event_type", event.Type),
        slog.String("provider", event.Provider),
        slog.String("user_id", event.UserID),
        slog.String("ip", event.IP),
        slog.Time("timestamp", event.Timestamp),
        slog.String("result", event.Result),
    )
}
```

**Audit events**:
- Authentication attempts (success/failure)
- Token refresh
- Credential access
- Configuration changes
- Provider requests

### Secret Scanning

**Pre-commit hook** (`.git/hooks/pre-commit`):
```bash
#!/bin/bash

# Scan for potential secrets
if git diff --cached --name-only | xargs grep -lE "sk-[a-zA-Z0-9]{48}|AIza[a-zA-Z0-9_-]{35}"; then
    echo "::error::Potential secrets detected in staged files"
    exit 1
fi
```

**CI secret scanning**:
```yaml
- name: Scan for secrets
  run: |
    pip install git-secrets
    git secrets --register-aws
    git secrets --scan
```

### Dependency Scanning

**CI integration**:
```yaml
- name: Run Trivy vulnerability scanner
  uses: aquasecurity/trivy-action@master
  with:
    scan-type: 'fs'
    scan-ref: '.'
    format: 'sarif'
    output: 'trivy-results.sarif'
```

### Vulnerability Management

**Weekly scan schedule**:
```yaml
name: Vulnerability Scan
on:
  schedule:
    - cron: '0 0 * * 0'  # Weekly

jobs:
  scan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run Trivy
        run: |
          trivy fs --severity HIGH,CRITICAL --exit-code 1 .
```

## Security Monitoring

### Metrics

**Security metrics exposed**:
```go
type SecurityMetrics struct {
    AuthFailures      int64
    RateLimitViolations int64
    SuspiciousActivity int64
    BlockedIPs        int64
}
```

**Alerting**:
```yaml
alerts:
  - name: High auth failure rate
    condition: auth_failures > 100
    duration: 5m
    action: notify_admin

  - name: Rate limit violations
    condition: rate_limit_violations > 50
    duration: 1m
    action: block_ip
```

### Incident Response

**Procedure**:
1. Detect anomaly via metrics/logs
2. Verify incident (false positive check)
3. Contain (block IP, disable provider)
4. Investigate (analyze logs)
5. Remediate (patch, rotate credentials)
6. Document (incident report)

## Compliance

### SOC 2 Readiness

- **Access Control**: Role-based access, MFA support
- **Change Management**: CI enforcement, audit trails
- **Data Protection**: Encryption at rest/transit
- **Monitoring**: 24/7 logging, alerting
- **Incident Response**: Documented procedures

### GDPR Compliance

- **Data Minimization**: Only store necessary data
- **Right to Erasure**: Credential deletion API
- **Data Portability**: Export credentials API
- **Audit Trails**: Complete logging

## Security Checklist

**Pre-Deployment**:
- [ ] All dependencies scanned (no HIGH/CRITICAL)
- [ ] Secrets scanned and removed
- [ ] TLS enabled with strong ciphers
- [ ] File permissions set (0600/0700)
- [ ] Rate limiting enabled
- [ ] IP allowlisting configured
- [ ] Audit logging enabled
- [ ] Container hardened (non-root, read-only)

**Post-Deployment**:
- [ ] Monitor security metrics
- [ ] Review audit logs daily
- [ ] Update dependencies monthly
- [ ] Rotate credentials quarterly
- [ ] Test incident response procedures
