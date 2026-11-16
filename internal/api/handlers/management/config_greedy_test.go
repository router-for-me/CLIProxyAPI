package management

import "testing"

func TestLegacyRoutingStrategyDoesNotEnableGreedy(t *testing.T) {
	got, ok := normalizeRoutingStrategy(" GREEDY ")
	if ok || got != "" {
		t.Fatalf("got %s %v", got, ok)
	}
}
