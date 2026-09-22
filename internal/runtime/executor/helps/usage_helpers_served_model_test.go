package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageReporter_SetServedModelKeepsFirstValue(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetServedModel("claude-opus-5")
	reporter.SetServedModel("claude-haiku-4-5")

	record := reporter.buildRecord(usage.Detail{}, false, usage.Failure{})
	if record.ServedModel != "claude-opus-5" {
		t.Fatalf("ServedModel = %q, want first reported value %q", record.ServedModel, "claude-opus-5")
	}
}

func TestUsageReporter_SetServedModelTrimsWhitespace(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetServedModel("  claude-opus-5  ")

	record := reporter.buildRecord(usage.Detail{}, false, usage.Failure{})
	if record.ServedModel != "claude-opus-5" {
		t.Fatalf("ServedModel = %q, want trimmed %q", record.ServedModel, "claude-opus-5")
	}
}

func TestUsageReporter_SetServedModelNilReporter(t *testing.T) {
	var reporter *UsageReporter
	reporter.SetServedModel("claude-opus-5") // must not panic
}
