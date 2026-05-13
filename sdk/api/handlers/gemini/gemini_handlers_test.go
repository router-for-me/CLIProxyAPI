package gemini

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReader) Close() error             { return nil }

func TestGeminiHandlerUnknownMethodReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})
	router.POST("/v1beta/models/*action", handler.GeminiHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-test:unknownMethod", strings.NewReader(`{}`))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func TestGeminiHandlerBodyReadErrorReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})
	router.POST("/v1beta/models/*action", handler.GeminiHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-test:generateContent", errReader{})
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestGeminiCLIHandlerBodyReadErrorReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewGeminiCLIAPIHandler(&handlers.BaseAPIHandler{
		Cfg: &config.SDKConfig{EnableGeminiCLIEndpoint: true},
	})
	router.POST("/v1internal:method", handler.CLIHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1internal:generateContent", errReader{})
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:34567"
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}
