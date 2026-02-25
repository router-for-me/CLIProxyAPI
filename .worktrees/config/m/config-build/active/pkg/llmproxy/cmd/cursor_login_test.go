package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func TestDoCursorLogin_TokenFileMode_WritesTokenAndConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	tokenPath := filepath.Join(tmp, "cursor-session-token.txt")

	cfg := &config.Config{Port: 8317}
	promptFn := promptFromQueue(t,
		"1",
		"",
		"sk-cursor-token-1",
		tokenPath,
	)

	DoCursorLogin(cfg, &LoginOptions{Prompt: promptFn, ConfigPath: configPath})

	if len(cfg.CursorKey) != 1 {
		t.Fatalf("expected cursor config entry, got %d", len(cfg.CursorKey))
	}

	entry := cfg.CursorKey[0]
	if entry.CursorAPIURL != defaultCursorAPIURL {
		t.Fatalf("CursorAPIURL = %q, want %q", entry.CursorAPIURL, defaultCursorAPIURL)
	}
	if entry.AuthToken != "" {
		t.Fatalf("AuthToken = %q, want empty", entry.AuthToken)
	}
	if entry.TokenFile != tokenPath {
		t.Fatalf("TokenFile = %q, want %q", entry.TokenFile, tokenPath)
	}

	contents, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if got := string(contents); got != "sk-cursor-token-1\n" {
		t.Fatalf("token file content = %q, want %q", got, "sk-cursor-token-1\n")
	}

	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if len(reloaded.CursorKey) != 1 || reloaded.CursorKey[0].TokenFile != tokenPath {
		t.Fatalf("saved cursor config %v", reloaded.CursorKey)
	}
}

func TestDoCursorLogin_ZeroActionMode_ConfiguresAuthToken(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := &config.Config{Port: 8317}
	promptFn := promptFromQueue(t,
		"2",
		"",
		"zero-action-token-1",
	)

	DoCursorLogin(cfg, &LoginOptions{Prompt: promptFn, ConfigPath: configPath})

	entry := cfg.CursorKey[0]
	if entry.TokenFile != "" {
		t.Fatalf("TokenFile = %q, want empty", entry.TokenFile)
	}
	if entry.AuthToken != "zero-action-token-1" {
		t.Fatalf("AuthToken = %q, want %q", entry.AuthToken, "zero-action-token-1")
	}
}

func TestResolveCursorPathForWrite_ExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	got, err := resolveCursorPathForWrite("~/.cursor/session-token.txt")
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	want := filepath.Join(home, ".cursor", "session-token.txt")
	if got != filepath.Clean(want) {
		t.Fatalf("resolved path = %q, want %q", got, want)
	}
}

func TestCursorTokenPathForConfig_HomePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}

	got := cursorTokenPathForConfig(filepath.Join(home, "cursor", "token.txt"))
	if got != "~/cursor/token.txt" {
		t.Fatalf("config path = %q, want %q", got, "~/cursor/token.txt")
	}
}

func promptFromQueue(t *testing.T, values ...string) func(string) (string, error) {
	return func(string) (string, error) {
		if len(values) == 0 {
			return "", errors.New("no prompt values left")
		}
		value := values[0]
		values = values[1:]
		t.Logf("prompt answer used: %q", value)
		return value, nil
	}
}

func TestIsCursorTokenFileMode(t *testing.T) {
	if !isCursorTokenFileMode("1") {
		t.Fatalf("expected mode 1 to be token-file mode")
	}
	if isCursorTokenFileMode("2") {
		t.Fatalf("expected mode 2 to be zero-action mode")
	}
	if isCursorTokenFileMode("zero-action") {
		t.Fatalf("expected zero-action mode token choice to disable token file")
	}
	if !isCursorTokenFileMode("") {
		t.Fatalf("expected empty input to default token-file mode")
	}
}

func TestCursorLoginHelpers_TrimmedMessages(t *testing.T) {
	prompted := make([]string, 0, 2)
	cfg := &config.Config{Port: 8317}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	promptedFn := func(msg string) (string, error) {
		prompted = append(prompted, msg)
		if strings.Contains(msg, "Cursor auth mode") {
			return " 1 ", nil
		}
		if strings.Contains(msg, "Cursor API URL") {
			return "  ", nil
		}
		if strings.Contains(msg, "Cursor token") {
			return "  sk-abc  ", nil
		}
		if strings.Contains(msg, "Token-file path") {
			return "  ", nil
		}
		return "", fmt.Errorf("unexpected prompt: %s", msg)
	}
	DoCursorLogin(cfg, &LoginOptions{Prompt: promptedFn, ConfigPath: configPath})
	if len(prompted) != 4 {
		t.Fatalf("expected 4 prompts, got %d", len(prompted))
	}
	entry := cfg.CursorKey[0]
	if entry.CursorAPIURL != defaultCursorAPIURL {
		t.Fatalf("CursorAPIURL = %q, want default %q", entry.CursorAPIURL, defaultCursorAPIURL)
	}
	if entry.TokenFile != defaultCursorTokenFilePath {
		t.Fatalf("TokenFile = %q, want default %q", entry.TokenFile, defaultCursorTokenFilePath)
	}
}
