package amp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

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
