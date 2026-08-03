# Install the repository profile and refresh the current zsh environment.
#
# Source this file after changing claude-profile.zsh:
#   source examples/claude-code-profiles/update-claude-profile.zsh
# Running it as a child process can install the file, but cannot change the
# exports in the parent shell.

_claude_profile_update_script="${(%):-%N}"
if [[ "$_claude_profile_update_script" != /* ]]; then
  _claude_profile_update_script="${0:A}"
fi
typeset -g CLAUDE_CODE_PROFILE_SOURCE="${_claude_profile_update_script:A:h}/claude-profile.zsh"
unset _claude_profile_update_script

claude-profile-update() {
  emulate -L zsh

  local source_file="${CLAUDE_CODE_PROFILE_SOURCE:-}"
  local install_dir="${CLAUDE_CODE_PROFILE_INSTALL_DIR:-$HOME/.config/claude-profiles}"
  local install_file="$install_dir/claude-profile.zsh"
  local requested_profile="${1:-}"
  local active_profile
  local source_dir
  local repository_config

  case "$requested_profile" in
    openai|claude|kimi|mixed)
      active_profile="$requested_profile"
      ;;
    "")
      active_profile="${CLAUDE_CODE_ACTIVE_PROFILE:-}"
      ;;
    *)
      print -u2 "Usage: source update-claude-profile.zsh [openai|claude|kimi|mixed]"
      return 2
      ;;
  esac

  if [[ -z "$source_file" || ! -r "$source_file" ]]; then
    print -u2 "Could not read the repository profile: ${source_file:-<unset>}"
    return 1
  fi

  source_dir="${source_file:A:h}"
  repository_config="${source_dir:h:h}/config.yaml"
  if [[ -z "${CLAUDE_CODE_ROUTER_TOKEN:-}" &&
        -z "${CLAUDE_CODE_ROUTER_CONFIG:-}" &&
        -r "$repository_config" ]]; then
    export CLAUDE_CODE_ROUTER_CONFIG="$repository_config"
  fi

  if ! command mkdir -p "$install_dir"; then
    print -u2 "Could not create the profile directory: $install_dir"
    return 1
  fi

  if ! command cp "$source_file" "$install_file"; then
    print -u2 "Could not install the profile: $install_file"
    return 1
  fi

  source "$install_file" || return 1

  if [[ -n "$active_profile" ]]; then
    claude-profile "$active_profile" || return 1
  fi

  print "Installed Claude profile: $install_file"
  if [[ "${ZSH_EVAL_CONTEXT:-}" == *:file* ]]; then
    print "The current shell has been refreshed."
  else
    print "The file was updated, but this child process cannot change the parent shell."
    print "Run: source $install_file"
  fi
}

claude-profile-update "$@"
