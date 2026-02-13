package antigravity

import (
	"strings"
	"testing"
)

func TestMetadataPlatformForGOOS(t *testing.T) {
	tests := []struct {
		name string
		goos string
		want string
	}{
		{name: "windows", goos: "windows", want: "WINDOWS"},
		{name: "darwin", goos: "darwin", want: "MACOS"},
		{name: "linux", goos: "linux", want: "MACOS"},
		{name: "unknown", goos: "freebsd", want: "MACOS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadataPlatformForGOOS(tt.goos)
			if got != tt.want {
				t.Fatalf("metadataPlatformForGOOS(%q) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}

func TestAntigravityJSONClientMetadata(t *testing.T) {
	m := antigravityJSONClientMetadata()
	if m == "" {
		t.Fatal("metadata should not be empty")
	}
	if !(strings.Contains(m, `"ideType":"ANTIGRAVITY"`) && strings.Contains(m, `"pluginType":"GEMINI"`)) {
		t.Fatalf("unexpected metadata: %s", m)
	}
}
