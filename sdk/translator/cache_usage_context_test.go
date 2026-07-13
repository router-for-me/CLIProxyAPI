package translator

import (
	"context"
	"testing"
)

func TestCodexClaudeCacheWriteEstimateContextIsRequestScoped(t *testing.T) {
	base := context.Background()
	enabled := WithCodexClaudeCacheWriteEstimate(base, true)
	disabled := WithCodexClaudeCacheWriteEstimate(enabled, false)

	if CodexClaudeCacheWriteEstimateEnabled(base) {
		t.Fatal("base context unexpectedly enabled")
	}
	if !CodexClaudeCacheWriteEstimateEnabled(enabled) {
		t.Fatal("enabled request context did not retain policy")
	}
	if CodexClaudeCacheWriteEstimateEnabled(disabled) {
		t.Fatal("explicit false did not override inherited policy")
	}
	if CodexClaudeCacheWriteEstimateEnabled(nil) {
		t.Fatal("nil context unexpectedly enabled")
	}
}
