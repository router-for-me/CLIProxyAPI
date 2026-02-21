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
