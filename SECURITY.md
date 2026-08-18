# Security Policy

CLIProxyAPI accepts client credentials and model requests, translates traffic across multiple upstream AI providers, and may run as a shared network service. Vulnerabilities in authentication, routing, logging, configuration, or provider adapters can expose API keys, prompts, responses, or internal network resources.

## Reporting a vulnerability

Do not disclose exploitable details in a public issue, discussion, pull request, or log attachment.

Use GitHub's private vulnerability reporting flow under **Security → Advisories → Report a vulnerability**. If private reporting is unavailable, open a public issue titled `[security] private contact requested` without technical details and ask a maintainer to establish a private channel.

A useful report includes:

- affected version or commit and deployment mode;
- the relevant endpoint, provider adapter, authentication mode, or configuration path;
- required attacker capabilities and realistic impact;
- minimal, sanitized reproduction steps or a proof of concept;
- whether credentials, prompts, responses, files, or internal network services are affected;
- a suggested mitigation, if available.

Never attach real provider keys, bearer tokens, cookies, complete configuration files, private prompts, user responses, or unredacted proxy logs. Rotate any exposed secret before continuing the report.

## Security-relevant areas

Examples include:

- authentication or authorization bypasses on proxy, management, or callback endpoints;
- one tenant or client receiving another tenant's credentials, prompts, responses, models, or usage data;
- provider keys appearing in logs, errors, metrics, traces, exported configuration, or process arguments;
- server-side request forgery through configurable base URLs, redirects, webhooks, or proxy settings;
- unsafe forwarding of `Authorization`, cookies, or provider-specific headers to the wrong host;
- request smuggling, header injection, path confusion, or unbounded request and response bodies;
- model-routing mistakes that cross account or provider trust boundaries;
- insecure TLS defaults or disabled certificate verification;
- path traversal, unsafe file permissions, destructive configuration writes, or command execution;
- malicious dependencies, release artifacts, container images, or third-party contributions;
- denial of service caused by unbounded concurrency, retries, streaming buffers, or parser work.

Provider outages, model-quality differences, billing disputes, and upstream API changes are normally support issues unless CLIProxyAPI creates an additional confidentiality, integrity, or authorization impact.

## Safe testing

Test only against systems, accounts, credentials, and providers you control. Prefer a local disposable deployment and mock upstream servers. Do not access another user's traffic, disrupt public endpoints, retain exposed data, or scan unrelated networks. Stop once the minimum impact is demonstrated.

## Coordinated disclosure

Maintainers should validate the report, identify affected versions and deployment modes, prepare a fix or mitigation, rotate project-controlled secrets when needed, and coordinate public disclosure with the reporter. Advisories should document fixed versions, configuration changes, log or cache cleanup, and credential-rotation steps. Credit will be given unless anonymity is requested.
