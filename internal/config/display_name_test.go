package config

import "testing"

func TestStaticProviderDisplayNameRoundTrip(t *testing.T) {
	configBytes := []byte(`gemini-api-key:
  - api-key: gemini-key
    display-name: Gemini account
interactions-api-key:
  - api-key: interactions-key
    display-name: Interactions account
codex-api-key:
  - api-key: codex-key
    display-name: Codex account
    base-url: https://codex.example.com/v1
xai-api-key:
  - api-key: xai-key
    display-name: xAI account
    base-url: https://xai.example.com/v1
claude-api-key:
  - api-key: claude-key
    display-name: Claude account
vertex-api-key:
  - api-key: vertex-key
    display-name: Vertex account
`)

	cfg, err := ParseConfigBytes(configBytes)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if got := cfg.GeminiKey[0].DisplayName; got != "Gemini account" {
		t.Fatalf("Gemini display name = %q, want %q", got, "Gemini account")
	}
	if got := cfg.InteractionsKey[0].DisplayName; got != "Interactions account" {
		t.Fatalf("Interactions display name = %q, want %q", got, "Interactions account")
	}
	if got := cfg.CodexKey[0].DisplayName; got != "Codex account" {
		t.Fatalf("Codex display name = %q, want %q", got, "Codex account")
	}
	if got := cfg.XAIKey[0].DisplayName; got != "xAI account" {
		t.Fatalf("xAI display name = %q, want %q", got, "xAI account")
	}
	if got := cfg.ClaudeKey[0].DisplayName; got != "Claude account" {
		t.Fatalf("Claude display name = %q, want %q", got, "Claude account")
	}
	if got := cfg.VertexCompatAPIKey[0].DisplayName; got != "Vertex account" {
		t.Fatalf("Vertex display name = %q, want %q", got, "Vertex account")
	}
}

func TestStaticProviderDisplayNameIsTrimmed(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`codex-api-key:
  - api-key: codex-key
    display-name: "  Codex account  "
    base-url: https://codex.example.com/v1
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.CodexKey[0].DisplayName; got != "Codex account" {
		t.Fatalf("trimmed display name = %q, want %q", got, "Codex account")
	}
}
