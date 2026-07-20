# Claude Code Model Profiles

This example provides three zsh profiles for switching every Claude Code model
tier before starting `claude`:

| Profile | Main | Opus tier | Sonnet tier | Fast/Haiku/Fable tier | Subagents |
| --- | --- | --- | --- | --- | --- |
| `openai` | `gpt-5.5` | `gpt-5.5` | `gpt-5.5` | `gpt-4o-mini` | `gpt-5.5` |
| `claude` | `claude-sonnet-5` | `claude-opus-4.6` | `claude-sonnet-5` | `claude-fable-5` | `claude-sonnet-5` |
| `mixed` | `claude-sonnet-5` | `gpt-5.5` | `claude-sonnet-5` | `kimi-k2.7-code` | `gpt-5.5` |

The profiles configure Claude Code only. CLIProxyAPI is used as the local
Anthropic-compatible router because Claude Code accepts a single
`ANTHROPIC_BASE_URL`, while the OpenAI and mixed profiles contain models from
multiple vendors.

`ENABLE_TOOL_SEARCH` is never assigned a value. The helper unsets it when a
profile is selected so a stale shell value such as `false` cannot disable tool
search.

## Install

Copy the profile script to a stable location:

```bash
mkdir -p ~/.config/claude-profiles
cp examples/claude-code-profiles/claude-profile.zsh \
  ~/.config/claude-profiles/claude-profile.zsh
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

After editing the repository copy, reinstall it and reload the shell:

```bash
cp examples/claude-code-profiles/claude-profile.zsh \
  ~/.config/claude-profiles/claude-profile.zsh
source ~/.zshrc
claude-profile mixed
```

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
claude-profile mixed
```

Finally, start `claude`, run `/status`, and send a small prompt to verify the
selected main model end to end.
