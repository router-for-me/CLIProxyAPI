package apikeyusage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestMiddlewarePreservesBodiesItDoesNotParse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newTestService(t, config.APIKeyProfile{
		ID: "alice", Name: "Alice", APIKey: testManagedKey,
	})

	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "binary", contentType: "image/png", body: []byte{0x89, 'P', 'N', 'G', 0x00, 0x01}},
		{name: "malformed json", contentType: "application/json", body: []byte(`{"model":`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var received []byte
			engine := gin.New()
			engine.POST("/v1/images/edits", func(c *gin.Context) {
				c.Set("userApiKey", testManagedKey)
				c.Next()
			}, Middleware(service), func(c *gin.Context) {
				received, _ = io.ReadAll(c.Request.Body)
				c.Status(http.StatusOK)
			})

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(tt.body))
			request.Header.Set("Content-Type", tt.contentType)
			engine.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if !bytes.Equal(received, tt.body) {
				t.Fatalf("received body = %q, want %q", received, tt.body)
			}
		})
	}

	summary, err := service.SummaryForPeriod(context.Background(), "week", time.Now())
	if err != nil {
		t.Fatalf("SummaryForPeriod() error = %v", err)
	}
	if len(summary.Profiles) != 1 || summary.Profiles[0].Usage.Requests != 1 {
		t.Fatalf("profile usage = %#v; only the valid non-JSON request should be reserved", summary.Profiles)
	}
}
