package helps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestZCodeRouteResolver_MapsAndFailsOpen(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/configs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"proxyEndpoint": map[string]any{"mapping": map[string]any{
				"https://api.z.ai/api/anthropic": "https://zcode.z.ai/api/v1/ultra-zai/anthropic",
			}},
		}, "msg": ""})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := NewZCodeRouteResolver(nil)
	res.configURL = srv.URL + "/api/v1/agent/configs"
	if err := res.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := res.BaseURL(nil, "/v1/messages")
	if got != "https://zcode.z.ai/api/v1/ultra-zai/anthropic" {
		t.Fatalf("mapped base = %q", got)
	}
}

func TestZCodeRouteResolver_FailsOpenWithoutSnapshot(t *testing.T) {
	res := NewZCodeRouteResolver(nil)
	got := res.BaseURL(nil, "/v1/messages")
	if got != "https://api.z.ai/api/anthropic" {
		t.Fatalf("expected fallback, got %q", got)
	}
}
