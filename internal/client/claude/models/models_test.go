package models

import "testing"

func TestBuildResponse(t *testing.T) {
	availableModels := []map[string]any{
		{"id": "custom-z", "display_name": "Zebra", "max_tokens": 64000},
		{"id": "gpt-4o", "display_name": "Alpha"},
		{"id": "claude-opus-5", "display_name": "Alpha"},
		{"id": "gemini-2.5-pro", "display_name": "Beta"},
	}

	response := BuildResponse(availableModels)
	models, ok := response["data"].([]map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want []map[string]any", response["data"])
	}

	wantIDs := []string{
		"claude-opus-5",
		"claude/gpt-4o",
		"claude/gemini-2.5-pro",
		"claude/custom-z",
	}
	if len(models) != len(wantIDs) {
		t.Fatalf("len(data) = %d, want %d", len(models), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got, _ := models[i]["id"].(string); got != want {
			t.Fatalf("data[%d].id = %q, want %q", i, got, want)
		}
	}
	if got := models[3]["max_tokens"]; got != 64000 {
		t.Fatalf("max_tokens = %v, want 64000", got)
	}
	if got := response["has_more"]; got != false {
		t.Fatalf("has_more = %v, want false", got)
	}
	if got := response["first_id"]; got != wantIDs[0] {
		t.Fatalf("first_id = %v, want %q", got, wantIDs[0])
	}
	if got := response["last_id"]; got != wantIDs[len(wantIDs)-1] {
		t.Fatalf("last_id = %v, want %q", got, wantIDs[len(wantIDs)-1])
	}

	if got := availableModels[1]["id"]; got != "gpt-4o" {
		t.Fatalf("BuildResponse mutated input id to %v", got)
	}
	if got := availableModels[0]["id"]; got != "custom-z" {
		t.Fatalf("BuildResponse reordered input: first id = %v", got)
	}
}

func TestBuildResponseEmpty(t *testing.T) {
	response := BuildResponse(nil)
	models, ok := response["data"].([]map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want []map[string]any", response["data"])
	}
	if len(models) != 0 {
		t.Fatalf("len(data) = %d, want 0", len(models))
	}
	if response["first_id"] != "" || response["last_id"] != "" {
		t.Fatalf("empty response IDs = (%v, %v), want empty", response["first_id"], response["last_id"])
	}
}

func TestEnsureClaudeModelIDPrefix(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"empty", "", ""},
		{"catalog Claude model", "claude-opus-5", "claude-opus-5"},
		{"catalog Claude model with thinking suffix", "claude-opus-5(high)", "claude-opus-5(high)"},
		{"Claude lookalike is namespaced", "claude-team/gpt-4o", "claude/claude-team/gpt-4o"},
		{"already namespaced", "claude/gpt-4o", "claude/gpt-4o"},
		{"repeated namespace is not extended", "claude/claude/gpt-4o", "claude/claude/gpt-4o"},
		{"contains claude mid-string is namespaced", "my-claude-custom", "claude/my-claude-custom"},
		{"uppercase Claude prefix is namespaced", "Claude-Opus-5", "claude/Claude-Opus-5"},
		{"gpt model is namespaced", "gpt-4o", "claude/gpt-4o"},
		{"gemini model is namespaced", "gemini-2.5-pro", "claude/gemini-2.5-pro"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EnsureClaudeModelIDPrefix(tt.id); got != tt.want {
				t.Fatalf("EnsureClaudeModelIDPrefix(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestResolveClaudeModelIDPrefix(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"empty", "", ""},
		{"catalog Claude model", "claude-opus-5", "claude-opus-5"},
		{"catalog Claude model with thinking suffix", "claude-opus-5(high)", "claude-opus-5(high)"},
		{"non namespaced id unchanged", "gpt-4o", "gpt-4o"},
		{"regular claude prefix unchanged", "claude-team/gpt-4o", "claude-team/gpt-4o"},
		{"uppercase namespace unchanged", "Claude/gpt-4o", "Claude/gpt-4o"},
		{"legacy cloaked id is no longer decoded", "claude-fable-5-dd-o4-tpg", "claude-fable-5-dd-o4-tpg"},
		{"reserved claude credential prefix is consumed", "claude/gpt-4o", "gpt-4o"},
		{"namespaced gemini model", "claude/gemini-2.5-pro", "gemini-2.5-pro"},
		{"empty namespace is consumed", "claude/", ""},
		{"strips every namespace", "claude/claude/claude/gpt-4o", "gpt-4o"},
		{"stops at wrapped catalog Claude model", "claude/claude-opus-5", "claude-opus-5"},
		{"stops at repeatedly wrapped catalog Claude model", "claude/claude/claude-opus-5", "claude-opus-5"},
		{"preserves wrapped native thinking suffix", "claude/claude-opus-5(high)", "claude-opus-5(high)"},
		{"preserves thinking suffix exactly", "claude/claude/gpt-4o( High )", "gpt-4o( High )"},
		{"round trip", EnsureClaudeModelIDPrefix("custom-model-x"), "custom-model-x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveClaudeModelIDPrefix(tt.id); got != tt.want {
				t.Fatalf("ResolveClaudeModelIDPrefix(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
