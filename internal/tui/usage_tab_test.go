package tui

import "testing"

func TestBuildProviderAvailabilitySnapshots(t *testing.T) {
	files := []map[string]any{
		{
			"account_type":   "oauth",
			"provider":       "claude",
			"name":           "claude-a",
			"email":          "a@example.com",
			"status":         "active",
			"status_message": "ok",
			"disabled":       false,
			"unavailable":    false,
		},
		{
			"account_type":   "oauth",
			"provider":       "anthropic",
			"name":           "claude-b",
			"email":          "b@example.com",
			"status":         "quota",
			"status_message": "quota exhausted",
			"disabled":       false,
			"unavailable":    true,
		},
		{
			"account_type":   "oauth",
			"provider":       "codex",
			"name":           "codex-a",
			"email":          "c@example.com",
			"status":         "disabled",
			"status_message": "disabled",
			"disabled":       true,
			"unavailable":    false,
		},
		{
			"account_type":   "oauth",
			"provider":       "gemini-cli",
			"name":           "gemini-a",
			"email":          "g@example.com",
			"status":         "active",
			"status_message": "ok",
			"disabled":       false,
			"unavailable":    false,
		},
		{
			"account_type":   "api_key",
			"provider":       "codex",
			"name":           "should-ignore",
			"email":          "ignored@example.com",
			"status":         "active",
			"status_message": "ok",
			"disabled":       false,
			"unavailable":    false,
		},
	}

	snapshots := buildProviderAvailabilitySnapshots(files)
	if len(snapshots) != 3 {
		t.Fatalf("expected 3 provider snapshots, got %d", len(snapshots))
	}

	byProvider := make(map[string]providerAvailabilitySnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byProvider[snapshot.Provider] = snapshot
	}

	claude, exists := byProvider["claude"]
	if !exists {
		t.Fatalf("expected claude provider snapshot")
	}
	if claude.Total != 2 || claude.Available != 1 || claude.Unavailable != 1 || claude.Disabled != 0 {
		t.Fatalf("unexpected claude counts: %+v", claude)
	}
	if len(claude.Credentials) != 2 {
		t.Fatalf("expected 2 claude credentials, got %d", len(claude.Credentials))
	}
	if claude.Credentials[0].Name != "claude-a" || claude.Credentials[1].Name != "claude-b" {
		t.Fatalf("expected credential names sorted by name, got %+v", claude.Credentials)
	}

	codex, exists := byProvider["codex"]
	if !exists {
		t.Fatalf("expected codex provider snapshot")
	}
	if codex.Total != 1 || codex.Available != 0 || codex.Unavailable != 0 || codex.Disabled != 1 {
		t.Fatalf("unexpected codex counts: %+v", codex)
	}

	gemini, exists := byProvider["gemini-cli"]
	if !exists {
		t.Fatalf("expected gemini-cli provider snapshot")
	}
	if gemini.Total != 1 || gemini.Available != 1 || gemini.Unavailable != 0 || gemini.Disabled != 0 {
		t.Fatalf("unexpected gemini counts: %+v", gemini)
	}
}

func TestRecordAvailabilitySampleRingBuffer(t *testing.T) {
	model := newUsageTabModel(nil)
	snapshot := []providerAvailabilitySnapshot{{Provider: "claude"}}

	for index := 0; index < availabilityHistoryMaxPoints+7; index++ {
		snapshot[0].AvailabilityPct = float64(index)
		model.recordAvailabilitySample(snapshot)
	}

	history := model.availabilityHistory["claude"]
	if len(history) != availabilityHistoryMaxPoints {
		t.Fatalf("expected history length %d, got %d", availabilityHistoryMaxPoints, len(history))
	}
	if history[0] != 7 {
		t.Fatalf("expected oldest retained point 7, got %.1f", history[0])
	}
	if history[len(history)-1] != float64(availabilityHistoryMaxPoints+6) {
		t.Fatalf("expected latest retained point %.1f, got %.1f", float64(availabilityHistoryMaxPoints+6), history[len(history)-1])
	}
}

func TestRenderSparkline(t *testing.T) {
	line := renderSparkline([]float64{0, 25, 50, 75, 100})
	if line == "" {
		t.Fatal("expected non-empty sparkline")
	}
	if len([]rune(line)) != 5 {
		t.Fatalf("expected sparkline length 5, got %d", len([]rune(line)))
	}

	clamped := renderSparkline([]float64{-10, 120})
	if len([]rune(clamped)) != 2 {
		t.Fatalf("expected clamped sparkline length 2, got %d", len([]rune(clamped)))
	}
}
