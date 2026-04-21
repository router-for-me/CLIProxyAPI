# Project Relationship

This fork and the companion Management Center repository are related but not redundant.

## Roles

### 1. `luyuehm/CLIProxyAPI`
This repository is the primary service fork.
It contains:
- the CLIProxyAPI service code
- OpenClaw autonomy/install/sync/self-heal/bootstrap templates
- project-level operational governance for OpenClaw-managed deployments

### 2. `luyuehm/Cli-Proxy-API-Management-Center`
This repository is the companion Management UI.
It contains:
- the React/TypeScript web interface for the Management API
- browser-side operations for config, logs, credentials, and usage
- a human operator surface, not an automation/governance layer

## Boundary

- Use **CLIProxyAPI fork** as the main project/fork entrypoint.
- Use **Management Center** as the companion UI project.
- Do not merge them into one repository unless you are intentionally coupling service code, OpenClaw ops assets, and a separate UI release cycle.

## Recommended linking

- The CLIProxyAPI fork should link to the Management Center as a companion human-ops UI.
- The Management Center should link back to the CLIProxyAPI fork for OpenClaw autonomy, self-heal, cron, and bootstrap documentation.
