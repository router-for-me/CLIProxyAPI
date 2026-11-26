package registry

import (
	"fmt"
	"strings"
	"testing"
)

func TestGetFirstAvailableModel_GeminiHandler(t *testing.T) {
	t.Run("with models prefix and newest first", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("client-1", "gemini", []*ModelInfo{
			{
				ID:      "gemini-2.5-pro",
				Name:    "models/gemini-2.5-pro",
				Created: 100,
			},
			{
				ID:      "gemini-z-newest",
				Name:    "models/gemini-z-newest",
				Created: 200,
			},
		})

		got, err := r.GetFirstAvailableModel("gemini")
		if err != nil {
			t.Fatalf("GetFirstAvailableModel(%q) unexpected error: %v", "gemini", err)
		}
		if got != "gemini-z-newest" {
			t.Fatalf("GetFirstAvailableModel(%q) = %q, want %q", "gemini", got, "gemini-z-newest")
		}

		// When the newest model is quota-exceeded, fall back to the next available model.
		r.SetModelQuotaExceeded("client-1", "gemini-z-newest")
		gotFallback, errFallback := r.GetFirstAvailableModel("gemini")
		if errFallback != nil {
			t.Fatalf("GetFirstAvailableModel(%q) fallback unexpected error: %v", "gemini", errFallback)
		}
		if gotFallback != "gemini-2.5-pro" {
			t.Fatalf("GetFirstAvailableModel(%q) fallback = %q, want %q", "gemini", gotFallback, "gemini-2.5-pro")
		}
	})

	t.Run("without Name field", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("client-1", "gemini", []*ModelInfo{
			{
				ID:      "gemini-2.5-flash",
				Created: 100,
			},
		})

		got, err := r.GetFirstAvailableModel("gemini")
		if err != nil {
			t.Fatalf("GetFirstAvailableModel(%q) unexpected error: %v", "gemini", err)
		}
		if got != "gemini-2.5-flash" {
			t.Fatalf("GetFirstAvailableModel(%q) = %q, want %q", "gemini", got, "gemini-2.5-flash")
		}
	})

	t.Run("all clients quota exceeded returns error", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("client-1", "gemini", []*ModelInfo{
			{
				ID:   "gemini-2.5-pro",
				Name: "models/gemini-2.5-pro",
			},
		})
		r.SetModelQuotaExceeded("client-1", "gemini-2.5-pro")

		_, err := r.GetFirstAvailableModel("gemini")
		if err == nil {
			t.Fatalf("expected error when all clients are quota-exceeded, got nil")
		}
		if !strings.Contains(err.Error(), "no available clients for any model in handler type: gemini") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestGetFirstAvailableModel_MixedCreatedStrictWeakOrdering(t *testing.T) {
	t.Run("mixed created and missing created selects newest deterministically", func(t *testing.T) {
		handlerTypes := []string{"", "openai", "claude", "gemini"}
		for _, handlerType := range handlerTypes {
			for iter := 0; iter < 50; iter++ {
				r := newTestModelRegistry()
				models := []*ModelInfo{
					{ID: "model-old", Created: 100},
					{ID: "model-mid", Created: 200},
					{ID: "model-z-newest", Created: 300},
				}
				for i := 0; i < 15; i++ {
					models = append(models, &ModelInfo{
						ID:      fmt.Sprintf("model-no-created-%02d", i),
						Created: 0,
					})
				}
				r.RegisterClient("client-1", "openai", models)

				got, err := r.GetFirstAvailableModel(handlerType)
				if err != nil {
					t.Fatalf("handlerType=%q iter=%d unexpected error: %v", handlerType, iter, err)
				}
				if got != "model-z-newest" {
					t.Fatalf("handlerType=%q iter=%d got %q, want %q", handlerType, iter, got, "model-z-newest")
				}
			}
		}
	})

	t.Run("equal created timestamps tie-break lexicographically by ID", func(t *testing.T) {
		for iter := 0; iter < 50; iter++ {
			r := newTestModelRegistry()
			r.RegisterClient("client-1", "openai", []*ModelInfo{
				{ID: "model-c", Created: 500},
				{ID: "model-a", Created: 500},
				{ID: "model-b", Created: 500},
				{ID: "model-older", Created: 100},
				{ID: "model-0-no-created", Created: 0},
			})

			got, err := r.GetFirstAvailableModel("openai")
			if err != nil {
				t.Fatalf("iter=%d unexpected error: %v", iter, err)
			}
			if got != "model-a" {
				t.Fatalf("iter=%d got %q, want %q", iter, got, "model-a")
			}
		}
	})

	t.Run("negative and zero created timestamps preserve non-zero ordering", func(t *testing.T) {
		for iter := 0; iter < 50; iter++ {
			r := newTestModelRegistry()
			r.RegisterClient("client-1", "openai", []*ModelInfo{
				{ID: "model-a", Created: -2},
				{ID: "model-z", Created: -1},
				{ID: "model-0-unset", Created: 0},
			})

			got, err := r.GetFirstAvailableModel("")
			if err != nil {
				t.Fatalf("iter=%d unexpected error: %v", iter, err)
			}
			if got != "model-z" {
				t.Fatalf("iter=%d got %q, want %q", iter, got, "model-z")
			}
		}
	})

	t.Run("all missing created timestamps tie-break lexicographically by ID", func(t *testing.T) {
		for iter := 0; iter < 50; iter++ {
			r := newTestModelRegistry()
			r.RegisterClient("client-1", "openai", []*ModelInfo{
				{ID: "model-c"},
				{ID: "model-a"},
				{ID: "model-b"},
			})

			got, err := r.GetFirstAvailableModel("")
			if err != nil {
				t.Fatalf("iter=%d unexpected error: %v", iter, err)
			}
			if got != "model-a" {
				t.Fatalf("iter=%d got %q, want %q", iter, got, "model-a")
			}
		}
	})
}
