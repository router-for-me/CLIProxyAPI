package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type xaiUsageTestExecutor struct{}

func (xaiUsageTestExecutor) Identifier() string { return "xai" }

func TestUsageReporterIgnoresXAIResponseBuildSuffix(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()
	reporter := NewExecutorUsageReporter(ctx, xaiUsageTestExecutor{}, "grok-4.7", nil)

	reporter.SetResponseModel("grok-4.7-build")
	reporter.Publish(ctx, usage.Detail{TotalTokens: 3})

	if got := reporter.ResponseModel(); got != "grok-4.7-build" {
		t.Fatalf("reporter response model = %q, want %q", got, "grok-4.7-build")
	}
	if warnings := substitutionWarnings(hook); len(warnings) != 0 {
		t.Fatalf("expected no substitution warning, got %#v", warnings)
	}
}

func TestUsageReporterWarnsForXAIResponseBuildFastSuffix(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()
	reporter := NewExecutorUsageReporter(ctx, xaiUsageTestExecutor{}, "grok-4.7", nil)

	reporter.SetResponseModel("grok-4.7-build-fast")
	reporter.Publish(ctx, usage.Detail{TotalTokens: 3})

	warnings := substitutionWarnings(hook)
	if len(warnings) != 1 {
		t.Fatalf("substitution warnings = %d, want 1: %#v", len(warnings), warnings)
	}
	want := `xai executor: upstream served model "grok-4.7-build-fast" for requested model "grok-4.7" (auth_index=nil)`
	if warnings[0] != want {
		t.Fatalf("warning = %q, want %q", warnings[0], want)
	}
}
