package util

import (
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestExtractNumericSegments(t *testing.T) {
	tests := []struct {
		input string
		want  []int64
	}{
		{"gemini-1.5-flash", []int64{1, 5}},
		{"gemini-2.5-pro", []int64{2, 5}},
		{"gemini-3-flash", []int64{3}},
		{"gemini-3.1-flash-lite-preview", []int64{3, 1}},
		{"gemini-flash", nil},
		{"gemini-3.10-pro", []int64{3, 10}},
	}

	for _, tt := range tests {
		got := ExtractNumericSegments(tt.input)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ExtractNumericSegments(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestCompareModelVersions(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int // -1 for a < b, 1 for a > b, 0 for equal
	}{
		{"gemini-1.5-flash", "gemini-2.0-flash", -1},
		{"gemini-2.0-flash", "gemini-2.5-flash", -1},
		{"gemini-2.5-flash", "gemini-3-flash", -1},
		{"gemini-3-flash", "gemini-3.5-flash", -1},
		{"gemini-3.1-flash", "gemini-3.10-flash", -1},
		{"gemini-3.10-flash", "gemini-3.2-flash", 1},
		{"gemini-2.5-flash", "gemini-2.5-flash", 0},
		{"gemini-2.5-flash", "gemini-2.5-flash-lite", -1},
		{"gemini-2.5-pro", "gemini-1.5-pro", 1},
	}

	for _, tt := range tests {
		got := CompareModelVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareModelVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestResolveGeminiFamilyFallback(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	clientGemini := "test-gemini-fallback-client"
	clientBlocked := "test-blocked-fallback-client"

	modelRegistry.RegisterClient(clientGemini, "gemini", []*registry.ModelInfo{
		{ID: "gemini-1.5-flash", Created: now + 10},
		{ID: "gemini-2.0-flash", Created: now + 20},
		{ID: "gemini-2.5-flash", Created: now + 30},
		{ID: "gemini-1.5-pro", Created: now + 15},
		{ID: "gemini-2.5-pro", Created: now + 25},
	})

	// Register a model without a provider (known in registry, but no providers/scope)
	modelRegistry.RegisterClient(clientBlocked, "", []*registry.ModelInfo{
		{ID: "gemini-3.0-pro", Created: now + 40},
	})

	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientGemini)
		modelRegistry.UnregisterClient(clientBlocked)
	})

	tests := []struct {
		name       string
		inputModel string
		want       string
	}{
		{
			name:       "missing flash preview falls back to highest flash",
			inputModel: "gemini-3.1-flash-lite-preview",
			want:       "gemini-2.5-flash",
		},
		{
			name:       "missing older flash falls back to highest flash",
			inputModel: "gemini-1.0-flash",
			want:       "gemini-2.5-flash",
		},
		{
			name:       "missing pro preview falls back to highest pro",
			inputModel: "gemini-3.1-pro-preview",
			want:       "gemini-2.5-pro",
		},
		{
			name:       "unknown non-flash-pro does not remap",
			inputModel: "gemini-private",
			want:       "",
		},
		{
			name:       "unknown ultra-experimental does not remap",
			inputModel: "gemini-ultra-experimental",
			want:       "",
		},
		{
			name:       "non-gemini prefix does not remap",
			inputModel: "claude-3-pro",
			want:       "",
		},
		{
			name:       "model existing in registry but blocked fails closed (no fallback)",
			inputModel: "gemini-3.0-pro",
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveGeminiFamilyFallback(tt.inputModel)
			if got != tt.want {
				t.Fatalf("ResolveGeminiFamilyFallback(%q) = %q, want %q", tt.inputModel, got, tt.want)
			}
		})
	}
}
