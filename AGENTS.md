# AGENTS.md

This file provides guidance to AI agents working with code in this repository.

## Quick Start

```bash
# Build
go build -o cliproxy ./cmd/cliproxy

# Run
./cliproxy --config config.yaml

# Docker
docker compose up -d
```

## Environment

```bash
# Required environment variables
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-..."
```

---

## Development Philosophy

### Extend, Never Duplicate

- NEVER create a v2 file. Refactor the original.
- NEVER create a new class if an existing one can be made generic.
- NEVER create custom implementations when an OSS library exists.
- Before writing ANY new code: search the codebase for existing patterns.

### Primitives First

- Build generic building blocks before application logic.
- A provider interface + registry is better than N isolated classes.
- Template strings > hardcoded messages. Config-driven > code-driven.

### Research Before Implementing

- Check pkg.go.dev for existing libraries.
- Search GitHub for 80%+ implementations to fork/adapt.

---

## Library Preferences (DO NOT REINVENT)

| Need | Use | NOT |
|------|-----|-----|
| HTTP router | chi | custom router |
| Logging | zerolog | fmt.Print |
| Config | viper | manual env parsing |
| Validation | go-playground/validator | manual if/else |
| Rate limiting | golang.org/x/time/rate | custom limiter |

---

## Code Quality Non-Negotiables

- Zero new lint suppressions without inline justification
- All new code must pass: go fmt, go vet, golint
- Max function: 40 lines
- No placeholder TODOs in committed code

---

## Verifiable Constraints

| Metric | Threshold | Enforcement |
|--------|-----------|-------------|
| Tests | 80% coverage | CI gate |
| Lint | 0 errors | golangci-lint |
| Security | 0 critical | trivy scan |

---

## Provider Support

| Provider | Auth | Status |
|----------|------|--------|
| OpenAI | API Key | ✅ |
| Anthropic | API Key | ✅ |
| Azure OpenAI | API Key/OAuth | ✅ |
| Google Gemini | API Key | ✅ |
| AWS Bedrock | IAM | ✅ |
| Kiro (CodeWhisperer) | OAuth | ✅ |
| GitHub Copilot | OAuth | ✅ |
| Ollama | Local | ✅ |

---

## Kush Ecosystem

This project is part of the Kush multi-repo system:

```
kush/
├── thegent/         # Agent orchestration
├── agentapi++/      # HTTP API for coding agents
├── cliproxy++/      # LLM proxy with multi-provider support (this repo)
├── tokenledger/     # Token and cost tracking
├── 4sgm/           # Python tooling workspace
├── civ/             # Deterministic simulation
├── parpour/        # Spec-first planning
└── pheno-sdk/       # Python SDK
```
