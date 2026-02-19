package executor

import (
	"testing"
)

func TestKiroExecutor_MapModelToKiro(t *testing.T) {
	e := &KiroExecutor{}

	tests := []struct {
		model string
		want  string
	}{
		{"amazonq-claude-opus-4-6", "claude-opus-4.6"},
		{"kiro-claude-sonnet-4-5", "claude-sonnet-4.5"},
		{"claude-haiku-4.5", "claude-haiku-4.5"},
		{"claude-opus-4.6-agentic", "claude-opus-4.6"},
		{"unknown-haiku-model", "claude-haiku-4.5"},
		{"claude-3.7-sonnet", "claude-3-7-sonnet-20250219"},
		{"claude-4.5-sonnet", "claude-sonnet-4.5"},
		{"something-else", "claude-sonnet-4.5"}, // Default fallback
	}

	for _, tt := range tests {
		got := e.mapModelToKiro(tt.model)
		if got != tt.want {
			t.Errorf("mapModelToKiro(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestDetermineAgenticMode(t *testing.T) {
	tests := []struct {
		model      string
		isAgentic  bool
		isChatOnly bool
	}{
		{"claude-opus-4.6-agentic", true, false},
		{"claude-opus-4.6-chat", false, true},
		{"claude-opus-4.6", false, false},
		{"anything-else", false, false},
	}

	for _, tt := range tests {
		isAgentic, isChatOnly := determineAgenticMode(tt.model)
		if isAgentic != tt.isAgentic || isChatOnly != tt.isChatOnly {
			t.Errorf("determineAgenticMode(%q) = (%v, %v), want (%v, %v)", tt.model, isAgentic, isChatOnly, tt.isAgentic, tt.isChatOnly)
		}
	}
}

func TestExtractRegionFromProfileARN(t *testing.T) {
	tests := []struct {
		arn  string
		want string
	}{
		{"arn:aws:iam:us-east-1:123456789012:role/name", "us-east-1"},
		{"arn:aws:iam:us-west-2:123456789012:role/name", "us-west-2"},
		{"arn:aws:iam::123456789012:role/name", ""}, // No region
		{"", ""},
	}

	for _, tt := range tests {
		got := extractRegionFromProfileARN(tt.arn)
		if got != tt.want {
			t.Errorf("extractRegionFromProfileARN(%q) = %q, want %q", tt.arn, got, tt.want)
		}
	}
}
