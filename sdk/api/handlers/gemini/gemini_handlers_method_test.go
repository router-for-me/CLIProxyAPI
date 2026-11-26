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

	tests := []struct {
		name        string
		path        string
		wantMessage string
	}{
		{
			name:        "unsupported embedContent method",
			path:        "/v1beta/models/gemini-2.5-pro:embedContent",
			wantMessage: "Unsupported method: embedContent",
		},
		{
			name:        "misspelled method name",
			path:        "/v1beta/models/gemini-2.5-pro:generateContnt",
			wantMessage: "Unsupported method: generateContnt",
		},
		{
			name:        "empty method after colon",
			path:        "/v1beta/models/gemini-2.5-pro:",
			wantMessage: "/v1beta/models/gemini-2.5-pro: not found.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected status %d, got %d (body=%q)", http.StatusNotFound, rec.Code, rec.Body.String())
			}

			var errResp handlers.ErrorResponse
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &errResp); errUnmarshal != nil {
				t.Fatalf("expected valid JSON error response, unmarshal error: %v (body=%q)", errUnmarshal, rec.Body.String())
			}

			if errResp.Error.Type != "invalid_request_error" {
				t.Errorf("expected error.type %q, got %q", "invalid_request_error", errResp.Error.Type)
			}
			if errResp.Error.Message != tc.wantMessage {
				t.Errorf("expected error.message %q, got %q", tc.wantMessage, errResp.Error.Message)
			}
		})
	}
}
