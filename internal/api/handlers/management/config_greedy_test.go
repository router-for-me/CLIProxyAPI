package management

import "testing"

func TestNormalizeGreedyRoutingStrategy(t *testing.T) {
	got, ok := normalizeRoutingStrategy(" GREEDY ")
	if !ok || got != "greedy" {
		t.Fatalf("got %s %v", got, ok)
	}
}
