package version

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"v1.2.3", "1.2.3"},
		{"v1.2.3-rc.1", "1.2.3-rc.1"},
		{"v1.2.3-5-gabc123", "1.2.3-5-gabc123"},
		{"v1.2.3-rc.1-5-gabc123", "1.2.3-rc.1-5-gabc123"},
		{"v1.2.3+dirty", "1.2.3+dirty"},
		{"1.2.3", "1.2.3"},
	}

	for _, tt := range tests {
		got, err := Normalize(tt.input)
		if err != nil {
			t.Fatalf("Normalize(%q) returned error: %v", tt.input, err)
		}
		if got != tt.expected {
			t.Fatalf("Normalize(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestNormalizeInvalid(t *testing.T) {
	invalid := []string{
		"",
		"v1",
		"v1.2",
		"v1.2.3.4",
		"v1.2.3-",
		"v1.2.3+",
	}
	for _, input := range invalid {
		if _, err := Normalize(input); err == nil {
			t.Fatalf("Normalize(%q) expected error, got nil", input)
		}
	}
}
