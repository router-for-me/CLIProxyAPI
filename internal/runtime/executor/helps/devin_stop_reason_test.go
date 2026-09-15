package helps

import "testing"

func TestDevinStopReasonToInteractions(t *testing.T) {
	cases := []struct {
		code       uint64
		wantStatus string
		wantReason string
	}{
		{DevinStopReasonUnspecified, InteractionsStatusCompleted, InteractionsStopReasonStop},
		{DevinStopReasonIncomplete, InteractionsStatusIncomplete, InteractionsStopReasonLength},
		{DevinStopReasonStopPattern, InteractionsStatusCompleted, InteractionsStopReasonStop},
		{DevinStopReasonMaxTokens, InteractionsStatusIncomplete, InteractionsStopReasonLength},
		{4, InteractionsStatusCompleted, InteractionsStopReasonStop},
		{9, InteractionsStatusCompleted, InteractionsStopReasonStop},
		{DevinStopReasonFunctionCall, InteractionsStatusCompleted, InteractionsStopReasonToolCalls},
		{DevinStopReasonContentFilter, InteractionsStatusCompleted, InteractionsStopReasonContentFilter},
		{12, InteractionsStatusCompleted, InteractionsStopReasonStop},
		{13, InteractionsStatusCompleted, InteractionsStopReasonStop},
	}
	for _, tc := range cases {
		status, reason := DevinStopReasonToInteractions(tc.code)
		if status != tc.wantStatus || reason != tc.wantReason {
			t.Fatalf("code %d: got (%q, %q), want (%q, %q)", tc.code, status, reason, tc.wantStatus, tc.wantReason)
		}
	}
}
