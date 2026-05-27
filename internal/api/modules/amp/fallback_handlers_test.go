package amp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestFallbackHandler_ModelMapping_RemapReasoningEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-amp-effort", "codex", []*registry.ModelInfo{
		{ID: "gpt-5.5", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("test-client-amp-effort")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{
			From: "claude-opus-4-7",
			To:   "gpt-5.5",
			ReasoningEffortMappings: map[string]string{
				"high":  "low",
				"xhigh": "medium",
				"max":   "high",
			},
		},
	})

	fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy { return nil }, mapper, func() bool { return true })

	handler := func(c *gin.Context) {
		var req struct {
			Model        string `json:"model"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"model":  req.Model,
			"effort": req.OutputConfig.Effort,
		})
	}

	r := gin.New()
	r.POST("/v1/messages", fallback.WrapHandler(handler))

	cases := []struct {
		name       string
		inEffort   string
		wantEffort string
	}{
		{name: "high to low", inEffort: "high", wantEffort: "low"},
		{name: "xhigh to medium", inEffort: "xhigh", wantEffort: "medium"},
		{name: "max to high", inEffort: "max", wantEffort: "high"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqBody := []byte(`{"model":"claude-opus-4-7","output_config":{"effort":"` + tc.inEffort + `"}}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
			}

			var resp struct {
				Model  string `json:"model"`
				Effort string `json:"effort"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("Failed to parse response JSON: %v", err)
			}
			if resp.Model != "claude-opus-4-7" {
				t.Errorf("Expected response model claude-opus-4-7, got %s", resp.Model)
			}
			if resp.Effort != tc.wantEffort {
				t.Errorf("Expected handler to see remapped effort %s, got %s", tc.wantEffort, resp.Effort)
			}
		})
	}
}

func TestFallbackHandler_ModelMapping_PreservesThinkingSuffixAndRewritesResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-amp-fallback", "codex", []*registry.ModelInfo{
		{ID: "test/gpt-5.2", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("test-client-amp-fallback")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gpt-5.2", To: "test/gpt-5.2"},
	})

	fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy { return nil }, mapper, nil)

	handler := func(c *gin.Context) {
		var req struct {
			Model string `json:"model"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"model":      req.Model,
			"seen_model": req.Model,
		})
	}

	r := gin.New()
	r.POST("/chat/completions", fallback.WrapHandler(handler))

	reqBody := []byte(`{"model":"gpt-5.2(xhigh)"}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	var resp struct {
		Model     string `json:"model"`
		SeenModel string `json:"seen_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}

	if resp.Model != "gpt-5.2(xhigh)" {
		t.Errorf("Expected response model gpt-5.2(xhigh), got %s", resp.Model)
	}
	if resp.SeenModel != "test/gpt-5.2(xhigh)" {
		t.Errorf("Expected handler to see test/gpt-5.2(xhigh), got %s", resp.SeenModel)
	}
}
