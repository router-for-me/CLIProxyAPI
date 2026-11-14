package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestRequestKiroToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 8317}, nil)

	router := gin.New()
	router.GET("/kiro-auth-url", h.RequestKiroToken)

	mockSSOServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/register":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"clientId":"test-client","clientSecret":"test-secret"}`))
		case "/device_authorization":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"deviceCode":"d-code","userCode":"USER-1234","verificationUriComplete":"https://view.awsapps.com/start","expiresIn":900,"interval":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockSSOServer.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/kiro-auth-url", nil)

	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status code: %d", w.Code)
	}

	if w.Code == http.StatusOK {
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp["status"] != "ok" {
			t.Errorf("status = %v, want ok", resp["status"])
		}
	}
}
