# Claude Code Model Profiles

This example provides four zsh profiles for switching every Claude Code model
tier before starting `claude`:

| Profile | Main | Opus tier | Sonnet tier | Fast/Haiku/Fable tier | Subagents |
| --- | --- | --- | --- | --- | --- |
| `openai` | `gpt-5.6-luna(high)` | `gpt-5.6-sol(medium)` | `gpt-5.6-luna(max)` | Haiku: `gpt-5.5`; Fable: `gpt-5.6-sol(high)` | `gpt-5.6-luna(high)` |
| `claude` | `claude-opus-5` | `claude-opus-5` | `claude-sonnet-5` | Haiku: `claude-sonnet-5`; Fable: `claude-fable-5` | `claude-opus-5` |
| `kimi` | `kimi-k2.7-code` | `kimi-k2.7-code` | `kimi-k2.7-code` | `kimi-k2.7-code` | `kimi-k2.7-code` |
| `mixed` | `claude-opus-5` | `gpt-5.6-sol(high)` | `claude-opus-5` | Haiku: `claude-fable-5`; Fable: `gpt-5.6-sol(medium)` | `gpt-5.6-luna(max)` |

The profiles configure Claude Code only. CLIProxyAPI is used as the local
Anthropic-compatible router because Claude Code accepts a single
`ANTHROPIC_BASE_URL`, while the OpenAI and mixed profiles contain models from
multiple vendors.

`ENABLE_TOOL_SEARCH` is never assigned a value. The helper unsets it when a
profile is selected so a stale shell value such as `false` cannot disable tool
search.

The OpenAI and Claude profiles use Claude Code's `max` effort. The Kimi and
mixed profiles use `high`, which is the highest effort advertised by
`kimi-k2.7-code`. The mixed profile keeps Kimi in the small/fast slot but uses
Claude for the main model and GPT for subagents, avoiding Kimi's current
Anthropic-compatible thinking and tool-loop limitations during create, review,
Agent, and Workflow tasks.

The all-Kimi profile is best treated as experimental for direct chat and simple
tool use. Changing the shell effort prevents the unsupported `max` value, but
it cannot rewrite Claude Code's `thinking.type` from `adaptive` to the
`enabled` value required by Kimi K2.7. That compatibility fix belongs in
CLIProxyAPI's Kimi request handling.

## Install

Copy the profile script to a stable location:

```bash
mkdir -p ~/.config/claude-profiles
cp examples/claude-code-profiles/claude-profile.zsh \
  ~/.config/claude-profiles/claude-profile.zsh
```

Alternatively, use the updater below. It installs the script and refreshes
the current shell in one step:

```bash
source examples/claude-code-profiles/update-claude-profile.zsh
```

When neither router variable is set, the updater automatically uses the
repository's readable `config.yaml`. This fallback only applies while running
the updater from a repository checkout; configure
`CLAUDE_CODE_ROUTER_CONFIG` in `~/.zshrc` for new terminals.

If an older profile was already active before installing the updater, pass
the profile name once so it can reapply the right exports:

```bash
source examples/claude-code-profiles/update-claude-profile.zsh mixed
```

Add the following to `~/.zshrc`:

```bash
# Path to the CLIProxyAPI configuration containing api-keys.
export CLAUDE_CODE_ROUTER_CONFIG="/absolute/path/to/CLIProxyAPI/config.yaml"

# Optional; defaults to http://127.0.0.1:8317.
export CLAUDE_CODE_ROUTER_URL="http://127.0.0.1:8317"

source "$HOME/.config/claude-profiles/claude-profile.zsh"
```

The helper reads the first entry under `api-keys` from the configuration file.
It does not copy the key into the profile script. As an alternative, set the
key directly in your environment:

```bash
export CLAUDE_CODE_ROUTER_TOKEN="your-local-router-api-key"
```

Do not commit real API keys or OAuth credentials.

Reload the current shell after installation:

```bash
source ~/.zshrc
```

The router must be running before Claude Code is started. For example, from the
CLIProxyAPI repository:

```bash
go run ./cmd/server --config config.yaml --local-model
```

## Use

Select a profile, then start Claude Code from any directory:

```bash
claude-profile openai
claude
```

```bash
claude-profile claude
claude
```

```bash
claude-profile kimi
claude
```

```bash
claude-profile mixed
claude
```

The selected variables remain active only in the current shell and processes
started from it. A new terminal starts without a selected profile until
`claude-profile` is run again.

Clear all profile variables and return to Claude Code's native login and
configuration with:

```bash
claude-profile clear
```

Inside Claude Code, run `/status` to confirm the base URL and main model. The
model names do not need to appear in Claude Code's `/model` menu because that
menu contains a fixed set of Claude aliases.

## Update models

First inspect the models currently exposed by the router:

```bash
curl -sS \
  -H "Authorization: Bearer $CLAUDE_CODE_ROUTER_TOKEN" \
  "$CLAUDE_CODE_ROUTER_URL/v1/models" |
  jq -r '.data[].id' |
  sort
```

If `CLAUDE_CODE_ROUTER_TOKEN` is not set because the profile reads
`CLAUDE_CODE_ROUTER_CONFIG`, select any profile first. That populates
`ANTHROPIC_AUTH_TOKEN`, which can be used for the query:

```bash
claude-profile mixed
curl -sS \
  -H "Authorization: Bearer $ANTHROPIC_AUTH_TOKEN" \
  "$ANTHROPIC_BASE_URL/v1/models" |
  jq -r '.data[].id' |
  sort
```

Edit the applicable block in
`examples/claude-code-profiles/claude-profile.zsh`:

```bash
case "$profile" in
  openai)
    export ANTHROPIC_MODEL="new-main-model"
    export ANTHROPIC_DEFAULT_OPUS_MODEL="new-opus-tier-model"
    export ANTHROPIC_DEFAULT_SONNET_MODEL="new-sonnet-tier-model"
    export ANTHROPIC_DEFAULT_HAIKU_MODEL="new-fast-model"
    export ANTHROPIC_DEFAULT_FABLE_MODEL="new-fast-model"
    export ANTHROPIC_SMALL_FAST_MODEL="new-fast-model"
    export CLAUDE_CODE_SUBAGENT_MODEL="new-subagent-model"
    ;;
esac
```

All tier variables should use exact model identifiers returned by `/v1/models`.
Setting every tier prevents background summaries, title generation, and
subagents from silently falling back to unsupported model names. This follows
the tier configuration described in the
[Kimi Claude Code guide](https://platform.kimi.ai/docs/guide/claude-code-kimi).

After editing the repository copy, update the installed script and refresh the
current shell with:

```bash
source examples/claude-code-profiles/update-claude-profile.zsh
```

The updater reapplies the currently selected profile, so all of its exported
model variables use the new values immediately. If no profile is active, run
one after updating, for example `claude-profile mixed`.

Do not run the updater as `./update-claude-profile.zsh` when you need to
refresh the current terminal. A child process cannot export variables into its
parent shell; source it as shown above. You can override the install location
with `CLAUDE_CODE_PROFILE_INSTALL_DIR` if needed.

If changes do not take effect, inspect `~/.claude/settings.json`. Values in its
`env` object override variables exported by the profile. Remove stale endpoint,
credential, model, `CLAUDE_CODE_*`, or `ENABLE_TOOL_SEARCH` entries before
retrying.

## Validate changes

Check zsh syntax:

```bash
zsh -n examples/claude-code-profiles/claude-profile.zsh
```

Then select each profile and verify its printed model assignments:

```bash
claude-profile openai
claude-profile claude
claude-profile kimi
claude-profile mixed
```

Finally, start `claude`, run `/status`, and send a small prompt to verify the
selected main model end to end.
