package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchModelsCatalog(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.URL.Query().Get("client_version"); got != "0.144.1" {
			t.Errorf("client_version = %q, want 0.144.1", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "account-1" {
			t.Errorf("Chatgpt-Account-Id = %q", got)
		}
		if got := r.Header.Get("Originator"); got != "test-origin" {
			t.Errorf("Originator = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "test-agent" {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("X-Test"); got != "custom" {
			t.Errorf("X-Test = %q", got)
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":" gpt-5.6-sol ","supported_reasoning_levels":[{"effort":"HIGH"},{"effort":"high"}]},{"slug":"GPT-5.6-SOL"},{"slug":"gpt-5.6-luna"}]}`))
	}))
	defer server.Close()

	catalog, err := FetchModelsCatalog(context.Background(), server.Client(), ModelsRequest{
		BaseURL:       server.URL,
		ClientVersion: " 0.144.1 ",
		AccessToken:   "access-token",
		AccountID:     "account-1",
		Originator:    "test-origin",
		UserAgent:     "test-agent",
		Headers:       http.Header{"X-Test": []string{"custom"}},
	})
	if err != nil {
		t.Fatalf("FetchModelsCatalog() error = %v", err)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(catalog.Models))
	}
	if catalog.Models[0].Slug != "gpt-5.6-sol" {
		t.Fatalf("first slug = %q", catalog.Models[0].Slug)
	}
	if got := catalog.Models[0].SupportedReasoningLevels; len(got) != 1 || got[0].Effort != "high" {
		t.Fatalf("reasoning levels = %#v", got)
	}
}

func TestFetchModelsCatalogRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"models":`},
		{name: "empty", body: `{"models":[]}`},
		{name: "missing slug", body: `{"models":[{"display_name":"missing"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := FetchModelsCatalog(context.Background(), server.Client(), ModelsRequest{
				BaseURL:     server.URL,
				AccessToken: "access-token",
			})
			if err == nil {
				t.Fatal("FetchModelsCatalog() error = nil, want error")
			}
		})
	}
}

func TestFetchModelsCatalogRejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxModelsCatalogSize+1)))
	}))
	defer server.Close()

	_, err := FetchModelsCatalog(context.Background(), server.Client(), ModelsRequest{
		BaseURL:     server.URL,
		AccessToken: "access-token",
	})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("FetchModelsCatalog() error = %v, want size error", err)
	}
}

func TestModelsURL(t *testing.T) {
	t.Parallel()

	got, err := ModelsURL("https://example.com/backend-api/codex/", " 0.144.1 ")
	if err != nil {
		t.Fatalf("ModelsURL() error = %v", err)
	}
	if want := "https://example.com/backend-api/codex/models?client_version=0.144.1"; got != want {
		t.Fatalf("ModelsURL() = %q, want %q", got, want)
	}
}
