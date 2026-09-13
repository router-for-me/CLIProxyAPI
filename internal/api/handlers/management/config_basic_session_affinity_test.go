package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func runSessionAffinity(t *testing.T, h *Handler, method, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v0/management/routing/session-affinity", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	if method == http.MethodGet {
		h.GetRoutingSessionAffinity(ctx)
	} else {
		h.PutRoutingSessionAffinity(ctx)
	}
	var out map[string]any
	if rec.Body.Len() > 0 {
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &out); errUnmarshal != nil {
			t.Fatalf("decode response: %v (%s)", errUnmarshal, rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestRoutingSessionAffinityRoundTrip(t *testing.T) {
	cfg := &config.Config{}
	path := writeTestConfigFile(t)
	h := &Handler{cfg: cfg, configFilePath: path}

	// Unset reads as disabled with the runtime default TTL.
	code, got := runSessionAffinity(t, h, http.MethodGet, "")
	if code != http.StatusOK || got["enabled"] != false || got["ttl"] != defaultSessionAffinityTTL {
		t.Fatalf("GET = %d %v, want enabled=false ttl=%q", code, got, defaultSessionAffinityTTL)
	}

	code, _ = runSessionAffinity(t, h, http.MethodPut, `{"enabled":true,"ttl":"30m"}`)
	if code != http.StatusOK {
		t.Fatalf("PUT status = %d", code)
	}
	if !cfg.Routing.SessionAffinity || cfg.Routing.SessionAffinityTTL != "30m" {
		t.Fatalf("config = %+v, want session affinity on with a 30m TTL", cfg.Routing)
	}
	saved, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	if !strings.Contains(string(saved), "session-affinity: true") || !strings.Contains(string(saved), "session-affinity-ttl: 30m") {
		t.Fatalf("persisted config missing the affinity fields:\n%s", saved)
	}

	// Either field alone is accepted; the other keeps its value.
	code, _ = runSessionAffinity(t, h, http.MethodPut, `{"enabled":false}`)
	if code != http.StatusOK || cfg.Routing.SessionAffinity || cfg.Routing.SessionAffinityTTL != "30m" {
		t.Fatalf("PUT enabled only = %d %+v", code, cfg.Routing)
	}
	code, got = runSessionAffinity(t, h, http.MethodGet, "")
	if code != http.StatusOK || got["enabled"] != false || got["ttl"] != "30m" {
		t.Fatalf("GET after PUT = %d %v", code, got)
	}

	// An empty TTL clears the field, so the runtime default applies again.
	code, _ = runSessionAffinity(t, h, http.MethodPut, `{"ttl":""}`)
	if code != http.StatusOK || cfg.Routing.SessionAffinityTTL != "" {
		t.Fatalf("PUT empty ttl = %d %+v", code, cfg.Routing)
	}
}

func TestRoutingSessionAffinityRejectsBadInput(t *testing.T) {
	cfg := &config.Config{Routing: config.RoutingConfig{SessionAffinity: true, SessionAffinityTTL: "1h"}}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	for _, body := range []string{`{}`, `{"ttl":"soon"}`, `{"ttl":"-5m"}`, `{"ttl":"500ms"}`, `{"enabled":"yes"}`, `not json`} {
		code, _ := runSessionAffinity(t, h, http.MethodPut, body)
		if code != http.StatusBadRequest {
			t.Fatalf("PUT %s status = %d, want 400", body, code)
		}
	}
	if !cfg.Routing.SessionAffinity || cfg.Routing.SessionAffinityTTL != "1h" {
		t.Fatalf("rejected writes must not change the config: %+v", cfg.Routing)
	}
}

func TestRoutingSessionAffinityClearedValuesLeaveTheFile(t *testing.T) {
	cfg := &config.Config{Routing: config.RoutingConfig{Strategy: "fill-first"}}
	path := writeTestConfigFile(t)
	h := &Handler{cfg: cfg, configFilePath: path}

	if code, _ := runSessionAffinity(t, h, http.MethodPut, `{"enabled":true,"ttl":"5m"}`); code != http.StatusOK {
		t.Fatalf("PUT enable status = %d", code)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	if !strings.Contains(string(data), "session-affinity: true") || !strings.Contains(string(data), "session-affinity-ttl: 5m") {
		t.Fatalf("enabled values not persisted:\n%s", data)
	}

	// Disabling and clearing must remove the omitempty keys from the YAML,
	// not just the in-memory fields, or a reload brings them back.
	if code, _ := runSessionAffinity(t, h, http.MethodPut, `{"enabled":false,"ttl":""}`); code != http.StatusOK {
		t.Fatalf("PUT clear status = %d", code)
	}
	data, errRead = os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	if strings.Contains(string(data), "session-affinity") {
		t.Fatalf("cleared values still in the file:\n%s", data)
	}
	if !strings.Contains(string(data), "strategy: fill-first") {
		t.Fatalf("strategy must survive the prune:\n%s", data)
	}
}
