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

func TestUsageReporter_SetServedModelIgnoresLeadingEmptyCalls(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetServedModel("")
	reporter.SetServedModel("   ")
	reporter.SetServedModel("claude-opus-5")
	reporter.SetServedModel("claude-haiku-4-5")

	record := reporter.buildRecord(usage.Detail{}, false, usage.Failure{})
	if record.ServedModel != "claude-opus-5" {
		t.Fatalf("ServedModel = %q, want %q: leading empty calls must not lock the slot",
			record.ServedModel, "claude-opus-5")
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

func TestUsageReporter_AdditionalModelRecordClearsServedModel(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5-codex", nil)
	reporter.SetServedModel("gpt-5-codex")

	record, ok := reporter.buildAdditionalModelRecord("gpt-image-1", usage.Detail{InputTokens: 10, OutputTokens: 5})
	if !ok {
		t.Fatal("buildAdditionalModelRecord() = ok false, want a secondary record")
	}
	if record.Model != "gpt-image-1" {
		t.Fatalf("Model = %q, want %q", record.Model, "gpt-image-1")
	}
	if record.ServedModel != "" {
		t.Fatalf("ServedModel = %q, want empty: secondary records must not pair with the primary served-model capture",
			record.ServedModel)
	}
}
