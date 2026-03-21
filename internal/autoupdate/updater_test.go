package autoupdate

import (
	"testing"
)

func TestParseSemver(t *testing.T) {
	tests := []struct {
		input    string
		expected []int
	}{
		{"6.8.55", []int{6, 8, 55}},
		{"v6.8.55", []int{6, 8, 55}},
		{"6.8.55-beta1", []int{6, 8, 55}},
		{"1.0.0", []int{1, 0, 0}},
		{"dev", nil},
		{"", nil},
		{"1.2", nil},
	}

	for _, tt := range tests {
		result := parseSemver(tt.input)
		if tt.expected == nil {
			if result != nil {
				t.Errorf("parseSemver(%q) = %v, want nil", tt.input, result)
			}
			continue
		}
		if result == nil {
			t.Errorf("parseSemver(%q) = nil, want %v", tt.input, tt.expected)
			continue
		}
		for i := 0; i < 3; i++ {
			if result[i] != tt.expected[i] {
				t.Errorf("parseSemver(%q)[%d] = %d, want %d", tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"6.8.55", "6.8.56", true},
		{"6.8.55", "6.9.0", true},
		{"6.8.55", "7.0.0", true},
		{"6.8.55", "6.8.55", false},
		{"6.8.56", "6.8.55", false},
		{"6.9.0", "6.8.99", false},
		{"dev", "6.8.55", false},
		{"6.8.55", "dev", false},
	}

	for _, tt := range tests {
		got := isNewer(tt.current, tt.latest)
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	content := `abc123def456  CLIProxyAPI_6.8.55_linux_amd64.tar.gz
789abc012def  CLIProxyAPI_6.8.55_windows_amd64.zip
fedcba987654  CLIProxyAPI_6.8.55_darwin_arm64.tar.gz
`

	hash, err := parseChecksums(content, "CLIProxyAPI_6.8.55_windows_amd64.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != "789abc012def" {
		t.Errorf("got %q, want %q", hash, "789abc012def")
	}

	_, err = parseChecksums(content, "nonexistent.zip")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestArchiveName(t *testing.T) {
	name := archiveName("6.8.55")
	// Just check it contains the version and project name
	if name == "" {
		t.Error("archiveName returned empty string")
	}
	if !contains(name, "6.8.55") || !contains(name, projectName) {
		t.Errorf("archiveName(%q) = %q, expected to contain version and project name", "6.8.55", name)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
