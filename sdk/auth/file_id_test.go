package auth

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeFileAuthIDForOS(t *testing.T) {
	baseDir := filepath.Join("auths")
	path := filepath.Join(baseDir, "Sub", "My-Auth.JSON")
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		t.Fatalf("failed to build relative path: %v", err)
	}

	tests := []struct {
		name string
		goos string
		want string
	}{
		{name: "windows lowercases id", goos: "windows", want: strings.ToLower(rel)},
		{name: "linux preserves case", goos: "linux", want: rel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeFileAuthIDForOS(path, baseDir, tt.goos)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestNormalizeFileAuthIDForOSEmptyPath(t *testing.T) {
	if got := normalizeFileAuthIDForOS("   ", "auths", "windows"); got != "" {
		t.Fatalf("expected empty id, got %q", got)
	}
}
