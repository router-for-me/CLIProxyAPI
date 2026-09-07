package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestRefreshUpstreamProviderModels_MergesNewModels verifies the live
// fetch adds only unknown model ids, leaving seed entries untouched.
func TestRefreshUpstreamProviderModels_MergesNewModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// base_url is the stub root; the handler appends /models (the
		// production row's base_url already ends in /v1).
		if r.URL.Path != "/models" {
			t.Errorf("upstream path = %s, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-refresh" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"new-model-a"},{"id":"glm-5.2"},{"id":""}]}`))
	}))
	defer upstream.Close()

	st := newFakeUpstreamProviderStore()
	created, errCreate := st.Create(context.Background(), store.UpstreamProvider{
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      upstream.URL,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{APIKey: "sk-refresh", Name: "main"},
		},
		Models: []store.UpstreamProviderModel{
			{Name: "glm-5.2"},
		},
	})
	if errCreate != nil {
		t.Fatalf("Create: %v", errCreate)
	}
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, created.ID, "refresh-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"added":1`) {
		t.Fatalf("body = %s, want added:1", rec.Body.String())
	}
	row, errGet := st.Get(context.Background(), created.ID)
	if errGet != nil {
		t.Fatalf("Get: %v", errGet)
	}
	if len(row.Models) != 2 {
		t.Fatalf("model count = %d, want 2", len(row.Models))
	}
	if row.Models[1].Name != "new-model-a" {
		t.Fatalf("second model = %q, want new-model-a", row.Models[1].Name)
	}
}

func TestRefreshUpstreamProviderModels_PinnedEntry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-second" {
			t.Errorf("Authorization = %q, want pinned entry key", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	st := newFakeUpstreamProviderStore()
	created, _ := st.Create(context.Background(), store.UpstreamProvider{
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      upstream.URL,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{APIKey: "sk-first", Name: "first"},
			{APIKey: "sk-second", Name: "second"},
		},
	})
	h := newSeedModelsHandler(t, st)
	secondID := created.APIKeyEntries[1].ID
	rec := postProviderAction(t, h, created.ID, "refresh-models", fmt.Sprintf(`{"entry_id":%d}`, secondID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRefreshUpstreamProviderModels_UpstreamErrorLeavesCatalog(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusUnauthorized)
	}))
	defer upstream.Close()

	st := newFakeUpstreamProviderStore()
	created, _ := st.Create(context.Background(), store.UpstreamProvider{
		ProviderType:  "opencode-go",
		Name:          "ocgo",
		BaseURL:       upstream.URL,
		APIKeyEntries: []store.UpstreamProviderAPIKey{{APIKey: "sk-1", Name: "main"}},
		Models:        []store.UpstreamProviderModel{{Name: "glm-5.2"}},
	})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, created.ID, "refresh-models", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	row, _ := st.Get(context.Background(), created.ID)
	if len(row.Models) != 1 {
		t.Fatalf("catalog mutated on upstream error: %d models", len(row.Models))
	}
}

func TestRefreshUpstreamProviderModels_NoKey(t *testing.T) {
	st := newFakeUpstreamProviderStore()
	created, _ := st.Create(context.Background(), store.UpstreamProvider{
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      "https://opencode.ai/zen/go/v1",
	})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, created.ID, "refresh-models", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
