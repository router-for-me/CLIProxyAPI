# Security Policy

> [!IMPORTANT]
> **Bulk Reporting Policy:** If you need to submit multiple vulnerability reports in bulk, **you must contact us first** ([support@quantumnous.com](mailto:support@quantumnous.com)) to coordinate the submission process. Uncoordinated bulk submissions have caused significant disruption to our team, and we will take the following actions:
>
> 1. **All uncoordinated bulk reports will be closed without review.**
> 2. **Repeated offenders may be blocked** from further submissions.
>
> We welcome thorough security research, but please reach out before submitting multiple reports.

## Supported Versions

We provide security updates for the following versions:


| Version | Supported          |
| ------- | ------------------ |
| Latest  | :white_check_mark: |
| Older   | :x:                |


We strongly recommend that users always use the latest version for the best security and features.

## Reporting a Vulnerability

We take security vulnerability reports very seriously. If you discover a security issue, please follow the steps below for responsible disclosure.

### How to Report

**Do NOT** report security vulnerabilities in public GitHub Issues.

To report a security issue, please use the GitHub Security Advisories tab to "[Open a draft security advisory](https://github.com/QuantumNous/new-api/security/advisories/new)". This is the preferred method as it provides a built-in private communication channel.

Alternatively, you can report via email:

- **Email:** [support@quantumnous.com](mailto:support@quantumnous.com)
- **Subject:** `[SECURITY] Security Vulnerability Report`

### What to Include

To help us understand and resolve the issue more quickly, please include the following information in your report:

1. **Vulnerability Type** - Brief description of the vulnerability (e.g., SQL injection, XSS, authentication bypass, etc.)
2. **Affected Component** - Affected file paths, endpoints, or functional modules
3. **Reproduction Steps** - Detailed steps to reproduce
4. **Impact Assessment** - Potential security impact and severity assessment
5. **Proof of Concept** - If possible, provide proof of concept code or screenshots (do not test in production environments)
6. **Suggested Fix** - If you have a fix suggestion, please provide it
7. **Your Contact Information** - So we can communicate with you

## Response Process

1. **Acknowledgment:** We will acknowledge receipt of your report within **48 hours**.
2. **Initial Assessment:** We will complete an initial assessment and communicate with you within **7 days**.
3. **Fix Development:** Based on the severity of the vulnerability, we will prioritize developing a fix.
4. **Security Advisory:** After the fix is released, we will publish a security advisory (if applicable).
5. **Credit:** If you wish, we will credit your contribution in the security advisory.

## Security Best Practices

When deploying and using New API, we recommend following these security best practices:

### Deployment Security

- **Use HTTPS:** Always serve over HTTPS to ensure transport layer security
- **Firewall Configuration:** Only open necessary ports and restrict access to management interfaces
- **Regular Updates:** Update to the latest version promptly to receive security patches
- **Environment Isolation:** Use separate database and Redis instances in production

### API Key Security

- **Key Protection:** Do not expose API keys in client-side code or public repositories
- **Least Privilege:** Create different API keys for different purposes, following the principle of least privilege
- **Regular Rotation:** Rotate API keys regularly
- **Monitor Usage:** Monitor API key usage and detect anomalies promptly

### Database Security

- **Strong Passwords:** Use strong passwords to protect database access
- **Network Isolation:** Database should not be directly exposed to the public internet
- **Regular Backups:** Regularly backup the database and verify backup integrity
- **Access Control:** Limit database user permissions, following the principle of least privilege

## Security-Related Configuration

Please ensure the following security-related environment variables and settings are properly configured:

- `SESSION_SECRET` - Use a strong random string
- `SQL_DSN` - Ensure database connection uses secure configuration
- `REDIS_CONN_STRING` - If using Redis, ensure secure connection

For detailed configuration instructions, please refer to the project documentation.

## Disclaimer

This project is provided "as is" without any express or implied warranty. Users should assess the security risks of using this software in their environment.