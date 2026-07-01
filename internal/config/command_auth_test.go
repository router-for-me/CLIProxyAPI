package config

import (
	"strings"
	"testing"
)

func TestParseConfigBytes_CommandAuthAliasesAndDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
gemini-api-key:
  - auth:
      command: fetch-gemini-token
claude-api-key:
  - auth:
      command: fetch-claude-token
codex-api-key:
  - base_url: https://proxy.example.com/v1
    auth:
      command: /usr/local/bin/fetch-token
      args: ["--audience", " codex "]
    models:
      - name: gpt-5-codex
        alias: gpt-5-codex
openai-compatibility:
  - name: proxy
    base_url: https://proxy.example.com/v1
    proxy_url: http://proxy.local
    auth:
      command: fetch-openai-token
      timeout_ms: 7000
      refresh_interval_ms: 9000
    models:
      - name: gpt-5
        alias: gpt-5
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	if len(cfg.GeminiKey) != 1 || cfg.GeminiKey[0].Auth == nil || cfg.GeminiKey[0].Auth.Command != "fetch-gemini-token" {
		t.Fatalf("gemini command auth = %#v", cfg.GeminiKey)
	}
	if len(cfg.ClaudeKey) != 1 || cfg.ClaudeKey[0].Auth == nil || cfg.ClaudeKey[0].Auth.Command != "fetch-claude-token" {
		t.Fatalf("claude command auth = %#v", cfg.ClaudeKey)
	}
	if len(cfg.CodexKey) != 1 {
		t.Fatalf("codex key count = %d, want 1", len(cfg.CodexKey))
	}
	codex := cfg.CodexKey[0]
	if codex.BaseURL != "https://proxy.example.com/v1" {
		t.Fatalf("codex base-url = %q", codex.BaseURL)
	}
	if codex.Auth == nil {
		t.Fatal("codex auth is nil")
	}
	if codex.Auth.TimeoutMS != DefaultCommandAuthTimeoutMS {
		t.Fatalf("codex timeout-ms = %d, want default %d", codex.Auth.TimeoutMS, DefaultCommandAuthTimeoutMS)
	}
	if codex.Auth.RefreshIntervalMS != DefaultCommandAuthRefreshIntervalMS {
		t.Fatalf("codex refresh-interval-ms = %d, want default %d", codex.Auth.RefreshIntervalMS, DefaultCommandAuthRefreshIntervalMS)
	}
	if len(codex.Auth.Args) != 2 || codex.Auth.Args[1] != " codex " {
		t.Fatalf("codex auth args = %#v, want second arg with surrounding spaces preserved", codex.Auth.Args)
	}

	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("openai compat count = %d, want 1", len(cfg.OpenAICompatibility))
	}
	compat := cfg.OpenAICompatibility[0]
	if compat.BaseURL != "https://proxy.example.com/v1" {
		t.Fatalf("compat base-url = %q", compat.BaseURL)
	}
	if compat.ProxyURL != "http://proxy.local" {
		t.Fatalf("compat proxy-url = %q", compat.ProxyURL)
	}
	if compat.Auth == nil {
		t.Fatal("compat auth is nil")
	}
	if compat.Auth.TimeoutMS != 7000 || compat.Auth.RefreshIntervalMS != 9000 {
		t.Fatalf("compat auth timings = %d/%d, want 7000/9000", compat.Auth.TimeoutMS, compat.Auth.RefreshIntervalMS)
	}
}

func TestParseConfigBytes_CommandAuthAidenExample(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
openai-compatibility:
  - name: aiden
    base_url: https://aiden-aiproxy.bytedance.net/v2
    auth:
      command: aiden
      args: ["auth", "get-sso-token"]
    models:
      - name: gpt-5.4
        alias: aiden-live-test
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("openai compat count = %d, want 1", len(cfg.OpenAICompatibility))
	}
	compat := cfg.OpenAICompatibility[0]
	if compat.BaseURL != "https://aiden-aiproxy.bytedance.net/v2" {
		t.Fatalf("base-url = %q", compat.BaseURL)
	}
	if compat.Auth == nil {
		t.Fatal("auth is nil")
	}
	if compat.Auth.Command != "aiden" {
		t.Fatalf("auth command = %q", compat.Auth.Command)
	}
	if len(compat.Auth.Args) != 2 || compat.Auth.Args[0] != "auth" || compat.Auth.Args[1] != "get-sso-token" {
		t.Fatalf("auth args = %#v", compat.Auth.Args)
	}
}

func TestCommandAuthIdentityCachedAfterParse(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
codex-api-key:
  - base-url: https://proxy.example.com/v1
    auth:
      command: fetch-token
      args: ["--audience", "codex"]
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	auth := cfg.CodexKey[0].Auth
	if auth == nil {
		t.Fatal("auth is nil")
	}
	identity := CommandAuthIdentity(auth)
	if identity == "" {
		t.Fatal("identity is empty")
	}
	auth.Args = []string{"mutated"}
	if got := CommandAuthIdentity(auth); got != identity {
		t.Fatalf("cached identity changed to %q, want %q", got, identity)
	}
}

func TestParseConfigBytes_CommandAuthRejectsStaticKeyConflict(t *testing.T) {
	_, err := ParseConfigBytes([]byte(`
codex-api-key:
  - api-key: static-key
    base-url: https://proxy.example.com/v1
    auth:
      command: fetch-token
`))
	if err == nil || !strings.Contains(err.Error(), "cannot set both api-key and auth") {
		t.Fatalf("error = %v, want api-key/auth conflict", err)
	}

	_, err = ParseConfigBytes([]byte(`
gemini-api-key:
  - api-key: static-key
    auth:
      command: fetch-token
`))
	if err == nil || !strings.Contains(err.Error(), "cannot set both api-key and auth") {
		t.Fatalf("error = %v, want gemini api-key/auth conflict", err)
	}

	_, err = ParseConfigBytes([]byte(`
claude-api-key:
  - api-key: static-key
    auth:
      command: fetch-token
`))
	if err == nil || !strings.Contains(err.Error(), "cannot set both api-key and auth") {
		t.Fatalf("error = %v, want claude api-key/auth conflict", err)
	}

	_, err = ParseConfigBytes([]byte(`
vertex-api-key:
  - api-key: static-key
    auth:
      command: fetch-token
`))
	if err == nil || !strings.Contains(err.Error(), "cannot set both api-key and auth") {
		t.Fatalf("error = %v, want vertex api-key/auth conflict", err)
	}

	_, err = ParseConfigBytes([]byte(`
openai-compatibility:
  - name: proxy
    base-url: https://proxy.example.com/v1
    auth:
      command: fetch-token
    api-key-entries:
      - api-key: static-key
`))
	if err == nil || !strings.Contains(err.Error(), "cannot set both api-key-entries") {
		t.Fatalf("error = %v, want api-key-entries/auth conflict", err)
	}
}
