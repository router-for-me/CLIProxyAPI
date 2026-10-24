package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestClientKeyModelAccess(t *testing.T) {
	h := NewBaseAPIHandlers(&config.SDKConfig{APIKeyModels: map[string][]string{"restricted": {"public-model"}, "blocked": {}}}, nil)
	for _, tc := range []struct {
		key, model string
		allowed    bool
	}{
		{"unrestricted", "anything", true}, {"restricted", "public-model", true},
		{"restricted", "other", false}, {"restricted", "provider/public-model", false},
		{"restricted", "public-model(high)", false}, {"blocked", "public-model", false},
	} {
		ctx := usage.WithAPIKey(context.Background(), tc.key)
		if got := h.ModelAllowed(ctx, tc.model); got != tc.allowed {
			t.Errorf("ModelAllowed(%q, %q) = %v", tc.key, tc.model, got)
		}
		if !tc.allowed {
			_, _, errMsg := h.ExecuteWithAuthManager(ctx, "openai", tc.model, []byte(`{}`), "")
			if errMsg == nil || errMsg.StatusCode != 403 {
				t.Fatalf("expected forbidden, got %v", errMsg)
			}
		}
	}
	ctx := usage.WithAPIKey(context.Background(), "restricted")
	for _, field := range []string{"data", "models"} {
		body := h.filterModelListBody(ctx, []byte(`{"`+field+`":[{"id":"public-model"},{"id":"other"}]}`))
		var result map[string][]map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if len(result[field]) != 1 {
			t.Fatalf("unfiltered catalog: %s", body)
		}
	}
}
