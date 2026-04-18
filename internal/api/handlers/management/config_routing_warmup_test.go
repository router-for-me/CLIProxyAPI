package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestRoutingWarmupHandlers(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
	r := gin.New()
	r.GET("/routing/warmup", h.GetRoutingWarmup)
	r.PUT("/routing/warmup", h.PutRoutingWarmup)
	r.PATCH("/routing/warmup", h.PutRoutingWarmup)

	getReq := httptest.NewRequest(http.MethodGet, "/routing/warmup", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getRec.Code, http.StatusOK)
	}
	if strings.TrimSpace(getRec.Body.String()) != `{"warmup":false}` {
		t.Fatalf("GET body = %s", getRec.Body.String())
	}

	putReq := httptest.NewRequest(http.MethodPut, "/routing/warmup", strings.NewReader(`{"value":true}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d; body=%s", putRec.Code, http.StatusOK, putRec.Body.String())
	}
	if !cfg.Routing.Warmup {
		t.Fatal("expected routing.warmup to become true")
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/routing/warmup", strings.NewReader(`{"value":false}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	r.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if cfg.Routing.Warmup {
		t.Fatal("expected routing.warmup to become false")
	}
}
