package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestListRoutingModelsOmitsClientIDs(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clients := map[string]string{
		"secret-claude-auth.json": "claude",
		"secret-devin-auth.json":  "devin",
		"secret-compat-key":       "openai-compatible-acme",
	}
	t.Cleanup(func() {
		for clientID := range clients {
			reg.UnregisterClient(clientID)
		}
	})
	reg.RegisterClient("secret-claude-auth.json", "claude", []*registry.ModelInfo{{ID: "routing-test-shared", OwnedBy: "anthropic"}})
	reg.RegisterClient("secret-devin-auth.json", "devin", []*registry.ModelInfo{{ID: "routing-test-shared", OwnedBy: "anthropic"}})
	reg.RegisterClient("secret-compat-key", "openai-compatible-acme", []*registry.ModelInfo{{ID: "routing-test-compat", OwnedBy: "openai-compatible-acme"}})

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/routing/models", nil)

	h := &Handler{}
	h.ListRoutingModels(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret-") {
		t.Fatalf("response leaks client ids: %s", body)
	}

	var payload struct {
		Models []struct {
			ID        string `json:"id"`
			OwnedBy   string `json:"owned_by"`
			Providers []struct {
				Provider string `json:"provider"`
				Clients  int    `json:"clients"`
			} `json:"providers"`
		} `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}

	found := map[string]string{}
	for _, model := range payload.Models {
		if !strings.HasPrefix(model.ID, "routing-test-") {
			continue
		}
		parts := make([]string, 0, len(model.Providers))
		for _, p := range model.Providers {
			parts = append(parts, p.Provider+"="+strconv.Itoa(p.Clients))
		}
		found[model.ID] = model.OwnedBy + ":" + strings.Join(parts, ",")
	}
	want := map[string]string{
		"routing-test-compat": "openai-compatible-acme:openai-compatible-acme=1",
		"routing-test-shared": "anthropic:claude=1,devin=1",
	}
	if len(found) != len(want) {
		t.Fatalf("models = %v, want %v", found, want)
	}
	for id, w := range want {
		if found[id] != w {
			t.Fatalf("model %s = %q, want %q", id, found[id], w)
		}
	}
}
