package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAPIKeyModelAccessRejectsNormalizedKeyCollision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		access  map[string]APIKeyModelAccess
		wantErr bool
	}{
		{"nil", nil, false},
		{"single", map[string]APIKeyModelAccess{"client": {AllowedModels: []string{"claude-*"}}}, false},
		{"distinct", map[string]APIKeyModelAccess{"a": {}, "b": {}}, false},
		{"collision", map[string]APIKeyModelAccess{
			"client":   {AllowedModels: []string{"claude-*"}},
			" client ": {DeniedModels: []string{"claude-*"}},
		}, true},
		{"collision with tab", map[string]APIKeyModelAccess{
			"client":   {},
			"\tclient": {},
		}, true},
		{"blank keys ignored", map[string]APIKeyModelAccess{"": {}, "   ": {}, "a": {}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errValidate := ValidateAPIKeyModelAccess(tc.access)
			if tc.wantErr {
				if errValidate == nil {
					t.Fatal("expected a collision error")
				}
				if !strings.Contains(errValidate.Error(), "api-key-model-access") {
					t.Fatalf("error lacks configuration context: %v", errValidate)
				}
				return
			}
			if errValidate != nil {
				t.Fatalf("unexpected error: %v", errValidate)
			}
		})
	}
}

func TestValidateAPIKeyModelAccessErrorIsDeterministic(t *testing.T) {
	access := map[string]APIKeyModelAccess{
		"client":   {},
		" client ": {},
	}
	first := ValidateAPIKeyModelAccess(access)
	if first == nil {
		t.Fatal("expected a collision error")
	}
	for i := 0; i < 50; i++ {
		next := ValidateAPIKeyModelAccess(access)
		if next == nil || next.Error() != first.Error() {
			t.Fatalf("error message depends on map iteration order: %q vs %q", first, next)
		}
	}
}

func TestLoadConfigRejectsCollidingAPIKeyModelAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "api-keys:\n  - client\napi-key-model-access:\n  client:\n    allowed-models: [claude-*]\n  ' client ':\n    denied-models: [claude-*]\n"
	if errWrite := os.WriteFile(path, []byte(data), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	if _, errLoad := LoadConfig(path); errLoad == nil {
		t.Fatal("expected the colliding api-key-model-access entries to be rejected")
	}
}
