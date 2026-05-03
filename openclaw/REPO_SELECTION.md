# Repository Selection Matrix

Use this matrix to decide which repository to clone first.

## Default choice

Start with:
- `https://github.com/luyuehm/CLIProxyAPI`

Add later if needed:
- `https://github.com/luyuehm/Cli-Proxy-API-Management-Center`

## Matrix

| Scenario | Required repo | Optional repo |
|---|---|---|
| I want CLIProxyAPI service code | `luyuehm/CLIProxyAPI` | — |
| I want OpenClaw install/bootstrap/self-heal/cron | `luyuehm/CLIProxyAPI` | — |
| I want to sync a service fork with upstream | `luyuehm/CLIProxyAPI` | — |
| I want a browser Management API UI | `luyuehm/CLIProxyAPI` | `luyuehm/Cli-Proxy-API-Management-Center` |
| I am only developing the UI itself | — | `luyuehm/Cli-Proxy-API-Management-Center` |
| I want a complete operator setup | `luyuehm/CLIProxyAPI` | `luyuehm/Cli-Proxy-API-Management-Center` |

## Practical rule

- Treat `CLIProxyAPI` as the main repository.
- Treat `Cli-Proxy-API-Management-Center` as a companion UI repository.
