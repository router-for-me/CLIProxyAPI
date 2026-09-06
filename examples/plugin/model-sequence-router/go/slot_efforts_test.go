package main

import (
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// Confirm that effortOrder, which ranks every tier the router validates,
// covers each discrete level thinking.ParseLevelSuffix documents as valid.
func TestEffortOrderRanksEveryDiscreteLevel(t *testing.T) {
	spelled := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	if len(effortOrder) != len(spelled) {
		t.Fatalf("effortOrder length = %d, want %d", len(effortOrder), len(spelled))
	}
	for _, name := range spelled {
		level, isLevel := thinking.ParseLevelSuffix(name)
		if !isLevel {
			t.Fatalf("ParseLevelSuffix(%q) rejected a documented level", name)
		}
		if !slices.Contains(effortOrder, level) {
			t.Fatalf("effortOrder omits %q", level)
		}
	}
}
