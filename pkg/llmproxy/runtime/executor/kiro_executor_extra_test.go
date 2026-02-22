package executor

import (
	"strings"
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro"
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

func TestFormatKiroCooldownError(t *testing.T) {
	t.Run("suspended has remediation", func(t *testing.T) {
		err := formatKiroCooldownError(2*time.Minute, kiroauth.CooldownReasonSuspended)
		msg := err.Error()
		if !strings.Contains(msg, "reason: account_suspended") {
			t.Fatalf("expected cooldown reason in message, got %q", msg)
		}
		if !strings.Contains(msg, "re-auth this Kiro entry or switch auth index") {
			t.Fatalf("expected suspension remediation in message, got %q", msg)
		}
	})

	t.Run("quota has routing guidance", func(t *testing.T) {
		err := formatKiroCooldownError(30*time.Second, kiroauth.CooldownReason429)
		msg := err.Error()
		if !strings.Contains(msg, "reason: rate_limit_exceeded") {
			t.Fatalf("expected cooldown reason in message, got %q", msg)
		}
		if !strings.Contains(msg, "quota-exceeded.switch-project") {
			t.Fatalf("expected quota guidance in message, got %q", msg)
		}
	})
}

func TestFormatKiroSuspendedStatusMessage(t *testing.T) {
	msg := formatKiroSuspendedStatusMessage([]byte(`{"status":"SUSPENDED"}`))
	if !strings.Contains(msg, `{"status":"SUSPENDED"}`) {
		t.Fatalf("expected upstream response body in message, got %q", msg)
	}
	if !strings.Contains(msg, "re-auth this Kiro entry or use another auth index") {
		t.Fatalf("expected remediation text in message, got %q", msg)
	}
}
