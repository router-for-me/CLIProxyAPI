# Merged Fragmented Markdown

## Source: features/security/SPEC.md

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


---

## Source: features/security/USER.md

# User Guide: Security Hardening

## Understanding Security in cliproxyapi++

cliproxyapi++ is built with a "Defense in Depth" philosophy, meaning multiple layers of security protect your deployments. This guide explains how to configure and use these security features effectively.

## Quick Security Checklist

**Before deploying to production**:

```bash
# 1. Verify Docker image is signed
docker pull KooshaPari/cliproxyapi-plusplus:latest
docker trust verify KooshaPari/cliproxyapi-plusplus:latest

# 2. Set secure file permissions
chmod 600 auths/*.json
chmod 700 auths/

# 3. Enable TLS
# Edit config.yaml to enable TLS (see below)

# 4. Enable encryption
# Generate encryption key and set in config.yaml

# 5. Configure rate limiting
# Set appropriate limits in config.yaml
```

## Container Security

### Hardened Docker Deployment

**docker-compose.yml**:
```yaml
services:
  cliproxy:
    image: KooshaPari/cliproxyapi-plusplus:latest
    container_name: cliproxyapi++

    # Security options
    security_opt:
      - no-new-privileges:true
    read_only: true
    tmpfs:
      - /tmp:noexec,nosuid,size=100m
    cap_drop:
      - ALL
    cap_add:
      - NET_BIND_SERVICE

    # Non-root user
    user: "65534:65534"

    # Volumes (writable only for these)
    volumes:
      - ./config.yaml:/config/config.yaml:ro
      - ./auths:/auths:rw
      - ./logs:/logs:rw
      - ./tls:/tls:ro

    # Network
    ports:
      - "8317:8317"

    # Resource limits
    deploy:
      resources:
        limits:
          cpus: '2'
          memory: 1G
        reservations:
          cpus: '0.5'
          memory: 256M

    restart: unless-stopped
```

**Explanation**:
- `no-new-privileges`: Prevents processes from gaining more privileges
- `read_only`: Makes container filesystem immutable (attackers can't modify binaries)
- `tmpfs:noexec`: Prevents execution of files in `/tmp`
- `cap_drop:ALL`: Drops all Linux capabilities
- `cap_add:NET_BIND_SERVICE`: Only adds back the ability to bind ports
- `user:65534:65534`: Runs as non-root "nobody" user

### Seccomp Profiles (Advanced)

**Custom seccomp profile**:
```bash
# Save seccomp profile
cat > seccomp-profile.json << 'EOF'
{
  "defaultAction": "SCMP_ACT_ERRNO",
  "syscalls": [
    {
      "names": ["read", "write", "open", "close", "socket", "bind", "listen"],
      "action": "SCMP_ACT_ALLOW"
    }
  ]
}
EOF

# Use in docker-compose
security_opt:
  - seccomp:./seccomp-profile.json
```

## TLS Configuration

### Enable HTTPS

**config.yaml**:
```yaml
server:
  port: 8317
  tls:
    enabled: true
    cert_file: "/tls/tls.crt"
    key_file: "/tls/tls.key"
    min_version: "1.2"
    cipher_suites:
      - "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"
      - "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"
```

### Generate Self-Signed Certificate (Testing)

```bash
# Generate private key
openssl genrsa -out tls.key 2048

# Generate certificate
openssl req -new -x509 -key tls.key -out tls.crt -days 365 \
  -subj "/C=US/ST=State/L=City/O=Organization/CN=localhost"

# Set permissions
chmod 600 tls.key
chmod 644 tls.crt
```

### Use Let's Encrypt (Production)

```bash
# Install certbot
sudo apt-get install certbot

# Generate certificate
sudo certbot certonly --standalone -d proxy.example.com

# Copy to tls directory
sudo cp /etc/letsencrypt/live/proxy.example.com/fullchain.pem tls/tls.crt
sudo cp /etc/letsencrypt/live/proxy.example.com/privkey.pem tls/tls.key

# Set permissions
sudo chown $USER:$USER tls/tls.key tls/tls.crt
chmod 600 tls/tls.key
chmod 644 tls/tls.crt
```

## Credential Encryption

### Enable Encryption

**config.yaml**:
```yaml
auth:
  encryption:
    enabled: true
    key: "YOUR_32_BYTE_ENCRYPTION_KEY_HERE"
```

### Generate Encryption Key

```bash
# Method 1: Using openssl
openssl rand -base64 32

# Method 2: Using Python
python3 -c "import secrets; print(secrets.token_urlsafe(32))"

# Method 3: Using /dev/urandom
head -c 32 /dev/urandom | base64
```

### Environment Variable (Recommended)

```yaml
auth:
  encryption:
    enabled: true
    key: "${CLIPROXY_ENCRYPTION_KEY}"
```

```bash
# Set in environment
export CLIPRO_ENCRYPTION_KEY="$(openssl rand -base64 32)"

# Use in docker-compose
environment:
  - CLIPRO_ENCRYPTION_KEY=${CLIPRO_ENCRYPTION_KEY}
```

### Migrating Existing Credentials

When enabling encryption, existing credentials remain unencrypted. To encrypt them:

```bash
# 1. Enable encryption in config.yaml
# 2. Restart service
# 3. Re-add credentials (they will be encrypted)
curl -X POST http://localhost:8317/v0/management/auths \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "claude",
    "type": "api_key",
    "token": "sk-ant-xxxxx"
  }'
```

## Access Control

### IP Allowlisting

**config.yaml**:
```yaml
server:
  security:
    ip_allowlist:
      enabled: true
      allowed_ips:
        - "10.0.0.0/8"      # Private network
        - "192.168.1.100"   # Specific IP
        - "203.0.113.0/24"  # Public network
```

**Block all except allowed**:
```yaml
server:
  security:
    ip_allowlist:
      enabled: true
      allowed_ips:
        - "10.0.0.0/8"
      deny_all: true  # Block all except allowed_ips
```

### IP Denylisting

```yaml
server:
  security:
    ip_denylist:
      enabled: true
      denied_ips:
        - "192.0.2.0/24"    # Test network
        - "198.51.100.100"  # Specific IP
```

### IP-Based Rate Limiting

```yaml
server:
  security:
    rate_limiting:
      enabled: true
      requests_per_second: 10
      burst: 20
      per_ip: true
```

## Rate Limiting

### Global Rate Limiting

```yaml
server:
  rate_limit:
    enabled: true
    requests_per_second: 100
    burst: 200
```

### Per-Provider Rate Limiting

```yaml
providers:
  claude:
    rate_limit:
      requests_per_minute: 100
      tokens_per_minute: 100000
  openai:
    rate_limit:
      requests_per_minute: 500
      tokens_per_minute: 200000
```

### Quota-Based Rate Limiting

```yaml
providers:
  claude:
    quota:
      limit: 1000000  # Tokens per month
      reset: "monthly"
```

## Security Headers

### Enable Security Headers

**config.yaml**:
```yaml
server:
  security:
    headers:
      enabled: true
      strict_transport_security: "max-age=31536000; includeSubDomains"
      content_type_options: "nosniff"
      frame_options: "DENY"
      xss_protection: "1; mode=block"
      content_security_policy: "default-src 'self'"
```

**Headers added to all responses**:
```
Strict-Transport-Security: max-age=31536000; includeSubDomains
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
X-XSS-Protection: 1; mode=block
Content-Security-Policy: default-src 'self'
```

## Audit Logging

### Enable Audit Logging

**config.yaml**:
```yaml
logging:
  audit:
    enabled: true
    file: "/logs/audit.log"
    format: "json"
    events:
      - "auth_success"
      - "auth_failure"
      - "token_refresh"
      - "config_change"
      - "provider_request"
      - "security_violation"
```

### View Audit Logs

```bash
# View all audit events
tail -f logs/audit.log

# Filter for auth failures
grep "auth_failure" logs/audit.log

# Filter for security violations
grep "security_violation" logs/audit.log

# Pretty print JSON logs
cat logs/audit.log | jq '.'
```

### Audit Log Format

```json
{
  "timestamp": "2026-02-19T23:00:00Z",
  "event_type": "auth_failure",
  "provider": "claude",
  "user_id": "user@example.com",
  "ip": "192.168.1.100",
  "result": "invalid_token",
  "details": {
    "reason": "Token expired"
  }
}
```

## Security Monitoring

### Enable Metrics

**config.yaml**:
```yaml
metrics:
  enabled: true
  port: 9090
  path: "/metrics"
```

**Security metrics exposed**:
```
# HELP cliproxy_auth_failures_total Total authentication failures
# TYPE cliproxy_auth_failures_total counter
cliproxy_auth_failures_total{provider="claude"} 5

# HELP cliproxy_rate_limit_violations_total Total rate limit violations
# TYPE cliproxy_rate_limit_violations_total counter
cliproxy_rate_limit_violations_total{ip="192.168.1.100"} 10

# HELP cliproxy_security_events_total Total security events
# TYPE cliproxy_security_events_total counter
cliproxy_security_events_total{event_type="suspicious_activity"} 1
```

### Query Metrics

```bash
# Get auth failure rate
curl http://localhost:9090/metrics | grep auth_failures

# Get rate limit violations
curl http://localhost:9090/metrics | grep rate_limit_violations

# Get all security events
curl http://localhost:9090/metrics | grep security_events
```

## Incident Response

### Block Suspicious IP

```bash
# Add to denylist
curl -X POST http://localhost:8317/v0/management/security/ip-denylist \
  -H "Content-Type: application/json" \
  -d '{
    "ip": "192.168.1.100",
    "reason": "Suspicious activity"
  }'
```

### Revoke Credentials

```bash
# Delete credential
curl -X DELETE http://localhost:8317/v0/management/auths/claude
```

### Enable Maintenance Mode

```yaml
server:
  maintenance_mode: true
  message: "Scheduled maintenance in progress"
```

## Security Best Practices

### Development

- [ ] Never commit credentials to version control
- [ ] Use pre-commit hooks to scan for secrets
- [ ] Enable security headers in development
- [ ] Test with different user permissions
- [ ] Review audit logs regularly

### Staging

- [ ] Use staging-specific credentials
- [ ] Enable all security features
- [ ] Test rate limiting
- [ ] Verify TLS configuration
- [ ] Monitor security metrics

### Production

- [ ] Use production TLS certificates (not self-signed)
- [ ] Enable encryption for credentials
- [ ] Configure IP allowlisting
- [ ] Set appropriate rate limits
- [ ] Enable comprehensive audit logging
- [ ] Set up security alerts
- [ ] Regular security audits
- [ ] Rotate credentials quarterly
- [ ] Keep dependencies updated

## Troubleshooting

### TLS Certificate Issues

**Problem**: `certificate verify failed`

**Solutions**:
1. Verify certificate file exists: `ls -la tls/tls.crt`
2. Check certificate is valid: `openssl x509 -in tls/tls.crt -text -noout`
3. Verify key matches cert: `openssl x509 -noout -modulus -in tls/tls.crt | openssl md5`
4. Check file permissions: `chmod 600 tls/tls.key`

### Encryption Key Issues

**Problem**: `decryption failed`

**Solutions**:
1. Verify encryption key is 32 bytes
2. Check key is set in config/environment
3. Ensure key hasn't changed
4. If key changed, re-add credentials

### Rate Limiting Too Strict

**Problem**: Legitimate requests blocked

**Solutions**:
1. Increase rate limit in config
2. Increase burst size
3. Whitelist trusted IPs
4. Use per-user rate limiting instead of per-IP

### IP Allowlisting Issues

**Problem**: Can't access from allowed IP

**Solutions**:
1. Verify IP address: `curl ifconfig.me`
2. Check CIDR notation
3. Verify allowlist is enabled
4. Check denylist doesn't block

### Audit Logs Not Working

**Problem**: No events in audit log

**Solutions**:
1. Verify audit logging is enabled
2. Check file permissions on log directory
3. Verify events are enabled in config
4. Check disk space

## Security Audits

### Pre-Deployment Checklist

```bash
#!/bin/bash
# security-check.sh

echo "Running security checks..."

# Check file permissions
echo "Checking file permissions..."
find auths/ -type f ! -perm 600
find auths/ -type d ! -perm 700

# Check for secrets
echo "Scanning for secrets..."
git secrets --scan

# Check TLS
echo "Verifying TLS..."
openssl x509 -in tls/tls.crt -checkend 86400

# Check dependencies
echo "Scanning dependencies..."
trivy fs .

echo "Security checks complete!"
```

Run before deployment:
```bash
./security-check.sh
```

## Next Steps

- See [SPEC.md](./SPEC.md) for technical security details
- See [../auth/](../auth/) for authentication security
- See [../operations/](../operations/) for operational security
- See [../../api/](../../api/) for API security


---
