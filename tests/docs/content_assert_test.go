package docs_test

import (
	"os"
	"strings"
	"testing"
)

func TestConfigExampleIncludesLocalhostAndEnvGate(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "仅通过 Python Bridge 路由") {
		t.Fatalf("should mention mandatory Python Bridge")
	}
	if !strings.Contains(s, "CLAUDE_AGENT_SDK_ALLOW_REMOTE") {
		t.Fatalf("should mention CLAUDE_AGENT_SDK_ALLOW_REMOTE gate")
	}
}

func TestMigrationDocMentionsRemoteGate(t *testing.T) {
	data, err := os.ReadFile("../../MIGRATION.md")
	if err != nil {
		t.Fatalf("read MIGRATION.md: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "CLAUDE_AGENT_SDK_ALLOW_REMOTE") {
		t.Fatalf("migration should mention remote gate env var")
	}
}
