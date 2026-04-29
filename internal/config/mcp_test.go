package config

import "testing"

func TestSanitizeMCP(t *testing.T) {
	cfg := &Config{
		MCP: MCPConfig{
			UpstreamURL:    "  https://mcp.example.com  ",
			UpstreamAPIKey: "  test-key  ",
		},
	}

	cfg.SanitizeMCP()

	if cfg.MCP.UpstreamURL != "https://mcp.example.com" {
		t.Fatalf("unexpected upstream-url: %q", cfg.MCP.UpstreamURL)
	}
	if cfg.MCP.UpstreamAPIKey != "test-key" {
		t.Fatalf("unexpected upstream-api-key: %q", cfg.MCP.UpstreamAPIKey)
	}
}
