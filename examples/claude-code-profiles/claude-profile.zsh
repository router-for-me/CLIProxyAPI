# Claude Code model profiles for zsh.
#
# Configure either CLAUDE_CODE_ROUTER_TOKEN or CLAUDE_CODE_ROUTER_CONFIG before
# calling claude-profile. The configuration file fallback reads the first key
# under api-keys from a CLIProxyAPI YAML configuration.

_claude_profile_clear() {
  unset ANTHROPIC_BASE_URL
  unset ANTHROPIC_API_KEY
  unset ANTHROPIC_AUTH_TOKEN
  unset ANTHROPIC_MODEL
  unset ANTHROPIC_SMALL_FAST_MODEL
  unset ANTHROPIC_DEFAULT_OPUS_MODEL
  unset ANTHROPIC_DEFAULT_OPUS_MODEL_NAME
  unset ANTHROPIC_DEFAULT_SONNET_MODEL
  unset ANTHROPIC_DEFAULT_SONNET_MODEL_NAME
  unset ANTHROPIC_DEFAULT_HAIKU_MODEL
  unset ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME
  unset ANTHROPIC_DEFAULT_FABLE_MODEL
  unset ANTHROPIC_DEFAULT_FABLE_MODEL_NAME
  unset CLAUDE_CODE_SUBAGENT_MODEL
  unset CLAUDE_CODE_AUTO_COMPACT_WINDOW
  unset CLAUDE_CODE_EFFORT_LEVEL
  unset ENABLE_TOOL_SEARCH
}

_claude_profile_router_token() {
  if [[ -n "${CLAUDE_CODE_ROUTER_TOKEN:-}" ]]; then
    print -r -- "$CLAUDE_CODE_ROUTER_TOKEN"
    return
  fi

  local router_config="${CLAUDE_CODE_ROUTER_CONFIG:-}"
  [[ -n "$router_config" && -r "$router_config" ]] || return 1
  awk '
    /^api-keys:/ { found=1; next }
    found && /^[[:space:]]*-[[:space:]]*/ {
      sub(/^[[:space:]]*-[[:space:]]*/, "")
      gsub(/^[\047\"]|[\047\"]$/, "")
      print
      exit
    }
  ' "$router_config"
}

claude-profile() {
  local profile="${1:-}"
  local router_token

  case "$profile" in
    openai|claude|kimi|mixed) ;;
    off|clear)
      _claude_profile_clear
      print "Claude profile cleared; Claude Code will use its native login/configuration."
      return
      ;;
    *)
      print "Usage: claude-profile {openai|claude|kimi|mixed|clear}"
      return 2
      ;;
  esac

  router_token="$(_claude_profile_router_token)"
  if [[ -z "$router_token" ]]; then
    print "Could not find the router API key."
    print "Set CLAUDE_CODE_ROUTER_TOKEN or CLAUDE_CODE_ROUTER_CONFIG and try again."
    return 1
  fi

  _claude_profile_clear
  export ANTHROPIC_BASE_URL="${CLAUDE_CODE_ROUTER_URL:-http://127.0.0.1:8317}"
  export ANTHROPIC_AUTH_TOKEN="$router_token"
  export CLAUDE_CODE_AUTO_COMPACT_WINDOW="262144"
  export CLAUDE_CODE_EFFORT_LEVEL="max"

  case "$profile" in
    openai)
      export ANTHROPIC_MODEL="gpt-5.6-luna(high)"
      export ANTHROPIC_DEFAULT_OPUS_MODEL="gpt-5.6-sol(medium)"
      export ANTHROPIC_DEFAULT_SONNET_MODEL="gpt-5.6-luna(max)"
      export ANTHROPIC_DEFAULT_HAIKU_MODEL="gpt-5.5"
      export ANTHROPIC_DEFAULT_FABLE_MODEL="gpt-5.6-sol(high)"
      export ANTHROPIC_SMALL_FAST_MODEL="gpt-5.5"
      export CLAUDE_CODE_SUBAGENT_MODEL="gpt-5.5(high)"
      ;;
    claude)
      export ANTHROPIC_MODEL="claude-sonnet-5"
      export ANTHROPIC_DEFAULT_OPUS_MODEL="claude-opus-4.6"
      export ANTHROPIC_DEFAULT_SONNET_MODEL="claude-sonnet-5"
      export ANTHROPIC_DEFAULT_HAIKU_MODEL="claude-fable-5"
      export ANTHROPIC_DEFAULT_FABLE_MODEL="claude-fable-5"
      export ANTHROPIC_SMALL_FAST_MODEL="claude-fable-5"
      export CLAUDE_CODE_SUBAGENT_MODEL="claude-sonnet-5"
      ;;
    kimi)
      export ANTHROPIC_MODEL="kimi-k2.7-code"
      export ANTHROPIC_DEFAULT_OPUS_MODEL="kimi-k2.7-code"
      export ANTHROPIC_DEFAULT_SONNET_MODEL="kimi-k2.7-code"
      export ANTHROPIC_DEFAULT_HAIKU_MODEL="kimi-k2.7-code"
      export ANTHROPIC_DEFAULT_FABLE_MODEL="kimi-k2.7-code"
      export ANTHROPIC_SMALL_FAST_MODEL="kimi-k2.7-code"
      export CLAUDE_CODE_SUBAGENT_MODEL="kimi-k2.7-code"
      ;;
    mixed)
      export ANTHROPIC_MODEL="claude-sonnet-5"
      export ANTHROPIC_DEFAULT_OPUS_MODEL="gpt-5.5"
      export ANTHROPIC_DEFAULT_SONNET_MODEL="claude-sonnet-5"
      export ANTHROPIC_DEFAULT_HAIKU_MODEL="kimi-k2.7-code"
      export ANTHROPIC_DEFAULT_FABLE_MODEL="kimi-k2.7-code"
      export ANTHROPIC_SMALL_FAST_MODEL="kimi-k2.7-code"
      export CLAUDE_CODE_SUBAGENT_MODEL="gpt-5.5"
      ;;
  esac

  print "Claude profile: $profile"
  print "Base URL: $ANTHROPIC_BASE_URL"
  print "Main: $ANTHROPIC_MODEL"
  print "Opus tier: $ANTHROPIC_DEFAULT_OPUS_MODEL"
  print "Sonnet tier: $ANTHROPIC_DEFAULT_SONNET_MODEL"
  print "Haiku/Fable tier: $ANTHROPIC_DEFAULT_HAIKU_MODEL"
  print "Subagents: $CLAUDE_CODE_SUBAGENT_MODEL"
}
