package config

import "testing"

func TestParseConfigBytes_WebSearchForward(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
websearch-forward:
  enable: true
  model: "deepseek"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.WebSearchForward.Enable {
		t.Fatal("WebSearchForward.Enable = false, want true")
	}
	if cfg.WebSearchForward.Model != "deepseek" {
		t.Fatalf("WebSearchForward.Model = %q, want %q", cfg.WebSearchForward.Model, "deepseek")
	}
}

func TestParseConfigBytes_WebSearchForwardDefault(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`port: 8317`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.WebSearchForward.Enable {
		t.Fatal("WebSearchForward.Enable = true, want false by default")
	}
	if cfg.WebSearchForward.Model != "" {
		t.Fatalf("WebSearchForward.Model = %q, want empty by default", cfg.WebSearchForward.Model)
	}
}
