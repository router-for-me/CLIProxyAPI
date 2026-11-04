package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGitHubTokenConfigRoundTrip(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("server:\n  github-token: test-github-secret\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubToken != "test-github-secret" {
		t.Fatal("server.github-token was not parsed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, []byte("server:\n  github-token: old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]interface{}
	if err = yaml.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	server, ok := tree["server"].(map[string]interface{})
	if !ok || server["github-token"] != "test-github-secret" {
		t.Fatal("canonical YAML path was not preserved")
	}
	reloaded, err := ParseConfigBytes(data)
	if err != nil || reloaded.GitHubToken != cfg.GitHubToken {
		t.Fatal("token did not round trip")
	}
	clone := cfg.CloneForRuntime()
	if clone.GitHubToken != cfg.GitHubToken {
		t.Fatal("clone lost token")
	}
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "test-github-secret") {
		t.Fatal("token leaked in runtime JSON")
	}
}
