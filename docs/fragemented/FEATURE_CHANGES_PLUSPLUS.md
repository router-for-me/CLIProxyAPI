# cliproxyapi++ Feature Change Reference (`++` vs baseline)

This document explains what changed in `cliproxyapi++`, why it changed, and how it affects users, integrators, and maintainers.

## 1. Architecture Changes

| Change | What changed in `++` | Why it matters |
|---|---|---|
| Reusable proxy core | Translation and proxy runtime are structured for reusability (`pkg/llmproxy`) | Enables embedding proxy logic into other Go systems and keeps runtime boundaries cleaner |
| Stronger module boundaries | Operational and integration concerns are separated from API surface orchestration | Easier upgrades, clearer ownership, lower accidental coupling |

## 2. Authentication and Identity Changes

| Change | What changed in `++` | Why it matters |
|---|---|---|
| Copilot-grade auth support | Extended auth handling for enterprise Copilot-style workflows | More stable integration for organizations depending on tokenized auth stacks |
| Kiro/AWS login path support | Additional OAuth/login handling pathways and operational UX around auth | Better compatibility for multi-provider enterprise environments |
| Token lifecycle automation | Background refresh and expiration handling | Reduces downtime from token expiry and manual auth recovery |

## 3. Provider and Model Routing Changes

| Change | What changed in `++` | Why it matters |
|---|---|---|
| Broader provider matrix | Expanded provider adapter and model mapping surfaces | More routing options without changing client-side OpenAI API integrations |
| Unified model translation | Stronger mapping between OpenAI-style model requests and provider-native model names | Lower integration friction and fewer provider mismatch errors |
| Cooldown and throttling controls | Runtime controls for rate-limit pressure and provider-specific cooldown windows | Better stability under burst traffic and quota pressure |

## 4. Security and Governance Changes

| Change | What changed in `++` | Why it matters |
|---|---|---|
| Defense-in-depth hardening | Added stricter operational defaults and hardened deployment assumptions | Safer default posture in production environments |
| Protected core path governance | Workflow-level controls around critical core logic paths | Reduces accidental regressions in proxy translation internals |
| Device and session consistency controls | Deterministic identity/session behavior for strict provider checks | Fewer auth anomalies in long-running deployments |

## 5. Operations and Delivery Changes

| Change | What changed in `++` | Why it matters |
|---|---|---|
| Stronger CI/CD posture | Expanded release, build, and guard workflows | Faster detection of regressions and safer release cadence |
| Multi-arch/container focus | Production deployment paths optimized for container-first ops | Better portability across heterogeneous infra |
| Runtime observability surfaces | Improved log and management endpoints | Easier production debugging and incident response |

## 6. API and Compatibility Surface

| Change | What changed in `++` | Why it matters |
|---|---|---|
| OpenAI-compatible core retained | `/v1/chat/completions` and `/v1/models` compatibility maintained | Existing OpenAI-style clients can migrate with minimal API churn |
| Expanded management endpoints | Added operational surfaces for config/auth/runtime introspection | Better operations UX without changing core client API |

## 7. Migration Impact Summary

- **Technical users**: gain higher operational stability, better auth longevity, and stronger multi-provider behavior.
- **External integrators**: keep OpenAI-compatible interfaces while gaining wider provider compatibility.
- **Internal maintainers**: get cleaner subsystem boundaries and stronger guardrails for production evolution.
