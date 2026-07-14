package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	claudeCodeAliasStartMarker = "# >>> CLIProxyAPI Claude Code >>>"
	claudeCodeAliasEndMarker   = "# <<< CLIProxyAPI Claude Code <<<"
)

// ClaudeCodeAliasOptions controls installation of the claude-codex shell function.
// APIKey is the local proxy client key, not an upstream provider credential.
type ClaudeCodeAliasOptions struct {
	Shell            string
	ProfilePath      string
	ClaudeExecutable string
	BaseURL          string
	APIKey           string
	DryRun           bool
}

// ClaudeCodeAliasResult describes an alias installation without exposing its key.
type ClaudeCodeAliasResult struct {
	Shell       string
	ProfilePath string
	Changed     bool
	DryRun      bool
}

// InstallClaudeCodeAliases installs an idempotent claude-codex function while
// deliberately leaving the user's native claude command untouched.
func InstallClaudeCodeAliases(options ClaudeCodeAliasOptions) (ClaudeCodeAliasResult, error) {
	shell, err := resolveAliasShell(options.Shell)
	if err != nil {
		return ClaudeCodeAliasResult{}, err
	}

	profilePath := strings.TrimSpace(options.ProfilePath)
	if profilePath == "" {
		profilePath, err = defaultAliasProfile(shell)
		if err != nil {
			return ClaudeCodeAliasResult{}, err
		}
	}
	profilePath, err = filepath.Abs(profilePath)
	if err != nil {
		return ClaudeCodeAliasResult{}, fmt.Errorf("resolve shell profile path: %w", err)
	}

	claudeExecutable, err := resolveClaudeExecutable(options.ClaudeExecutable)
	if err != nil {
		return ClaudeCodeAliasResult{}, err
	}
	if strings.TrimSpace(options.BaseURL) == "" {
		return ClaudeCodeAliasResult{}, errors.New("Claude Code alias base URL is required")
	}
	if strings.TrimSpace(options.APIKey) == "" {
		return ClaudeCodeAliasResult{}, errors.New("config must contain at least one non-empty api-keys entry")
	}

	block, err := renderClaudeCodeAlias(shell, claudeExecutable, strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"), strings.TrimSpace(options.APIKey))
	if err != nil {
		return ClaudeCodeAliasResult{}, err
	}
	existing, mode, err := readAliasProfile(profilePath)
	if err != nil {
		return ClaudeCodeAliasResult{}, err
	}
	updated, err := mergeClaudeCodeAliasBlock(existing, block)
	if err != nil {
		return ClaudeCodeAliasResult{}, fmt.Errorf("update shell profile: %w", err)
	}

	result := ClaudeCodeAliasResult{
		Shell:       shell,
		ProfilePath: profilePath,
		Changed:     updated != existing,
		DryRun:      options.DryRun,
	}
	if options.DryRun || !result.Changed {
		return result, nil
	}
	if err = writeAliasProfile(profilePath, []byte(updated), mode); err != nil {
		return ClaudeCodeAliasResult{}, err
	}
	return result, nil
}

func resolveAliasShell(requested string) (string, error) {
	shell := strings.ToLower(strings.TrimSpace(requested))
	if shell == "" || shell == "auto" {
		if runtime.GOOS == "windows" {
			return "powershell", nil
		}
		shell = strings.ToLower(filepath.Base(strings.TrimSpace(os.Getenv("SHELL"))))
		if shell == "" {
			shell = "bash"
		}
	}
	switch shell {
	case "pwsh", "powershell", "powershell.exe", "pwsh.exe":
		return "powershell", nil
	case "bash", "zsh", "fish":
		return shell, nil
	default:
		return "", fmt.Errorf("unsupported shell %q (supported: auto, powershell, bash, zsh, fish)", requested)
	}
}

func defaultAliasProfile(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	switch shell {
	case "powershell":
		for _, executable := range []string{"pwsh", "powershell"} {
			path, lookErr := exec.LookPath(executable)
			if lookErr != nil {
				continue
			}
			output, commandErr := exec.Command(path, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$PROFILE.CurrentUserAllHosts").Output()
			if commandErr == nil && strings.TrimSpace(string(output)) != "" {
				return strings.TrimSpace(string(output)), nil
			}
		}
		folder := "PowerShell"
		if runtime.GOOS == "windows" {
			if _, errLook := exec.LookPath("pwsh"); errLook != nil {
				folder = "WindowsPowerShell"
			}
		}
		return filepath.Join(home, "Documents", folder, "profile.ps1"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "zsh":
		return filepath.Join(home, ".zshrc"), nil
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish"), nil
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

func resolveClaudeExecutable(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "claude"
	}
	path, err := exec.LookPath(requested)
	if err != nil {
		return "", fmt.Errorf("find Claude Code executable %q: %w (install Claude Code or use --claude-executable)", requested, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Claude Code executable: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect Claude Code executable: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("Claude Code executable %q is a directory", path)
	}
	return path, nil
}

func readAliasProfile(path string) (string, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0o600, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("read shell profile: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("inspect shell profile: %w", err)
	}
	return string(data), info.Mode().Perm(), nil
}

func mergeClaudeCodeAliasBlock(existing, block string) (string, error) {
	start := strings.Index(existing, claudeCodeAliasStartMarker)
	end := strings.Index(existing, claudeCodeAliasEndMarker)
	if (start >= 0) != (end >= 0) || (start >= 0 && end < start) {
		return "", errors.New("found an incomplete CLIProxyAPI alias block; repair or remove it before retrying")
	}
	if strings.Count(existing, claudeCodeAliasStartMarker) > 1 || strings.Count(existing, claudeCodeAliasEndMarker) > 1 {
		return "", errors.New("found multiple CLIProxyAPI alias blocks; remove duplicates before retrying")
	}

	newline := "\n"
	if strings.Contains(existing, "\r\n") {
		newline = "\r\n"
	}
	block = strings.ReplaceAll(strings.TrimSpace(block), "\n", newline)
	if start >= 0 {
		end += len(claudeCodeAliasEndMarker)
		return existing[:start] + block + existing[end:], nil
	}
	if strings.TrimSpace(existing) == "" {
		return block + newline, nil
	}
	return strings.TrimRight(existing, "\r\n") + newline + newline + block + newline, nil
}

func writeAliasProfile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create shell profile directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".cliproxyapi-profile-*")
	if err != nil {
		return fmt.Errorf("create temporary shell profile: %w", err)
	}
	tempPath := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		_ = os.Remove(tempPath)
	}()
	if mode == 0 {
		mode = 0o600
	}
	if err = temp.Chmod(mode); err != nil {
		return fmt.Errorf("secure temporary shell profile: %w", err)
	}
	if _, err = temp.Write(data); err != nil {
		return fmt.Errorf("write temporary shell profile: %w", err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary shell profile: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close temporary shell profile: %w", err)
	}
	closed = true
	if err = os.Rename(tempPath, path); err == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("replace shell profile: %w", err)
	}
	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("replace shell profile: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace shell profile: %w", err)
	}
	return nil
}

func renderClaudeCodeAlias(shell, executable, baseURL, apiKey string) (string, error) {
	switch shell {
	case "powershell":
		return renderPowerShellClaudeCodeAlias(executable, baseURL, apiKey), nil
	case "bash", "zsh":
		return renderPOSIXClaudeCodeAlias(executable, baseURL, apiKey), nil
	case "fish":
		return renderFishClaudeCodeAlias(executable, baseURL, apiKey), nil
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

func renderPowerShellClaudeCodeAlias(executable, baseURL, apiKey string) string {
	return fmt.Sprintf(`%s
$script:CLIProxyAPIClaudeExecutable = %s
$script:CLIProxyAPIClaudeBaseURL = %s
$script:CLIProxyAPIClaudeKey = %s

function global:claude-codex {
    $claudeArgs = @($args)
    $hasModel = $false
    $hasEffort = $false
    foreach ($arg in $claudeArgs) {
        $value = [string]$arg
        if ($value -eq '--model' -or $value.StartsWith('--model=')) { $hasModel = $true }
        if ($value -eq '--effort' -or $value.StartsWith('--effort=')) { $hasEffort = $true }
    }
    if (-not $hasEffort) { $claudeArgs = @('--effort', 'xhigh') + $claudeArgs }
    if (-not $hasModel) { $claudeArgs = @('--model', 'opus') + $claudeArgs }

    $managed = @(
        'ANTHROPIC_API_KEY', 'ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_BASE_URL',
        'ANTHROPIC_DEFAULT_OPUS_MODEL', 'ANTHROPIC_DEFAULT_SONNET_MODEL', 'ANTHROPIC_DEFAULT_HAIKU_MODEL',
        'CLAUDE_CODE_ALWAYS_ENABLE_EFFORT', 'CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY',
        'CLAUDE_CODE_MAX_CONTEXT_TOKENS', 'CLAUDE_AUTOCOMPACT_PCT_OVERRIDE', 'DISABLE_AUTO_COMPACT'
    )
    $previous = @{}
    $present = @{}
	$code = 1
    foreach ($name in $managed) {
        $present[$name] = Test-Path "Env:$name"
        if ($present[$name]) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
    }

    try {
        Remove-Item Env:\ANTHROPIC_API_KEY -ErrorAction SilentlyContinue
        Remove-Item Env:\CLAUDE_CODE_MAX_CONTEXT_TOKENS -ErrorAction SilentlyContinue
        Remove-Item Env:\CLAUDE_AUTOCOMPACT_PCT_OVERRIDE -ErrorAction SilentlyContinue
        Remove-Item Env:\DISABLE_AUTO_COMPACT -ErrorAction SilentlyContinue
        $env:ANTHROPIC_AUTH_TOKEN = $script:CLIProxyAPIClaudeKey
        $env:ANTHROPIC_BASE_URL = $script:CLIProxyAPIClaudeBaseURL
        $env:ANTHROPIC_DEFAULT_OPUS_MODEL = 'gpt-5.6-sol'
        $env:ANTHROPIC_DEFAULT_SONNET_MODEL = 'gpt-5.6-terra'
        $env:ANTHROPIC_DEFAULT_HAIKU_MODEL = 'gpt-5.6-luna'
        $env:CLAUDE_CODE_ALWAYS_ENABLE_EFFORT = '1'
        $env:CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY = '1'
        & $script:CLIProxyAPIClaudeExecutable @claudeArgs
        $code = $LASTEXITCODE
    }
    finally {
        foreach ($name in $managed) {
            if ($present[$name]) { Set-Item "Env:$name" $previous[$name] }
            else { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
        }
    }
    $global:LASTEXITCODE = $code
}
%s`, claudeCodeAliasStartMarker, quotePowerShell(executable), quotePowerShell(baseURL), quotePowerShell(apiKey), claudeCodeAliasEndMarker)
}

func renderPOSIXClaudeCodeAlias(executable, baseURL, apiKey string) string {
	return fmt.Sprintf(`%s
_cliproxyapi_claude_executable=%s
_cliproxyapi_claude_base_url=%s
_cliproxyapi_claude_key=%s

claude-codex() {
    local has_model=0 has_effort=0 arg
    local -a cliproxyapi_args=("$@")
    for arg in "$@"; do
        case "$arg" in
            --model|--model=*) has_model=1 ;;
            --effort|--effort=*) has_effort=1 ;;
        esac
    done
    if [ "$has_effort" -eq 0 ]; then cliproxyapi_args=(--effort xhigh "${cliproxyapi_args[@]}"); fi
    if [ "$has_model" -eq 0 ]; then cliproxyapi_args=(--model opus "${cliproxyapi_args[@]}"); fi

    env -u ANTHROPIC_API_KEY \
        -u CLAUDE_CODE_MAX_CONTEXT_TOKENS \
        -u CLAUDE_AUTOCOMPACT_PCT_OVERRIDE \
        -u DISABLE_AUTO_COMPACT \
        ANTHROPIC_AUTH_TOKEN="$_cliproxyapi_claude_key" \
        ANTHROPIC_BASE_URL="$_cliproxyapi_claude_base_url" \
        ANTHROPIC_DEFAULT_OPUS_MODEL=gpt-5.6-sol \
        ANTHROPIC_DEFAULT_SONNET_MODEL=gpt-5.6-terra \
        ANTHROPIC_DEFAULT_HAIKU_MODEL=gpt-5.6-luna \
        CLAUDE_CODE_ALWAYS_ENABLE_EFFORT=1 \
        CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1 \
        "$_cliproxyapi_claude_executable" "${cliproxyapi_args[@]}"
}
%s`, claudeCodeAliasStartMarker, quotePOSIX(executable), quotePOSIX(baseURL), quotePOSIX(apiKey), claudeCodeAliasEndMarker)
}

func renderFishClaudeCodeAlias(executable, baseURL, apiKey string) string {
	return fmt.Sprintf(`%s
set -g _cliproxyapi_claude_executable %s
set -g _cliproxyapi_claude_base_url %s
set -g _cliproxyapi_claude_key %s

function claude-codex
    set -l cliproxyapi_args $argv
    set -l has_model 0
    set -l has_effort 0
    for arg in $argv
        if test "$arg" = --model; or string match -q -- '--model=*' "$arg"
            set has_model 1
        end
        if test "$arg" = --effort; or string match -q -- '--effort=*' "$arg"
            set has_effort 1
        end
    end
    if test $has_effort -eq 0
        set -p cliproxyapi_args --effort xhigh
    end
    if test $has_model -eq 0
        set -p cliproxyapi_args --model opus
    end

    env -u ANTHROPIC_API_KEY \
        -u CLAUDE_CODE_MAX_CONTEXT_TOKENS \
        -u CLAUDE_AUTOCOMPACT_PCT_OVERRIDE \
        -u DISABLE_AUTO_COMPACT \
        ANTHROPIC_AUTH_TOKEN="$_cliproxyapi_claude_key" \
        ANTHROPIC_BASE_URL="$_cliproxyapi_claude_base_url" \
        ANTHROPIC_DEFAULT_OPUS_MODEL=gpt-5.6-sol \
        ANTHROPIC_DEFAULT_SONNET_MODEL=gpt-5.6-terra \
        ANTHROPIC_DEFAULT_HAIKU_MODEL=gpt-5.6-luna \
        CLAUDE_CODE_ALWAYS_ENABLE_EFFORT=1 \
        CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1 \
        "$_cliproxyapi_claude_executable" $cliproxyapi_args
end
%s`, claudeCodeAliasStartMarker, quoteFish(executable), quoteFish(baseURL), quoteFish(apiKey), claudeCodeAliasEndMarker)
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func quoteFish(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "'", "\\'")
	return "'" + value + "'"
}
