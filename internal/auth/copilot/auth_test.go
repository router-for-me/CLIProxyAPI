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
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
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

func TestFetchAvailableModelCatalog_ResponsesOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-4.6","supported_endpoints":["/v1/messages","/chat/completions"]},{"id":"mai-code-1-flash-picker","supported_endpoints":["/responses"]}]}`))
	}))
	defer server.Close()

	auth := &Auth{httpClient: server.Client()}
	catalog, err := auth.FetchAvailableModelCatalog(context.Background(), "session-token", server.URL)
	if err != nil {
		t.Fatalf("FetchAvailableModelCatalog() error = %v", err)
	}
	if want := []string{"claude-opus-4.6", "mai-code-1-flash-picker"}; !reflect.DeepEqual(catalog.ModelIDs, want) {
		t.Fatalf("ModelIDs = %#v, want %#v", catalog.ModelIDs, want)
	}
	if want := []string{"mai-code-1-flash-picker"}; !reflect.DeepEqual(catalog.ResponsesOnlyIDs, want) {
		t.Fatalf("ResponsesOnlyIDs = %#v, want %#v", catalog.ResponsesOnlyIDs, want)
	}
}
