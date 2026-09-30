package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// RouteModel is the requested side of the substitution comparison; a reporter
// that loses it makes Task 4's alerts blind for that provider.
func TestUsageReporter_SetRouteModelRoundTrip(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetRouteModel("claude-opus-5-alias")

	record := reporter.buildRecord(context.Background(), usage.Detail{}, false, usage.Failure{})
	if record.RouteModel != "claude-opus-5-alias" {
		t.Fatalf("RouteModel = %q, want %q", record.RouteModel, "claude-opus-5-alias")
	}
}

func TestUsageReporter_SetRouteModelStaysEmptyWhenUnset(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)

	record := reporter.buildRecord(context.Background(), usage.Detail{}, false, usage.Failure{})
	if record.RouteModel != "" {
		t.Fatalf("RouteModel = %q, want empty: no accidental fallback pollution", record.RouteModel)
	}
}

func TestUsageReporter_SetRouteModelTrimsWhitespace(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetRouteModel(" x ")

	record := reporter.buildRecord(context.Background(), usage.Detail{}, false, usage.Failure{})
	if record.RouteModel != "x" {
		t.Fatalf("RouteModel = %q, want trimmed %q", record.RouteModel, "x")
	}
}

func TestUsageReporter_SetRouteModelNilReporter(t *testing.T) {
	var reporter *UsageReporter
	reporter.SetRouteModel("claude-opus-5") // must not panic
}
