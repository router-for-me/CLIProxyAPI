package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallClaudeCodeAliasesPowerShellIsIdempotent(t *testing.T) {
	tempDir := t.TempDir()
	profile := filepath.Join(tempDir, "profile.ps1")
	executable := fakeClaudeExecutable(t, tempDir)
	initial := "# keep this user setting\n$env:EXISTING = 'yes'\n"
	if err := os.WriteFile(profile, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	options := ClaudeCodeAliasOptions{
		Shell:            "powershell",
		ProfilePath:      profile,
		ClaudeExecutable: executable,
		BaseURL:          "http://127.0.0.1:8317",
		APIKey:           "local-proxy-test-key",
	}
	first, err := InstallClaudeCodeAliases(options)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !first.Changed || first.DryRun {
		t.Fatalf("first result = %#v", first)
	}
	installed := readTestFile(t, profile)
	for _, want := range []string{
		initial,
		"function global:claude-codex",
		"ANTHROPIC_DEFAULT_OPUS_MODEL = 'gpt-5.6-sol'",
		"ANTHROPIC_DEFAULT_SONNET_MODEL = 'gpt-5.6-terra'",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL = 'gpt-5.6-luna'",
		"CLAUDE_CODE_ALWAYS_ENABLE_EFFORT = '1'",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY = '1'",
		"@('--model', 'opus')",
		"@('--effort', 'xhigh')",
		"local-proxy-test-key",
	} {
		if !strings.Contains(installed, want) {
			t.Errorf("installed profile missing %q", want)
		}
	}
	if strings.Contains(installed, "function global:claude {") {
		t.Fatal("installer must leave the native claude command untouched")
	}
	if got := strings.Count(installed, claudeCodeAliasStartMarker); got != 1 {
		t.Fatalf("start marker count = %d, want 1", got)
	}

	second, err := InstallClaudeCodeAliases(options)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second.Changed {
		t.Fatalf("second install unexpectedly changed profile: %#v", second)
	}
	if got := readTestFile(t, profile); got != installed {
		t.Fatal("idempotent install changed profile contents")
	}
}

func TestInstallClaudeCodeAliasesReplacesManagedBlock(t *testing.T) {
	tempDir := t.TempDir()
	profile := filepath.Join(tempDir, ".bashrc")
	executable := fakeClaudeExecutable(t, tempDir)
	oldBlock, err := renderClaudeCodeAlias("bash", executable, "http://127.0.0.1:8317", "old-local-key")
	if err != nil {
		t.Fatal(err)
	}
	initial := "export KEEP_ME=1\n\n" + oldBlock + "\n\nalias after=true\n"
	if err = os.WriteFile(profile, []byte(initial), 0o640); err != nil {
		t.Fatal(err)
	}

	result, err := InstallClaudeCodeAliases(ClaudeCodeAliasOptions{
		Shell:            "bash",
		ProfilePath:      profile,
		ClaudeExecutable: executable,
		BaseURL:          "http://127.0.0.1:9417",
		APIKey:           "new-local-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("expected managed block update")
	}
	installed := readTestFile(t, profile)
	for _, want := range []string{"export KEEP_ME=1", "alias after=true", "http://127.0.0.1:9417", "new-local-key"} {
		if !strings.Contains(installed, want) {
			t.Errorf("updated profile missing %q", want)
		}
	}
	for _, unwanted := range []string{"old-local-key", "http://127.0.0.1:8317"} {
		if strings.Contains(installed, unwanted) {
			t.Errorf("updated profile retained %q", unwanted)
		}
	}
	if got := strings.Count(installed, claudeCodeAliasStartMarker); got != 1 {
		t.Fatalf("start marker count = %d, want 1", got)
	}
	if info, statErr := os.Stat(profile); statErr != nil {
		t.Fatal(statErr)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("profile mode = %o, want 640", info.Mode().Perm())
	}
}

func TestInstallClaudeCodeAliasesDryRunDoesNotWrite(t *testing.T) {
	tempDir := t.TempDir()
	profile := filepath.Join(tempDir, "config.fish")
	executable := fakeClaudeExecutable(t, tempDir)

	result, err := InstallClaudeCodeAliases(ClaudeCodeAliasOptions{
		Shell:            "fish",
		ProfilePath:      profile,
		ClaudeExecutable: executable,
		BaseURL:          "http://127.0.0.1:8317",
		APIKey:           "dry-run-local-key",
		DryRun:           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.DryRun {
		t.Fatalf("result = %#v", result)
	}
	if _, statErr := os.Stat(profile); !os.IsNotExist(statErr) {
		t.Fatalf("dry run created profile: %v", statErr)
	}
	if strings.Contains(result.ProfilePath, "dry-run-local-key") {
		t.Fatal("result exposed the local proxy key")
	}
}

func TestRenderClaudeCodeAliasesForSupportedShells(t *testing.T) {
	for _, shell := range []string{"powershell", "bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			block, err := renderClaudeCodeAlias(shell, filepath.Join("tmp", "Claude Code", "claude"), "http://127.0.0.1:8317", "local-key")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				claudeCodeAliasStartMarker,
				claudeCodeAliasEndMarker,
				"claude-codex",
				"gpt-5.6-sol",
				"gpt-5.6-terra",
				"gpt-5.6-luna",
				"CLAUDE_CODE_ALWAYS_ENABLE_EFFORT",
				"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
				"local-key",
			} {
				if !strings.Contains(block, want) {
					t.Errorf("rendered block missing %q", want)
				}
			}
		})
	}
}

func TestMergeClaudeCodeAliasBlockRejectsIncompleteBlock(t *testing.T) {
	_, err := mergeClaudeCodeAliasBlock("before\n"+claudeCodeAliasStartMarker+"\nbroken\n", "replacement")
	if err == nil {
		t.Fatal("expected incomplete marker error")
	}
}

func TestResolveAliasShell(t *testing.T) {
	for input, want := range map[string]string{
		"powershell.exe": "powershell",
		"pwsh":           "powershell",
		"bash":           "bash",
		"zsh":            "zsh",
		"fish":           "fish",
	} {
		got, err := resolveAliasShell(input)
		if err != nil {
			t.Errorf("resolveAliasShell(%q): %v", input, err)
		} else if got != want {
			t.Errorf("resolveAliasShell(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := resolveAliasShell("cmd"); err == nil {
		t.Fatal("expected unsupported shell error")
	}
}

func fakeClaudeExecutable(t *testing.T, dir string) string {
	t.Helper()
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
