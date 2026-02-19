package diff

import (
	"testing"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestBuildConfigChangeDetails(t *testing.T) {
	oldCfg := &config.Config{
		Port: 8080,
		Debug: false,
		ClaudeKey: []config.ClaudeKey{{APIKey: "k1"}},
	}
	newCfg := &config.Config{
		Port: 9090,
		Debug: true,
		ClaudeKey: []config.ClaudeKey{{APIKey: "k1"}, {APIKey: "k2"}},
	}
	
	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	if len(changes) != 3 {
		t.Errorf("expected 3 changes, got %d: %v", len(changes), changes)
	}
	
	// Test unknown proxy URL
	u := formatProxyURL("http://user:pass@host:1234")
	if u != "http://host:1234" {
		t.Errorf("expected redacted user:pass, got %s", u)
	}
}

func TestEqualStringMap(t *testing.T) {
	m1 := map[string]string{"a": "1"}
	m2 := map[string]string{"a": "1"}
	m3 := map[string]string{"a": "2"}
	if !equalStringMap(m1, m2) {
		t.Error("expected true for m1, m2")
	}
	if equalStringMap(m1, m3) {
		t.Error("expected false for m1, m3")
	}
}

func TestEqualStringSet(t *testing.T) {
	s1 := []string{"a", "b"}
	s2 := []string{"b", "a"}
	s3 := []string{"a"}
	if !equalStringSet(s1, s2) {
		t.Error("expected true for s1, s2")
	}
	if equalStringSet(s1, s3) {
		t.Error("expected false for s1, s3")
	}
}
