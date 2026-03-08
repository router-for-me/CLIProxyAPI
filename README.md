# CLIProxyAPI Plus

English | [Chinese](README_CN.md)

This is the Plus version of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI), adding support for third-party providers on top of the mainline project.

All third-party provider support is maintained by community contributors; CLIProxyAPI does not provide technical support. Please contact the corresponding community maintainer if you need assistance.

The Plus release stays in lockstep with the mainline features.

## Differences from the Mainline

[![z.ai](https://assets.router-for.me/english-5-0.jpg)](https://z.ai/subscribe?ic=8JVLJQFSKB)

## New Features (Plus Enhanced)

GLM CODING PLAN is a subscription service designed for AI coding, starting at just $10/month. It provides access to their flagship GLM-4.7 & （GLM-5 Only Available  for Pro Users）model across 10+ popular AI coding tools (Claude Code, Cline, Roo Code, etc.), offering developers top-tier, fast, and stable coding experiences.

## Kiro Authentication

### CLI Login

> **Note:** Google/GitHub login is not available for third-party applications due to AWS Cognito restrictions.

**AWS Builder ID** (recommended):

```bash
# Device code flow
./CLIProxyAPI --kiro-aws-login

# Authorization code flow
./CLIProxyAPI --kiro-aws-authcode
```

**Import token from Kiro IDE:**

```bash
./CLIProxyAPI --kiro-import
```

To get a token from Kiro IDE:

1. Open Kiro IDE and login with Google (or GitHub)
2. Find the token file: `~/.kiro/kiro-auth-token.json`
3. Run: `./CLIProxyAPI --kiro-import`

**AWS IAM Identity Center (IDC):**

```bash
./CLIProxyAPI --kiro-idc-login --kiro-idc-start-url https://d-xxxxxxxxxx.awsapps.com/start

# Specify region
./CLIProxyAPI --kiro-idc-login --kiro-idc-start-url https://d-xxxxxxxxxx.awsapps.com/start --kiro-idc-region us-west-2
```

**Additional flags:**

| Flag | Description |
|------|-------------|
| `--no-browser` | Don't open browser automatically, print URL instead |
| `--no-incognito` | Use existing browser session (Kiro defaults to incognito). Useful for corporate SSO that requires an authenticated browser session |
| `--kiro-idc-start-url` | IDC Start URL (required with `--kiro-idc-login`) |
| `--kiro-idc-region` | IDC region (default: `us-east-1`) |
| `--kiro-idc-flow` | IDC flow type: `authcode` (default) or `device` |

### Web-based OAuth Login

Access the Kiro OAuth web interface at:

```
http://your-server:8317/v0/oauth/kiro
```

This provides a browser-based OAuth flow for Kiro (AWS CodeWhisperer) authentication with:
- AWS Builder ID login
- AWS Identity Center (IDC) login
- Token import from Kiro IDE

## Quick Deployment with Docker

### One-Command Deployment

```bash
# Clone and enter the repository
git clone https://github.com/router-for-me/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus

# Prepare persistent directories
mkdir -p config auths logs

# Build and start
docker compose up -d --build
```

### Configuration

After the first start, edit `config/config.yaml`:

```yaml
# Basic configuration example
port: 8317

# Add your provider configurations here
```

### Update to Latest Version

```bash
git pull
docker compose up -d --build
```

The container entrypoint automatically creates `config/config.yaml` from `config.example.yaml` on first start.

## Contributing

This project only accepts pull requests that relate to third-party provider support. Any pull requests unrelated to third-party provider support will be rejected.

If you need to submit any non-third-party provider changes, please open them against the [mainline](https://github.com/router-for-me/CLIProxyAPI) repository.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
