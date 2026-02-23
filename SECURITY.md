# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 6.0.x   | :white_check_mark: |
| < 6.0   | :x:                |

## Reporting a Vulnerability

We take the security of **cliproxyapi++** seriously. If you discover a security vulnerability, please do NOT open a public issue. Instead, report it privately.

Please report any security concerns directly to the maintainers at [kooshapari@gmail.com](mailto:kooshapari@gmail.com) (assuming this as the email for KooshaPari).

### What to include
- A detailed description of the vulnerability.
- Steps to reproduce (proof of concept).
- Potential impact.
- Any suggested fixes or mitigations.

We will acknowledge your report within 48 hours and provide a timeline for resolution.

## Hardening Measures

**cliproxyapi++** incorporates several security-hardening features:

- **Minimal Docker Images**: Based on Alpine Linux to reduce attack surface.
- **Path Guard**: GitHub Actions that monitor and protect critical translation and core logic files.
- **Rate Limiting**: Built-in mechanisms to prevent DoS attacks.
- **Device Fingerprinting**: Enhanced authentication security using device-specific metadata.
- **Dependency Scanning**: Automatic scanning for vulnerable Go modules.

---
Thank you for helping keep the community secure!
