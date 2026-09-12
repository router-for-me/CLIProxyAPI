package main

import "testing"

func TestSortBackendsByPenaltyDeprioritizesFailures(t *testing.T) {
	resetBackendPenaltiesForTest()
	t.Cleanup(resetBackendPenaltiesForTest)
	recordBackendFailure(backendAntigravityGoogle)
	recordBackendFailure(backendAntigravityGoogle)
	ordered := sortBackendsByPenalty([]routeBackend{
		backendAntigravityGoogle,
		backendCodexWebSearch,
		backendXAIWebSearch,
	})
	if ordered[0] != backendCodexWebSearch {
		t.Fatalf("ordered = %v, want codex first after antigravity penalty", ordered)
	}
}
