package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAICompatibilityWireAPIValidation(t *testing.T) {
	for _, wire := range []string{"", "chat-completions", "responses", " RESPONSES ", " CHAT-COMPLETIONS ", "response", "respones", "chat_completions"} {
		valid := wire == "" || strings.EqualFold(strings.TrimSpace(wire), "responses") || strings.EqualFold(strings.TrimSpace(wire), "chat-completions")
		for _, mode := range []string{"parse", "load", "optional load"} {
			t.Run(mode+"/"+wire, func(t *testing.T) {
				data := []byte(fmt.Sprintf("openai-compatibility:\n  - name: native-test\n    base-url: https://example.test/v1\n    wire-api: %q\n", wire))
				var cfg *Config
				var err error
				if mode == "parse" {
					cfg, err = ParseConfigBytes(data)
				} else {
					path := filepath.Join(t.TempDir(), "config.yaml")
					if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
						t.Fatal(errWrite)
					}
					cfg, err = LoadConfigOptional(path, mode == "optional load")
				}
				if !valid {
					if err == nil || !strings.Contains(err.Error(), "openai-compatibility[0].wire-api") {
						t.Fatalf("error=%v, want indexed wire-api validation error", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got, want := cfg.OpenAICompatibility[0].WireAPI, strings.ToLower(strings.TrimSpace(wire)); got != want {
					t.Errorf("wire-api=%q, want %q", got, want)
				}
			})
		}
	}
}

func TestOpenAICompatibilityInvalidWireAPIDoesNotRewriteConfig(t *testing.T) {
	data := []byte("remote-management:\n  secret-key: test-not-a-real-secret\nopenai-compatibility:\n  - name: invalid\n    base-url: https://example.test\n    wire-api: respones\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigOptional(path, false); err == nil {
		t.Error("invalid wire-api was accepted")
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != string(data) {
		t.Fatal("invalid config was rewritten before validation")
	}
}
