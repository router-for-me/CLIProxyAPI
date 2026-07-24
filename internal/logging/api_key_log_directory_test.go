package logging

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIKeyLogDirectory(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "named key", key: "cpa_team-a_0123456789abcdef", want: "team-a"},
		{name: "empty key", key: "", want: "unauthenticated"},
		{name: "unsafe alias falls back", key: "cpa_../escape_0123456789abcdef", want: "key-"},
		{name: "legacy key is fingerprinted", key: "legacy-secret", want: "key-"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := APIKeyLogDirectory(test.key)
			if strings.HasSuffix(test.want, "-") {
				if !strings.HasPrefix(got, test.want) {
					t.Fatalf("APIKeyLogDirectory() = %q, want prefix %q", got, test.want)
				}
				return
			}
			if got != test.want {
				t.Fatalf("APIKeyLogDirectory() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFileRequestLoggerForAPIKey(t *testing.T) {
	root := t.TempDir()
	logger := NewFileRequestLogger(true, root, "", 10)
	scoped, ok := logger.ForAPIKey("cpa_automation_0123456789abcdef").(*FileRequestLogger)
	if !ok {
		t.Fatal("ForAPIKey() did not return a FileRequestLogger")
	}
	want := filepath.Join(root, "keys", "automation")
	if scoped.logsDir != want {
		t.Fatalf("scoped logsDir = %q, want %q", scoped.logsDir, want)
	}
	if logger.logsDir != root {
		t.Fatalf("base logger logsDir changed to %q", logger.logsDir)
	}
}

func TestFileRequestLoggerUsesConfiguredAPIKeyName(t *testing.T) {
	root := t.TempDir()
	logger := NewFileRequestLogger(true, root, "", 10)
	logger.SetAPIKeyNames([]string{"secret-key"}, []string{"张三 / Mobile"})
	scoped := logger.ForAPIKey("secret-key").(*FileRequestLogger)
	want := filepath.Join(root, "keys", "张三-Mobile")
	if scoped.logsDir != want {
		t.Fatalf("scoped logsDir = %q, want %q", scoped.logsDir, want)
	}
}

func TestFileRequestLoggerForKeyLabel(t *testing.T) {
	root := t.TempDir()
	logger := NewFileRequestLogger(true, root, "", 10)
	scoped, ok := logger.ForKeyLabel("team-a").(*FileRequestLogger)
	if !ok {
		t.Fatal("ForKeyLabel() did not return a FileRequestLogger")
	}
	want := filepath.Join(root, "keys", "team-a")
	if scoped.logsDir != want {
		t.Fatalf("scoped logsDir = %q, want %q", scoped.logsDir, want)
	}
	// Sanitizes unsafe characters; never embeds the raw secret.
	scoped = logger.ForKeyLabel("Team A / prod").(*FileRequestLogger)
	want = filepath.Join(root, "keys", "Team-A-prod")
	if scoped.logsDir != want {
		t.Fatalf("scoped logsDir = %q, want %q", scoped.logsDir, want)
	}
	scoped = logger.ForKeyLabel("").(*FileRequestLogger)
	want = filepath.Join(root, "keys", "unauthenticated")
	if scoped.logsDir != want {
		t.Fatalf("empty label logsDir = %q, want %q", scoped.logsDir, want)
	}
}
