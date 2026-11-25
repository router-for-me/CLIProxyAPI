package gemini

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestGeminiHandlerUnsupportedMethodReturnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	cases := []struct {
		name   string
		path   string
		method string
	}{
		{
			name:   "embedContent",
			path:   "/v1beta/models/gemini-2.5-pro:embedContent",
			method: "embedContent",
		},
		{
			name:   "typo method",
			path:   "/v1beta/models/gemini-2.5-pro:generateContnt",
			method: "generateContnt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected status %d, got %d (body=%q)", http.StatusNotFound, rec.Code, rec.Body.String())
			}

			var resp handlers.ErrorResponse
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &resp); errUnmarshal != nil {
				t.Fatalf("expected valid JSON error body, got %q: %v", rec.Body.String(), errUnmarshal)
			}
			if resp.Error.Type != "invalid_request_error" {
				t.Fatalf("expected error.type %q, got %q", "invalid_request_error", resp.Error.Type)
			}
			if !strings.Contains(resp.Error.Message, tc.method) {
				t.Fatalf("expected error.message to mention %q, got %q", tc.method, resp.Error.Message)
			}
		})
	}
}
