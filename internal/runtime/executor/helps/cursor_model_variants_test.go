package helps

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestCursorVariantSelectionIsOrderIndependent(t *testing.T) {
	models := []*registry.ModelInfo{{ID: "gpt-5.5-xhigh"}, {ID: "gpt-5.5-extra-high"}}
	for range 2 {
		got, err := ResolveCursorModel("gpt-5.5", []byte(`{"reasoning_effort":"xhigh"}`), "openai", models)
		if err != nil || got != "gpt-5.5-extra-high" {
			t.Fatalf("got %q, %v; want deterministic variant", got, err)
		}
		models[0], models[1] = models[1], models[0]
	}
}

func TestCursorResolutionErrorKinds(t *testing.T) {
	models := []*registry.ModelInfo{{ID: "claude-fable-5-1-thinking-high"}}
	for _, tc := range []struct {
		body        string
		unavailable bool
	}{
		{`{"reasoning_effort":"low"}`, true},
		{`{"reasoning_effort":"high","service_tier":"priority"}`, true},
		{`{"reasoning_effort":"invalid"}`, false},
		{`{"thinking":{"type":"invalid"}}`, false},
		{`{"speed":"invalid"}`, false},
		{`{"reasoning_effort":"none","thinking":{"type":"enabled"}}`, false},
	} {
		_, err := ResolveCursorModel("claude-fable-5-1", []byte(tc.body), "openai", models)
		if err == nil || errors.Is(err, ErrCursorVariantUnavailable) != tc.unavailable {
			t.Fatalf("%s: error=%v, want unavailable=%t", tc.body, err, tc.unavailable)
		}
	}
}

func TestResolveCursorModel(t *testing.T) {
	models := []*registry.ModelInfo{}
	for _, id := range []string{"claude-fable-5-1-medium", "claude-fable-5-1-thinking-medium", "claude-fable-5-1-thinking-high", "claude-fable-5-1-thinking-high-fast", "composer-2.5", "composer-2.5-fast", "cursor-grok-4.6-high", "cursor-grok-4.6-xhigh"} {
		models = append(models, &registry.ModelInfo{ID: id})
	}
	for _, tc := range []struct {
		name, model, format, body, want string
		fail                            bool
	}{
		{"response", "claude-fable-5-1", "openai-response", `{"reasoning":{"effort":"high"}}`, "claude-fable-5-1-thinking-high", false},
		{"chat", "cursor-grok-4.6", "openai", `{"reasoning_effort":"xhigh"}`, "cursor-grok-4.6-xhigh", false},
		{"claude", "claude-fable-5-1", "claude", `{"thinking":{"type":"enabled"},"output_config":{"effort":"high"}}`, "claude-fable-5-1-thinking-high", false},
		{"fast", "claude-fable-5-1", "openai-response", `{"reasoning":{"effort":"high"},"service_tier":"priority"}`, "claude-fable-5-1-thinking-high-fast", false},
		{"none", "claude-fable-5-1", "openai-response", `{"reasoning":{"effort":"none"}}`, "claude-fable-5-1-medium", false},
		{"default", "composer-2.5", "claude", `{}`, "composer-2.5", false},
		{"composer fast", "composer-2.5", "claude", `{"speed":"fast"}`, "composer-2.5-fast", false},
		{"exact", "claude-fable-5-1-thinking-high", "claude", `{"output_config":{"effort":"low"}}`, "claude-fable-5-1-thinking-high", false},
		{"unsupported", "claude-fable-5-1", "openai-response", `{"reasoning":{"effort":"low"}}`, "", true},
		{"unsupported fast", "cursor-grok-4.6", "openai", `{"service_tier":"priority"}`, "", true},
		{"suffix response", "claude-fable-5-1(high)", "openai-response", `{"reasoning":{"effort":"medium"}}`, "claude-fable-5-1-thinking-high", false},
		{"suffix chat", "cursor-grok-4.6(xhigh)", "openai", `{"reasoning_effort":"high"}`, "cursor-grok-4.6-xhigh", false},
		{"suffix claude", "claude-fable-5-1(high)", "claude", `{"thinking":{"type":"enabled"},"output_config":{"effort":"medium"}}`, "claude-fable-5-1-thinking-high", false},
		{"suffix overrides disabled thinking", "claude-fable-5-1(high)", "claude", `{"thinking":{"type":"disabled"}}`, "claude-fable-5-1-thinking-high", false},
		{"suffix none overrides enabled thinking", "claude-fable-5-1(none)", "claude", `{"thinking":{"type":"enabled"},"output_config":{"effort":"high"}}`, "claude-fable-5-1-medium", false},
		{"suffix numeric", "claude-fable-5-1(16384)", "claude", `{"thinking":{"type":"disabled"}}`, "claude-fable-5-1-thinking-high", false},
		{"suffix auto", "claude-fable-5-1(auto)", "claude", `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`, "claude-fable-5-1-thinking-medium", false},
		{"suffix fast", "claude-fable-5-1(high)", "openai-response", `{"service_tier":"priority"}`, "claude-fable-5-1-thinking-high-fast", false},
		{"suffix exact", "claude-fable-5-1-thinking-high(low)", "claude", `{"thinking":{"type":"disabled"},"output_config":{"effort":"medium"}}`, "claude-fable-5-1-thinking-high", false},
		{"suffix exact base", "composer-2.5(high)", "openai", `{}`, "composer-2.5", false},
		{"suffix unknown family", "unknown-model(high)", "openai", `{}`, "unknown-model(high)", false},
		{"suffix unsupported", "claude-fable-5-1(low)", "openai-response", `{"reasoning":{"effort":"high"}}`, "", true},
		{"invalid suffix body fallback", "claude-fable-5-1(invalid)", "claude", `{"thinking":{"type":"enabled"},"output_config":{"effort":"high"}}`, "claude-fable-5-1-thinking-high", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCursorModel(tc.model, []byte(tc.body), tc.format, models)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %q, %v; want %q, failure=%v", got, err, tc.want, tc.fail)
			}
		})
	}
	augmented := AddCursorModelFamilies(models)
	if len(augmented) != 3 {
		t.Fatalf("unexpected family catalog length %d", len(augmented))
	}
	if models[0].Thinking != nil {
		t.Fatal("mutated input")
	}
}

func TestCursorClaudeOptionsForEffortOnlyFamilies(t *testing.T) {
	models := []*registry.ModelInfo{{ID: "gpt-5.6-sol-high"}, {ID: "gpt-5.6-sol-medium"}, {ID: "gpt-5.6-sol-none"}}
	for _, tc := range []struct{ body, want string }{
		{`{"thinking":{"type":"enabled"},"output_config":{"effort":"high"}}`, "gpt-5.6-sol-high"},
		{`{"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}}`, "gpt-5.6-sol-medium"},
		{`{"thinking":{"type":"enabled","budget_tokens":16384}}`, "gpt-5.6-sol-high"},
		{`{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`, "gpt-5.6-sol-none"},
	} {
		got, err := ResolveCursorModel("gpt-5.6-sol", []byte(tc.body), "claude", models)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %s, %v; want %s", tc.body, got, err, tc.want)
		}
	}
}

func TestAddCursorModelFamilies(t *testing.T) {
	models := []*registry.ModelInfo{
		{ID: "composer-2.5", DisplayName: "Composer", ContextLength: 200000},
		{ID: "composer-2.5-fast"},
		{ID: "claude-opus-5-thinking-high", ContextLength: 1000000},
		{ID: "claude-opus-5-low"},
		{ID: "claude-opus-5-thinking-high"},
	}
	got := AddCursorModelFamilies(models)
	if len(got) != 2 || got[0].ID != "composer-2.5" || got[1].ID != "claude-opus-5" {
		t.Fatalf("collapsed catalog = %v", modelIDs(got))
	}
	if !got[0].SupportsFast || got[0].Thinking != nil {
		t.Fatalf("composer family = %+v", got[0])
	}
	family := got[1]
	if family.Thinking == nil || len(family.Thinking.Levels) != 2 || family.Thinking.Levels[0] != "low" || family.Thinking.Levels[1] != "high" || family.SupportsFast {
		t.Fatalf("bad family: %+v", family)
	}
	for _, hidden := range []string{"composer-2.5-fast", "claude-opus-5-thinking-high", "claude-opus-5-low"} {
		for _, model := range got {
			if model.ID == hidden {
				t.Fatalf("variant %s remained in the catalog", hidden)
			}
		}
	}
	if models[0].DisplayName != "Composer" || models[2].Thinking != nil {
		t.Fatal("mutated inputs")
	}
	if got[0] == models[0] || got[0].ContextLength != 200000 || got[0].DisplayName != "Composer" {
		t.Fatal("base model metadata not independently preserved")
	}
	base, variant := cursorVariant("gpt-5.5-extra-high-fast")
	if base != "gpt-5.5" || variant.Effort != "xhigh" || !variant.Fast {
		t.Fatalf("legacy extra-high parsing: %s %+v", base, variant)
	}
}

func TestComposerIgnoresHarnessEffortDefaults(t *testing.T) {
	models := []*registry.ModelInfo{{ID: "composer-2.5"}, {ID: "composer-2.5-fast"}}
	for _, tc := range []struct{ format, body, want string }{
		{"openai-response", `{"reasoning":{"effort":"medium"}}`, "composer-2.5"},
		{"openai", `{"reasoning_effort":"high","service_tier":"priority"}`, "composer-2.5-fast"},
		{"claude", `{"output_config":{"effort":"medium"},"thinking":{"type":"enabled","budget_tokens":16384}}`, "composer-2.5"},
		{"claude", `{"output_config":{"effort":"high"},"speed":"fast"}`, "composer-2.5-fast"},
	} {
		got, err := ResolveCursorModel("composer-2.5", []byte(tc.body), tc.format, models)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %s, %v; want %s", tc.body, got, err, tc.want)
		}
	}
}

func TestCursorSingletonMetadataUnchanged(t *testing.T) {
	model := &registry.ModelInfo{ID: "gpt-5-mini", DisplayName: "GPT-5 Mini"}
	got := AddCursorModelFamilies([]*registry.ModelInfo{model})
	if len(got) != 1 || got[0] != model {
		t.Fatal("singleton catalog entry changed")
	}
}

func TestAddCursorModelFamiliesThinkingOnly(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		base := &registry.ModelInfo{ID: "claude-fable-5-1", DisplayName: "Fable", ContextLength: 200000, SupportedParameters: []string{"tools"}}
		variant := &registry.ModelInfo{ID: "claude-fable-5-1-thinking"}
		models := []*registry.ModelInfo{base, variant}
		if reverse {
			models[0], models[1] = models[1], models[0]
		}
		got := AddCursorModelFamilies(models)
		if len(got) != 1 || got[0].ID != base.ID {
			t.Fatalf("reverse=%t: catalog = %v", reverse, modelIDs(got))
		}
		family := got[0]
		if family.Thinking == nil || len(family.Thinking.Levels) != 0 {
			t.Fatalf("reverse=%t: thinking-only support not advertised: %+v", reverse, family.Thinking)
		}
		if family == base || family.ID != base.ID || family.DisplayName != base.DisplayName || family.ContextLength != base.ContextLength {
			t.Fatalf("reverse=%t: base metadata not independently preserved: %+v", reverse, family)
		}
		if len(family.SupportedParameters) != 1 || family.SupportedParameters[0] != "tools" {
			t.Fatalf("invented effort support: %v", family.SupportedParameters)
		}
		if base.Thinking != nil || variant.Thinking != nil {
			t.Fatal("mutated input thinking metadata")
		}
		for _, tc := range []struct{ body, want string }{
			{`{"thinking":{"type":"enabled"}}`, variant.ID},
			{`{"thinking":{"type":"disabled"}}`, base.ID},
		} {
			resolved, err := ResolveCursorModel(family.ID, []byte(tc.body), "claude", models)
			if err != nil || resolved != tc.want {
				t.Fatalf("%s: got %q, %v; want %q", tc.body, resolved, err, tc.want)
			}
		}
		base.Thinking = &registry.ThinkingSupport{Min: 1024, Max: 32768, ZeroAllowed: true}
		got = AddCursorModelFamilies(models)
		if len(got) != 1 || got[0].Thinking != base.Thinking {
			t.Fatal("replaced existing thinking metadata")
		}
	}
}

func TestCursorRoutingModels_NilAndEmptySliceSemantics(t *testing.T) {
	authID := t.Name()
	t.Cleanup(func() { DeleteCursorRoutingModels(authID) })

	fallback := []*registry.ModelInfo{{ID: "fallback-model"}}
	active := []*registry.ModelInfo{{ID: "active-model"}}
	empty := []*registry.ModelInfo{}

	// 1. Initial state: absent key returns fallback
	if got := CursorRoutingModels(authID, fallback); len(got) != 1 || got[0].ID != "fallback-model" {
		t.Fatalf("expected fallback model, got: %#v", got)
	}

	// 2. Store active models: returns active
	StoreCursorRoutingModels(authID, active)
	if got := CursorRoutingModels(authID, fallback); len(got) != 1 || got[0].ID != "active-model" {
		t.Fatalf("expected active model, got: %#v", got)
	}

	// 3. Storing nil normalizes to authoritative empty catalog (does NOT fall back to unfiltered models)
	StoreCursorRoutingModels(authID, nil)
	if got := CursorRoutingModels(authID, fallback); len(got) != 0 {
		t.Fatalf("expected authoritative empty slice after Store(nil), got: %#v", got)
	}

	// 4. Storing non-nil empty slice is also authoritative (does NOT fall back)
	StoreCursorRoutingModels(authID, empty)
	if got := CursorRoutingModels(authID, fallback); len(got) != 0 {
		t.Fatalf("expected authoritative empty slice after Store(empty), got: %#v", got)
	}

	// 5. DeleteCursorRoutingModels clears key and restores fallback
	DeleteCursorRoutingModels(authID)
	if got := CursorRoutingModels(authID, fallback); len(got) != 1 || got[0].ID != "fallback-model" {
		t.Fatalf("expected fallback after Delete, got: %#v", got)
	}
}

func modelIDs(models []*registry.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil {
			ids = append(ids, "")
			continue
		}
		ids = append(ids, model.ID)
	}
	return ids
}
