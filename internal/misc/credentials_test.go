package misc

import (
	"testing"
)

func TestMergeMetadata(t *testing.T) {
	source := map[string]any{
		"type":         "codex",
		"access_token": "token-123",
	}
	metadata := map[string]any{
		"disabled":   false,
		"email":      "test@example.com",
		"prefix":     "custom-prefix",
		"websockets": false,
		"note":       "custom note",
	}

	result, err := MergeMetadata(source, metadata)
	if err != nil {
		t.Fatalf("MergeMetadata() error = %v", err)
	}

	if result["type"] != "codex" {
		t.Errorf("type = %v, want codex", result["type"])
	}
	if result["access_token"] != "token-123" {
		t.Errorf("access_token = %v, want token-123", result["access_token"])
	}
	if result["disabled"] != false {
		t.Errorf("disabled = %v, want false", result["disabled"])
	}
	if result["email"] != "test@example.com" {
		t.Errorf("email = %v, want test@example.com", result["email"])
	}
	if result["prefix"] != "custom-prefix" {
		t.Errorf("prefix = %v, want custom-prefix", result["prefix"])
	}
	if result["websockets"] != false {
		t.Errorf("websockets = %v, want false", result["websockets"])
	}
	if result["note"] != "custom note" {
		t.Errorf("note = %v, want custom note", result["note"])
	}
}
