---
title: "Codex Agency MCP Startup Residual Config"
tags: ["codex", "mcp", "oh-my-codex", "agency", "windows", "cc-switch", "claude"]
created: 2026-06-13T01:15:52.366Z
updated: 2026-06-13T01:23:56.010Z
sources: ["C:/Users/76578/.codex/config.toml", "C:/Users/76578/.claude.json", "C:/Users/76578/.cc-switch/cc-switch.db", "C:/Users/76578/.cc-switch/settings.json"]
links: []
category: environment
confidence: high
schemaVersion: 1
---

# Codex Agency MCP Startup Residual Config

### [Environment] Codex Agency MCP Startup Residual Config

**Problem**
Codex startup can keep reporting `MCP client for agency failed to start: program not found` even after project-level MCP files or Claude project MCP settings are deleted.

**Cause**
The active startup source can be the user-level Codex config at `C:/Users/76578/.codex/config.toml`, specifically a `[mcp_servers.agency]` block with `command = "agency-mcp-server"`. If `agency-mcp-server` is not on PATH, Codex attempts to launch it on startup and emits the warning. Deleting project MCP files or disabling entries in `.claude.json` does not remove this Codex user-level entry.

**Impact**
Codex App/CLI startup shows incomplete MCP startup and may repeatedly retry or warn, confusing agents into searching project files or Claude config instead of the actual Codex config source.

**Resolution**
Remove the `[mcp_servers.agency]` block from `C:/Users/76578/.codex/config.toml`, then restart Codex. Verify with `Select-String -LiteralPath "$env:USERPROFILE\.codex\config.toml" -Pattern 'mcp_servers\.agency|agency-mcp-server'` and `Get-Command agency-mcp-server -ErrorAction SilentlyContinue`.

**References**
`C:/Users/76578/.codex/config.toml` lines around the MCP server section; `.claude.json` may contain separate disabled historical `agency` entries but those are not the Codex startup source.

---

## Update (2026-06-13T01:21:10.245Z)

### [Environment] Codex Agency MCP Startup Residual Config

**Problem**
Codex startup can keep reporting `MCP client for agency failed to start: program not found` even after project-level MCP files, Claude project MCP settings, or the visible `C:/Users/76578/.codex/config.toml` block are deleted.

**Cause**
The active startup source is `C:/Users/76578/.codex/config.toml`, but CC Switch can regenerate that file from its provider templates. The CC Switch database at `C:/Users/76578/.cc-switch/cc-switch.db` stored Codex provider `settings_config.config` values containing `[mcp_servers.agency]` with `command = "agency-mcp-server"`, including current provider `b243bdae-c859-4e01-87a3-db35c949668b`. The `mcp_servers` table also had row `agency` with `server_config={"command":"agency-mcp-server"}` and `enabled_codex=1`. Because `agency-mcp-server` is not on PATH, Codex emitted the warning after CC Switch rewrote the config.

**Impact**
Manual edits to `.codex/config.toml` are temporary when CC Switch later switches or reapplies a Codex provider. Agents can waste time deleting project or Claude MCP files while the real persistent source is CC Switch's provider DB/template.

**Resolution**
Stop `cc-switch.exe`, back up `C:/Users/76578/.cc-switch/cc-switch.db`, remove the `[mcp_servers.agency]` block from all Codex provider `settings_config.config` values, and set the `agency` row in `mcp_servers` to disabled for Codex/Claude/Gemini/OpenCode/Hermes. Also remove the visible block from `C:/Users/76578/.codex/config.toml`, then restart Codex/CC Switch. Verify no Codex provider template contains `agency-mcp-server` and that the `mcp_servers` row has all `enabled_* = 0`.

**References**
Current provider was read from `C:/Users/76578/.cc-switch/settings.json` as `currentProviderCodex=b243bdae-c859-4e01-87a3-db35c949668b`. Backup created at `C:/Users/76578/.cc-switch/backups/cc-switch-before-remove-agency-mcp-20260613_091905.db`.

---

## Update (2026-06-13T01:23:56.010Z)

### [Environment] Codex/Claude Agency MCP Startup Residual Config

**Problem**
Codex or Claude startup can keep reporting `agency` MCP startup failures such as `program not found` even after visible project-level MCP files are deleted. The same stale `agency` entry can appear in both Codex and Claude surfaces.

**Cause**
For Codex, the visible startup source is `C:/Users/76578/.codex/config.toml`, but CC Switch can regenerate that file from provider templates stored in `C:/Users/76578/.cc-switch/cc-switch.db`. The database stored Codex provider `settings_config.config` values containing `[mcp_servers.agency]` with `command = "agency-mcp-server"`, including current provider `b243bdae-c859-4e01-87a3-db35c949668b`. The `mcp_servers` table also had row `agency` with `server_config={"command":"agency-mcp-server"}`. For Claude, `C:/Users/76578/.claude.json` can also retain a global `mcpServers.agency` object plus project `disabledMcpServers` references.

**Impact**
Manual edits to `.codex/config.toml` or project MCP files are temporary or incomplete when CC Switch later reapplies provider state, and Claude may still contain stale global/history entries. Agents can waste time deleting the wrong layer.

**Resolution**
Stop `cc-switch.exe`, back up `C:/Users/76578/.cc-switch/cc-switch.db`, remove `[mcp_servers.agency]` from all Codex provider `settings_config.config` values, and set the `agency` row in `mcp_servers` to disabled for all clients. Remove the visible block from `C:/Users/76578/.codex/config.toml`. For Claude, back up `C:/Users/76578/.claude.json`, then remove any `mcpServers.agency` object and remove `agency` from all `disabledMcpServers` arrays. Verify `Select-String` finds no `agency-mcp-server` in `.codex/config.toml` or `.claude.json`, and verify CC Switch DB has no provider template containing `agency-mcp-server` while the `agency` MCP row has all `enabled_* = 0`.

**References**
CC Switch DB backup: `C:/Users/76578/.cc-switch/backups/cc-switch-before-remove-agency-mcp-20260613_091905.db`. Claude config backup: `C:/Users/76578/.claude.json.before-remove-agency-mcp-20260613_092305.bak`.
