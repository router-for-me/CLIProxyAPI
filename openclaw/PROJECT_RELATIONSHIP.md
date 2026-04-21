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


## Do you need both repositories?

Not always.

### Use only `luyuehm/CLIProxyAPI` when
- you mainly need the service fork itself
- you want OpenClaw install/bootstrap/self-heal/cron templates
- you do not need a separate browser management UI right now

### Use both repositories when
- you want the service fork **and**
- you also want a dedicated browser-based Management API UI

### Use only `luyuehm/Cli-Proxy-API-Management-Center` temporarily when
- you are only working on the management UI itself
- you already have a running CLIProxyAPI backend elsewhere

But for a complete operator/deployment setup, the recommended default is:
- `CLIProxyAPI` as the main service + OpenClaw autonomy repository
- `Cli-Proxy-API-Management-Center` as the optional companion UI
