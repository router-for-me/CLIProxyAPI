package registry

import (
	"fmt"
	"testing"
)

func TestGetFirstAvailableModel_GeminiHandler(t *testing.T) {
	t.Run("strips models prefix from gemini name field", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("gemini-client-1", "gemini", []*ModelInfo{
			{
				ID:          "gemini-2.5-pro",
				Name:        "models/gemini-2.5-pro",
				DisplayName: "Gemini 2.5 Pro",
				Type:        "gemini",
			},
		})

		got, err := r.GetFirstAvailableModel("gemini")
		if err != nil {
			t.Fatalf("GetFirstAvailableModel(gemini) returned error: %v", err)
		}
		if got != "gemini-2.5-pro" {
			t.Fatalf("GetFirstAvailableModel(gemini) = %q, want %q", got, "gemini-2.5-pro")
		}
	})

	t.Run("supports gemini model without Name field", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("gemini-client-2", "gemini", []*ModelInfo{
			{
				ID:          "gemini-2.5-flash",
				DisplayName: "Gemini 2.5 Flash",
				Type:        "gemini",
			},
		})

		got, err := r.GetFirstAvailableModel("gemini")
		if err != nil {
			t.Fatalf("GetFirstAvailableModel(gemini) returned error: %v", err)
		}
		if got != "gemini-2.5-flash" {
			t.Fatalf("GetFirstAvailableModel(gemini) = %q, want %q", got, "gemini-2.5-flash")
		}
	})

	t.Run("uses registered ID when gemini Name differs from ID", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("gemini-client-3", "gemini", []*ModelInfo{
			{
				ID:          "custom-gemini",
				Name:        "models/gemini-2.5-pro",
				DisplayName: "Custom Gemini",
				Type:        "gemini",
			},
		})

		got, err := r.GetFirstAvailableModel("gemini")
		if err != nil {
			t.Fatalf("GetFirstAvailableModel(gemini) returned error: %v", err)
		}
		if got != "custom-gemini" {
			t.Fatalf("GetFirstAvailableModel(gemini) = %q, want %q", got, "custom-gemini")
		}
	})

	t.Run("preserves creation-time ordering for gemini models", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("gemini-client-4", "gemini", []*ModelInfo{
			{
				ID:      "alpha-gemini-old",
				Name:    "models/gemini-2.0-flash",
				Created: 1000,
				Type:    "gemini",
			},
			{
				ID:      "zeta-gemini-newest",
				Name:    "models/gemini-2.5-pro",
				Created: 3000,
				Type:    "gemini",
			},
			{
				ID:      "mid-gemini-no-created",
				Name:    "models/gemini-2.0-pro",
				Created: 0,
				Type:    "gemini",
			},
		})

		got, err := r.GetFirstAvailableModel("gemini")
		if err != nil {
			t.Fatalf("GetFirstAvailableModel(gemini) returned error: %v", err)
		}
		if got != "zeta-gemini-newest" {
			t.Fatalf("GetFirstAvailableModel(gemini) = %q, want %q", got, "zeta-gemini-newest")
		}
	})
}

func TestGetFirstAvailableModel_MixedCreatedStrictWeakOrdering(t *testing.T) {
	t.Run("newest created model wins over models without created timestamp", func(t *testing.T) {
		for iter := 0; iter < 20; iter++ {
			r := newTestModelRegistry()
			models := make([]*ModelInfo, 0, 20)
			for i := 0; i < 15; i++ {
				models = append(models, &ModelInfo{
					ID:      fmt.Sprintf("model-no-created-%02d", i),
					Created: 0,
					Type:    "openai",
				})
			}
			models = append(models,
				&ModelInfo{ID: "model-old", Created: 1000, Type: "openai"},
				&ModelInfo{ID: "model-mid", Created: 2000, Type: "openai"},
				&ModelInfo{ID: "model-newest", Created: 3000, Type: "openai"},
			)
			r.RegisterClient("openai-client-1", "openai", models)

			got, err := r.GetFirstAvailableModel("openai")
			if err != nil {
				t.Fatalf("iter %d: GetFirstAvailableModel(openai) returned error: %v", iter, err)
			}
			if got != "model-newest" {
				t.Fatalf("iter %d: GetFirstAvailableModel(openai) = %q, want %q", iter, got, "model-newest")
			}
		}
	})

	t.Run("deterministic tie break when created timestamps are equal or absent", func(t *testing.T) {
		for iter := 0; iter < 10; iter++ {
			r := newTestModelRegistry()
			r.RegisterClient("openai-client-2", "openai", []*ModelInfo{
				{ID: "zeta-model", Created: 0, Type: "openai"},
				{ID: "alpha-model", Created: 0, Type: "openai"},
				{ID: "mid-model", Created: 0, Type: "openai"},
			})

			got, err := r.GetFirstAvailableModel("openai")
			if err != nil {
				t.Fatalf("iter %d: GetFirstAvailableModel(openai) returned error: %v", iter, err)
			}
			if got != "alpha-model" {
				t.Fatalf("iter %d: GetFirstAvailableModel(openai) = %q, want %q", iter, got, "alpha-model")
			}
		}
	})
}
