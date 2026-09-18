package helps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ultraRoutingConfig returns a config with ultra routing explicitly enabled.
func ultraRoutingConfig() *config.Config {
	return &config.Config{ZCode: config.ZCodeConfig{UltraRouting: true}}
}

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

	res := NewZCodeRouteResolver(ultraRoutingConfig())
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
	res := NewZCodeRouteResolver(ultraRoutingConfig())
	got := res.BaseURL(nil, "/v1/messages")
	if got != "https://api.z.ai/api/anthropic" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

// Routing is opt-in: with the default config the resolver must never fetch and
// must always return the pinned endpoint, even when the server would publish a
// mapping.
func TestZCodeRouteResolver_DisabledByDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/configs", func(w http.ResponseWriter, r *http.Request) {
		t.Error("config endpoint must not be fetched when routing is disabled")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"proxyEndpoint": map[string]any{"mapping": map[string]any{
				"https://api.z.ai/api/anthropic": "https://zcode.z.ai/api/v1/ultra-zai/anthropic",
			}},
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for name, cfg := range map[string]*config.Config{"nil": nil, "default": {}} {
		t.Run(name, func(t *testing.T) {
			res := NewZCodeRouteResolver(cfg)
			res.configURL = srv.URL + "/api/v1/agent/configs"
			if err := res.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := res.BaseURL(nil, "/v1/messages"); got != "https://api.z.ai/api/anthropic" {
				t.Fatalf("disabled routing must keep the pinned endpoint, got %q", got)
			}
		})
	}
}
