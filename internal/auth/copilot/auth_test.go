package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFetchAvailableModels_DataArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" || got == "Bearer " {
			t.Fatalf("Authorization header is missing bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4-mini"},{"id":"gpt-5.6-sol"}]}`))
	}))
	defer server.Close()

	auth := &Auth{httpClient: server.Client()}
	models, err := auth.FetchAvailableModels(context.Background(), "session-token", server.URL)
	if err != nil {
		t.Fatalf("FetchAvailableModels() error = %v", err)
	}
	want := []string{"gpt-5.4-mini", "gpt-5.6-sol"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("FetchAvailableModels() = %#v, want %#v", models, want)
	}
}

func TestFetchAvailableModels_ModelsMap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gpt-5.5":{},"gpt-5.6-terra":{}}}`))
	}))
	defer server.Close()

	auth := &Auth{httpClient: server.Client()}
	models, err := auth.FetchAvailableModels(context.Background(), "session-token", server.URL)
	if err != nil {
		t.Fatalf("FetchAvailableModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("FetchAvailableModels() len = %d, want 2", len(models))
	}
	if (models[0] != "gpt-5.5" && models[0] != "gpt-5.6-terra") ||
		(models[1] != "gpt-5.5" && models[1] != "gpt-5.6-terra") ||
		models[0] == models[1] {
		t.Fatalf("FetchAvailableModels() unexpected models %#v", models)
	}
}
