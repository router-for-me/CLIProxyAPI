# VS Code Claude Code through CLIProxyAPI

This example launches VS Code or VS Code Insiders with an isolated user data
folder where the Claude Code extension is configured to use a local CLIProxyAPI
router.

It is intended for people who want both modes available:

- Normal VS Code / Claude Code: use your regular Claude login or subscription.
- Router VS Code / Claude Code: use CLIProxyAPI through environment variables.

The example does not modify your normal VS Code settings and does not modify
`~/.claude/settings.json`.

## Files

- `code-claude-router.zsh` — launcher that creates isolated VS Code settings and
  starts VS Code with those settings.
- `install.zsh` — optional installer that copies the launcher to `~/bin`.

## Requirements

- macOS or Linux shell with `zsh`.
- VS Code command line launcher installed:
  - `code-insiders`, or
  - `code`.
- Claude Code for VS Code extension installed in the VS Code build you launch.
- Python 3, used only to write JSON safely.
- CLIProxyAPI running and reachable, default:
  - `http://127.0.0.1:8317`
- CLIProxyAPI config file containing `api-keys`, default:
  - repository `config.yaml`

Start CLIProxyAPI from the repository root if it is not already running:

```bash
go run ./cmd/server --config config.yaml --local-model
```

## Quick start from the repository

From the CLIProxyAPI repository root:

```bash
chmod +x examples/VSCODE/code-claude-router.zsh
examples/VSCODE/code-claude-router.zsh .
```

Open the Claude Code extension in that VS Code window. The extension should not
ask you to log in because `claudeCode.disableLoginPrompt` is enabled for this
isolated user data folder and the launcher injects the Anthropic-compatible
router variables.

## Optional install

```bash
chmod +x examples/VSCODE/install.zsh
examples/VSCODE/install.zsh
```

Then from any project:

```bash
code-claude-router .
```

If `~/bin` is not on your `PATH`, add it to your shell startup file:

```bash
export PATH="$HOME/bin:$PATH"
```

## Normal Claude subscription use

To use your normal Claude subscription, do not use this launcher. Open VS Code
normally, for example:

```bash
code-insiders .
```

or:

```bash
code .
```

Normal VS Code uses your regular user data directory, settings, and Claude login.
The router launcher uses a separate user data directory.

## What the launcher writes

The launcher writes this generated settings file:

- VS Code Insiders default:
  - `~/.vscode-claude-router-insiders-user-data/User/settings.json`
- VS Code stable default:
  - `~/.vscode-claude-router-user-data/User/settings.json`

The generated settings include:

- `claudeCode.disableLoginPrompt: true`
- `claudeCode.preferredLocation: panel`
- `claudeCode.environmentVariables` with:
  - `ANTHROPIC_BASE_URL`
  - `ANTHROPIC_AUTH_TOKEN`
  - `ANTHROPIC_MODEL`
  - `ANTHROPIC_DEFAULT_OPUS_MODEL`
  - `ANTHROPIC_DEFAULT_SONNET_MODEL`
  - `ANTHROPIC_DEFAULT_HAIKU_MODEL`
  - `ANTHROPIC_DEFAULT_FABLE_MODEL`
  - `ANTHROPIC_SMALL_FAST_MODEL`
  - `CLAUDE_CODE_SUBAGENT_MODEL`
  - `CLAUDE_CODE_AUTO_COMPACT_WINDOW`
  - `CLAUDE_CODE_EFFORT_LEVEL`

The local router token is copied into the isolated VS Code settings file because
VS Code extensions need their environment variables available when the extension
process starts. Do not commit generated settings files.

## Configuration

Override paths and launcher behavior with environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `CLI_PROXY_API_REPO` | auto-detected repo root | CLIProxyAPI repository path |
| `CLAUDE_CODE_ROUTER_CONFIG` | `$CLI_PROXY_API_REPO/config.yaml` | Config file used to read the first `api-keys` value |
| `CLAUDE_CODE_ROUTER_URL` | `http://127.0.0.1:8317` | CLIProxyAPI Anthropic-compatible base URL |
| `CLAUDE_ROUTER_VSCODE_CLI` | `code-insiders` if available, else `code` | VS Code CLI executable |
| `CLAUDE_ROUTER_VSCODE_USER_DATA_DIR` | `~/.vscode-claude-router-*-user-data` | Isolated VS Code user data directory |
| `CLAUDE_ROUTER_VSCODE_EXTENSIONS_DIR` | matching normal extensions dir | Extension directory to reuse |
| `CLAUDE_ROUTER_VSCODE_PROFILE` | `Claude Router` | VS Code profile name |
| `CLAUDE_ROUTER_VSCODE_LOCATION` | `panel` | Claude extension location (`panel` or `sidebar`) |

Override model assignments with environment variables:

| Variable | Default |
| --- | --- |
| `CLAUDE_ROUTER_MAIN_MODEL` | `claude-opus-5` |
| `CLAUDE_ROUTER_OPUS_MODEL` | `claude-opus-5` |
| `CLAUDE_ROUTER_SONNET_MODEL` | `claude-sonnet-5` |
| `CLAUDE_ROUTER_HAIKU_MODEL` | `claude-sonnet-5` |
| `CLAUDE_ROUTER_FABLE_MODEL` | `claude-fable-5` |
| `CLAUDE_ROUTER_SMALL_FAST_MODEL` | `claude-sonnet-5` |
| `CLAUDE_ROUTER_SUBAGENT_MODEL` | `claude-opus-5` |
| `CLAUDE_CODE_AUTO_COMPACT_WINDOW` | `262144` |
| `CLAUDE_CODE_EFFORT_LEVEL` | `max` |

Example using OpenAI-routed model names exposed by CLIProxyAPI:

```bash
CLAUDE_ROUTER_MAIN_MODEL='gpt-5.6-luna(high)' \
CLAUDE_ROUTER_OPUS_MODEL='gpt-5.6-sol(medium)' \
CLAUDE_ROUTER_SONNET_MODEL='gpt-5.6-luna(max)' \
CLAUDE_ROUTER_HAIKU_MODEL='gpt-5.5' \
CLAUDE_ROUTER_FABLE_MODEL='gpt-5.6-sol(high)' \
CLAUDE_ROUTER_SMALL_FAST_MODEL='gpt-5.5' \
CLAUDE_ROUTER_SUBAGENT_MODEL='gpt-5.6-luna(high)' \
examples/VSCODE/code-claude-router.zsh .
```

## Validation

Check the launcher syntax:

```bash
zsh -n examples/VSCODE/code-claude-router.zsh
zsh -n examples/VSCODE/install.zsh
```

Check the router:

```bash
TOKEN=$(awk '/^api-keys:/ {f=1; next} f && /^[[:space:]]*-[[:space:]]*/ {sub(/^[[:space:]]*-[[:space:]]*/, ""); gsub(/^[\047\"]|[\047\"]$/, ""); print; exit}' config.yaml)
curl -fsS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8317/v1/models >/dev/null
```

After launching VS Code with this example, open Claude Code and run `/status` to
confirm the base URL and selected model.

## Troubleshooting

### The extension still asks for login

Make sure you launched VS Code through `code-claude-router.zsh`, not the Dock or
normal `code` command. The router settings only apply to the isolated user data
directory used by the launcher.

### The Claude Code extension is missing

Install the extension in the VS Code build you launch, or point
`CLAUDE_ROUTER_VSCODE_EXTENSIONS_DIR` at the extension directory that already has
`anthropic.claude-code` installed.

Common extension directories:

- VS Code Insiders: `~/.vscode-insiders/extensions`
- VS Code stable: `~/.vscode/extensions`

### The router warning appears

The launcher checks `$CLAUDE_CODE_ROUTER_URL/v1/models`. If that fails, start
CLIProxyAPI or set the correct `CLAUDE_CODE_ROUTER_URL`.

### I want to reset the router VS Code window

Remove the isolated user data folder:

```bash
rm -rf ~/.vscode-claude-router-insiders-user-data
rm -rf ~/.vscode-claude-router-user-data
```

This does not remove normal VS Code settings.
