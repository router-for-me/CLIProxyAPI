package main

import (
	"testing"

	"github.com/tidwall/gjson"
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
		raw := []byte(`{"models":[{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","visibility":"list"},{"slug":"gpt-5.6-sol","visibility":"list"}]}`)
		out, count, errEnsure := ensureBaselineModel(raw)
		if errEnsure != nil {
			t.Fatalf("ensureBaselineModel: %v", errEnsure)
		}
		if count != 3 {
			t.Fatalf("count = %d, want 3", count)
		}
		if got := gjson.GetBytes(out, "models.0.slug").String(); got != "gpt-6.1-sol" {
			t.Fatalf("models[0].slug = %q, want gpt-6.1-sol", got)
		}
		if got := gjson.GetBytes(out, "models.2.slug").String(); got != "gpt-5.5" {
			t.Fatalf("models[2].slug = %q, want gpt-5.5", got)
		}
		if got := gjson.GetBytes(out, "models.2.display_name").String(); got != "GPT-6.1-Sol" {
			t.Fatalf("models[2].display_name = %q, want GPT-6.1-Sol", got)
		}
		if got := gjson.GetBytes(out, "models.2.visibility").String(); got != "list" {
			t.Fatalf("models[2].visibility = %q, want list", got)
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
		if got := gjson.GetBytes(out, "models.0.visibility").String(); got != "hide" {
			t.Fatalf("models[0].visibility = %q, want hide", got)
		}
		if got := gjson.GetBytes(out, "models.1.slug").String(); got != "gpt-5.5" {
			t.Fatalf("models[1].slug = %q, want gpt-5.5", got)
		}
		if got := gjson.GetBytes(out, "models.1.visibility").String(); got != "list" {
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
		if got := gjson.GetBytes(out, "models.1.slug").String(); got != "gpt-5.5" {
			t.Fatalf("models[1].slug = %q, want gpt-5.5", got)
		}
		if got := gjson.GetBytes(out, "models.1.visibility").String(); got != "list" {
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
			vis := gjson.GetBytes(out, "models.0.visibility")
			if vis.Type != gjson.String || vis.Str != "list" {
				t.Fatalf("models[0].visibility = %v (%s), want string \"list\"", vis.Value(), vis.Type)
			}
		}
	})
}
