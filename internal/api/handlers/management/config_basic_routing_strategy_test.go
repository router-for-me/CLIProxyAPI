package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestNormalizeRoutingStrategy(t *testing.T) {
	tests := []struct {
		input  string
		want   string
		wantOK bool
	}{
		{input: "", want: "round-robin", wantOK: true},
		{input: "rr", want: "round-robin", wantOK: true},
		{input: "fill-first", want: "fill-first", wantOK: true},
		{input: "ff", want: "fill-first", wantOK: true},
		{input: "soonest-reset", want: "soonest-reset", wantOK: true},
		{input: "soonestreset", want: "soonest-reset", wantOK: true},
		{input: "reset-first", want: "soonest-reset", wantOK: true},
		{input: "resetfirst", want: "soonest-reset", wantOK: true},
		{input: " Reset-First ", want: "soonest-reset", wantOK: true},
		{input: "SOONEST-RESET", want: "soonest-reset", wantOK: true},
		{input: "soonest"},
		{input: "reset"},
	}
	for _, tt := range tests {
		got, ok := normalizeRoutingStrategy(tt.input)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("normalizeRoutingStrategy(%q) = %q, %v; want %q, %v", tt.input, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestPutRoutingStrategySoonestReset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, value := range []string{"soonest-reset", "soonestreset", "reset-first", "ResetFirst"} {
		t.Run(value, func(t *testing.T) {
			configFile := writeTestConfigFile(t)
			h := &Handler{cfg: &config.Config{}, configFilePath: configFile}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/routing/strategy", strings.NewReader(`{"value":"`+value+`"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			h.PutRoutingStrategy(ctx)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if h.cfg.Routing.Strategy != "soonest-reset" {
				t.Fatalf("cfg.Routing.Strategy = %q, want soonest-reset", h.cfg.Routing.Strategy)
			}
			saved, errLoad := config.LoadConfig(configFile)
			if errLoad != nil {
				t.Fatalf("LoadConfig() error = %v", errLoad)
			}
			if saved.Routing.Strategy != "soonest-reset" {
				t.Fatalf("persisted routing.strategy = %q, want soonest-reset", saved.Routing.Strategy)
			}

			getRec := httptest.NewRecorder()
			getCtx, _ := gin.CreateTestContext(getRec)
			getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/routing/strategy", nil)
			h.GetRoutingStrategy(getCtx)
			if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"strategy":"soonest-reset"`) {
				t.Fatalf("GET status = %d body = %s, want soonest-reset", getRec.Code, getRec.Body.String())
			}
		})
	}
}

func TestConfigV8PutRoutingStrategySoonestReset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("config-version: 8\nrouting: {strategy: fill-first, session-affinity: true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: file}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v8/management/config/routing/strategy", strings.NewReader(`"soonest-reset"`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	saved, err := config.LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Routing.Strategy != "soonest-reset" || !saved.Routing.SessionAffinity {
		t.Fatalf("persisted routing = %+v, want soonest-reset with session affinity kept", saved.Routing)
	}
}
