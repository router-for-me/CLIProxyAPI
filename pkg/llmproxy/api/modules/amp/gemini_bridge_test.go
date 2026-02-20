package amp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCreateGeminiBridgeHandler_ActionParameterExtraction(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		path           string
		mappedModel    string // empty string means no mapping
		expectedAction string
	}{
		{
			name:           "no_mapping_uses_url_model",
			path:           "/publishers/google/models/gemini-pro:generateContent",
			mappedModel:    "",
			expectedAction: "gemini-pro:generateContent",
		},
		{
			name:           "mapped_model_replaces_url_model",
			path:           "/publishers/google/models/gemini-exp:generateContent",
			mappedModel:    "gemini-2.0-flash",
			expectedAction: "gemini-2.0-flash:generateContent",
		},
		{
			name:           "mapping_preserves_method",
			path:           "/publishers/google/models/gemini-2.5-preview:streamGenerateContent",
			mappedModel:    "gemini-flash",
			expectedAction: "gemini-flash:streamGenerateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedAction string

			mockGeminiHandler := func(c *gin.Context) {
				capturedAction = c.Param("action")
				c.JSON(http.StatusOK, gin.H{"captured": capturedAction})
			}

			// Use the actual createGeminiBridgeHandler function
			bridgeHandler := createGeminiBridgeHandler(mockGeminiHandler)

			r := gin.New()
			if tt.mappedModel != "" {
				r.Use(func(c *gin.Context) {
					c.Set(MappedModelContextKey, tt.mappedModel)
					c.Next()
				})
			}
			r.POST("/api/provider/google/v1beta1/*path", bridgeHandler)

			req := httptest.NewRequest(http.MethodPost, "/api/provider/google/v1beta1"+tt.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("Expected status 200, got %d", w.Code)
			}
			if capturedAction != tt.expectedAction {
				t.Errorf("Expected action '%s', got '%s'", tt.expectedAction, capturedAction)
			}
		})
	}
}

func TestCreateGeminiBridgeHandler_InvalidPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
	bridgeHandler := createGeminiBridgeHandler(mockHandler)

	r := gin.New()
	r.POST("/api/provider/google/v1beta1/*path", bridgeHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/provider/google/v1beta1/invalid/path", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400 for invalid path, got %d", w.Code)
	}
}
