package main

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestCodexModelsURL(t *testing.T) {
	got, err := codexModelsURL(" 0.144.1 ")
	if err != nil {
		t.Fatalf("codexModelsURL: %v", err)
	}
	want := "https://chatgpt.com/backend-api/codex/models?client_version=0.144.1"
	if got != want {
		t.Fatalf("codexModelsURL = %q, want %q", got, want)
	}
}

func TestCountModels(t *testing.T) {
	count, err := countModels([]byte(`{"models":[{"slug":"a"},{"slug":"b"}]}`))
	if err != nil {
		t.Fatalf("countModels(valid): %v", err)
	}
	if count != 2 {
		t.Fatalf("countModels(valid) = %d, want 2", count)
	}

	// Upstream dumps may omit CPA catalog-required fields; counting must still work.
	count, err = countModels([]byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`))
	if err != nil {
		t.Fatalf("countModels(incomplete upstream model): %v", err)
	}
	if count != 1 {
		t.Fatalf("countModels(incomplete upstream model) = %d, want 1", count)
	}

	count, err = countModels([]byte(`{"models":[]}`))
	if err != nil {
		t.Fatalf("countModels(empty): %v", err)
	}
	if count != 0 {
		t.Fatalf("countModels(empty) = %d, want 0", count)
	}

	if _, err := countModels([]byte(`{"models":`)); err == nil {
		t.Fatal("countModels(malformed) error = nil, want error")
	}
	if _, err := countModels([]byte(`{}`)); err == nil {
		t.Fatal("countModels(missing models) error = nil, want error")
	}
}

func TestEnsureBaselineModel(t *testing.T) {
	t.Run("clones first model as gpt-5.5 when missing", func(t *testing.T) {
		raw := []byte(`{"models":[{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","visibility":"list","tool_mode":"code_mode_only","use_responses_lite":true,"supports_reasoning_effort_updates":true},{"slug":"gpt-5.6-sol","visibility":"list"}]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 3 {
			t.Fatalf("count = %d, want 3", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.0.slug").String(); got != "gpt-6.1-sol" {
			t.Fatalf("models[0].slug = %q, want gpt-6.1-sol", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.0.tool_mode").String(); got != "code_mode_only" {
			t.Fatalf("models[0].tool_mode = %q, want code_mode_only", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.slug").String(); got != "gpt-5.5" {
			t.Fatalf("models[2].slug = %q, want gpt-5.5", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.display_name").String(); got != "GPT-6.1-Sol" {
			t.Fatalf("models[2].display_name = %q, want GPT-6.1-Sol", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.visibility").String(); got != "list" {
			t.Fatalf("models[2].visibility = %q, want list", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.tool_mode"); got.Type != ojson.String || got.Str != "direct" {
			t.Fatalf("models[2].tool_mode = %v (%s), want string \"direct\"", got.Value(), got.Type)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.use_responses_lite"); got.Type != ojson.False {
			t.Fatalf("models[2].use_responses_lite = %v (%s), want false", got.Value(), got.Type)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.supports_reasoning_effort_updates"); got.Type != ojson.False {
			t.Fatalf("models[2].supports_reasoning_effort_updates = %v (%s), want false", got.Value(), got.Type)
		}
	})

	t.Run("clones first model and forces visibility to list when first model is hidden", func(t *testing.T) {
		raw := []byte(`{"models":[{"slug":"gpt-reserve","visibility":"hide"}]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 2 {
			t.Fatalf("count = %d, want 2", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.0.visibility").String(); got != "hide" {
			t.Fatalf("models[0].visibility = %q, want hide", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.1.slug").String(); got != "gpt-5.5" {
			t.Fatalf("models[1].slug = %q, want gpt-5.5", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.1.visibility").String(); got != "list" {
			t.Fatalf("models[1].visibility = %q, want list", got)
		}
	})

	t.Run("forces existing gpt-5.5 visibility to list when hidden", func(t *testing.T) {
		raw := []byte(`{"models":[{"slug":"gpt-6.1-sol","visibility":"list"},{"slug":"gpt-5.5","visibility":"hide"}]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 2 {
			t.Fatalf("count = %d, want 2", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.1.slug").String(); got != "gpt-5.5" {
			t.Fatalf("models[1].slug = %q, want gpt-5.5", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.1.visibility").String(); got != "list" {
			t.Fatalf("models[1].visibility = %q, want list", got)
		}
	})

	t.Run("forces existing gpt-5.5 visibility to exact list when padded or non-string", func(t *testing.T) {
		for _, raw := range [][]byte{
			[]byte(`{"models":[{"slug":"gpt-5.5","visibility":" list "}]}`),
			[]byte(`{"models":[{"slug":"gpt-5.5","visibility":false}]}`),
		} {
			out, count, errEnsure := ensureBaselineModel(raw)
			if errEnsure != nil {
				t.Fatalf("ensureBaselineModel(%s): %v", raw, errEnsure)
			}
			if count != 1 {
				t.Fatalf("count = %d, want 1", count)
			}
			vis := ojson.GetBytesNoCopy(out, "models.0.visibility")
			if vis.Type != ojson.String || vis.Str != "list" {
				t.Fatalf("models[0].visibility = %v (%s), want string \"list\"", vis.Value(), vis.Type)
			}
		}
	})

	t.Run("skips hidden non-API first model and clones first visible API-supported model as gpt-5.5", func(t *testing.T) {
		raw := []byte(`{"models":[
			{"slug":"gpt-6-chat-thinking","display_name":"GPT-6 Chat Thinking","visibility":"hide","supported_in_api":false,"minimal_client_version":"0.144.0","service_tiers":[]},
			{"slug":"gpt-reserve","display_name":"GPT-Reserve","visibility":"hide","supported_in_api":true,"minimal_client_version":"0.144.0","service_tiers":[{"id":"priority"}]},
			{"slug":"gpt-6.2-sol","display_name":"GPT-6.2-Sol","visibility":"list","supported_in_api":true,"minimal_client_version":"0.166.0","service_tiers":[{"id":"priority"},{"id":"ultrafast"}],"tool_mode":"code_mode_only","use_responses_lite":true,"supports_reasoning_effort_updates":true}
		]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 4 {
			t.Fatalf("count = %d, want 4", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.slug").String(); got != "gpt-5.5" {
			t.Fatalf("models[3].slug = %q, want gpt-5.5", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.display_name").String(); got != "GPT-6.2-Sol" {
			t.Fatalf("models[3].display_name = %q, want GPT-6.2-Sol", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.supported_in_api"); got.Type != ojson.True {
			t.Fatalf("models[3].supported_in_api = %v (%s), want true", got.Value(), got.Type)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.service_tiers.0.id").String(); got != "priority" {
			t.Fatalf("models[3].service_tiers[0].id = %q, want priority", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.visibility").String(); got != "list" {
			t.Fatalf("models[3].visibility = %q, want list", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.minimal_client_version").String(); got != "0.124.0" {
			t.Fatalf("models[3].minimal_client_version = %q, want 0.124.0", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.minimal_client_version").String(); got != "0.166.0" {
			t.Fatalf("models[2].minimal_client_version = %q, want 0.166.0", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.tool_mode"); got.Type != ojson.String || got.Str != "direct" {
			t.Fatalf("models[3].tool_mode = %v (%s), want string \"direct\"", got.Value(), got.Type)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.use_responses_lite"); got.Type != ojson.False {
			t.Fatalf("models[3].use_responses_lite = %v (%s), want false", got.Value(), got.Type)
		}
		if got := ojson.GetBytesNoCopy(out, "models.3.supports_reasoning_effort_updates"); got.Type != ojson.False {
			t.Fatalf("models[3].supports_reasoning_effort_updates = %v (%s), want false", got.Value(), got.Type)
		}
	})

	t.Run("prefers visible API-supported model with priority service tier when earlier candidate lacks it", func(t *testing.T) {
		raw := []byte(`{"models":[
			{"slug":"gpt-6.2-plain","display_name":"GPT-6.2-Plain","visibility":"list","supported_in_api":true,"minimal_client_version":"0.166.0","service_tiers":[]},
			{"slug":"gpt-6.2-fast","display_name":"GPT-6.2-Fast","visibility":"list","supported_in_api":true,"minimal_client_version":"0.166.0","service_tiers":[{"id":"priority"}]}
		]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 3 {
			t.Fatalf("count = %d, want 3", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.display_name").String(); got != "GPT-6.2-Fast" {
			t.Fatalf("models[2].display_name = %q, want GPT-6.2-Fast", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.service_tiers.0.id").String(); got != "priority" {
			t.Fatalf("models[2].service_tiers[0].id = %q, want priority", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.minimal_client_version").String(); got != "0.124.0" {
			t.Fatalf("models[2].minimal_client_version = %q, want 0.124.0", got)
		}
	})

	t.Run("falls back to first visible API-supported model when no candidate has priority service tier", func(t *testing.T) {
		raw := []byte(`{"models":[
			{"slug":"gpt-hidden-exp","display_name":"Hidden Exp","visibility":"hide","supported_in_api":false,"service_tiers":[]},
			{"slug":"gpt-6.2-plain","display_name":"GPT-6.2-Plain","visibility":"list","supported_in_api":true,"service_tiers":[]}
		]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 3 {
			t.Fatalf("count = %d, want 3", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.display_name").String(); got != "GPT-6.2-Plain" {
			t.Fatalf("models[2].display_name = %q, want GPT-6.2-Plain", got)
		}
	})

	t.Run("falls back to first visible model when supported_in_api is omitted", func(t *testing.T) {
		raw := []byte(`{"models":[
			{"slug":"gpt-hidden","display_name":"Hidden","visibility":"hide"},
			{"slug":"gpt-visible","display_name":"Visible","visibility":"list"}
		]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 3 {
			t.Fatalf("count = %d, want 3", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.display_name").String(); got != "Visible" {
			t.Fatalf("models[2].display_name = %q, want Visible", got)
		}
	})

	t.Run("falls back to first API-supported model when all models are hidden", func(t *testing.T) {
		raw := []byte(`{"models":[
			{"slug":"gpt-hidden-exp","display_name":"Hidden Exp","visibility":"hide","supported_in_api":false},
			{"slug":"gpt-hidden-api","display_name":"Hidden API","visibility":"hide","supported_in_api":true}
		]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 3 {
			t.Fatalf("count = %d, want 3", count)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.display_name").String(); got != "Hidden API" {
			t.Fatalf("models[2].display_name = %q, want Hidden API", got)
		}
		if got := ojson.GetBytesNoCopy(out, "models.2.visibility").String(); got != "list" {
			t.Fatalf("models[2].visibility = %q, want list", got)
		}
	})
}
