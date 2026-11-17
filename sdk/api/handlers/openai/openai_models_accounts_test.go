package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type modelsAccountsResponse struct {
	Data []map[string]json.RawMessage `json:"data"`
}

func TestOpenAIModelsListsAccountKindsOnRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	model := "accounts-test-model"
	only := "accounts-test-key-only"
	auths := []*coreauth.Auth{
		{ID: "accounts-test-key", Provider: "claude", Attributes: map[string]string{coreauth.AttributeAPIKey: "secret-key", "priority": "-25"}},
		{ID: "accounts-test-login", Provider: "claude", Label: "someone@example.com", Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth", "priority": "-15"}},
		{ID: "accounts-test-off", Provider: "claude", Disabled: true, Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth", "priority": "-10"}},
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		models := []*registry.ModelInfo{{ID: model, OwnedBy: "anthropic"}}
		if auth.ID == "accounts-test-key" {
			models = append(models, &registry.ModelInfo{ID: only, OwnedBy: "anthropic"})
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(nil, manager))

	get := func(target string) (map[string]map[string]json.RawMessage, string) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
		handler.OpenAIModels(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", target, recorder.Code, recorder.Body.String())
		}
		var body modelsAccountsResponse
		if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("decode: %v", errDecode)
		}
		byID := map[string]map[string]json.RawMessage{}
		for _, entry := range body.Data {
			var id string
			_ = json.Unmarshal(entry["id"], &id)
			byID[id] = entry
		}
		return byID, recorder.Body.String()
	}

	plain, _ := get("/v1/models")
	if _, exists := plain[model]["accounts"]; exists {
		t.Fatal("accounts listed without include=accounts")
	}

	withAccounts, raw := get("/v1/models?include=accounts")
	var accounts []map[string]any
	if errDecode := json.Unmarshal(withAccounts[model]["accounts"], &accounts); errDecode != nil {
		t.Fatalf("decode accounts of %s: %v (%s)", model, errDecode, raw)
	}
	// Highest priority first, the order CPA tries them in; a disabled credential is not listed.
	want := []map[string]any{
		{"provider": "claude", "kind": "oauth", "priority": float64(-15)},
		{"provider": "claude", "kind": "apikey", "priority": float64(-25)},
	}
	if len(accounts) != len(want) {
		t.Fatalf("accounts of %s = %v, want %v", model, accounts, want)
	}
	for i := range want {
		for key, value := range want[i] {
			if accounts[i][key] != value {
				t.Fatalf("accounts[%d][%s] = %v, want %v (all: %v)", i, key, accounts[i][key], value, accounts)
			}
		}
		if len(accounts[i]) != len(want[i]) {
			t.Fatalf("accounts[%d] has extra fields: %v", i, accounts[i])
		}
	}
	var keyOnly []map[string]any
	_ = json.Unmarshal(withAccounts[only]["accounts"], &keyOnly)
	if len(keyOnly) != 1 || keyOnly[0]["kind"] != "apikey" {
		t.Fatalf("accounts of %s = %v, want one api key", only, keyOnly)
	}
	for _, secret := range []string{"secret-key", "someone@example.com", "accounts-test-login"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the model list exposes %q", secret)
		}
	}
}
