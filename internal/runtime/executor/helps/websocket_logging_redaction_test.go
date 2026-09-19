package helps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestWebsocketRequestTimelineRedactsSensitiveData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	sentinels := []string{"secret-query", "secret-auth", "secret-session", "secret-body", "secret-response", "secret-error"}

	RecordAPIWebsocketRequest(ctx, cfg, UpstreamRequestLog{
		URL: "wss://provider.example/ws?token=secret-query", Method: "WEBSOCKET",
		Headers: http.Header{"Authorization": []string{"Bearer secret-auth"}, "X-Session": []string{"secret-session"}},
		Body:    []byte(`{"input":"secret-body"}`), Provider: "xai", AuthID: "secret-auth",
	})
	AppendAPIWebsocketResponse(ctx, cfg, []byte(`{"output":"secret-response"}`))
	RecordAPIWebsocketError(ctx, cfg, "read", errors.New("secret-error"))

	raw, _ := ginCtx.Get(apiWebsocketTimelineKey)
	timeline, _ := raw.([]byte)
	for _, sentinel := range sentinels {
		if strings.Contains(string(timeline), sentinel) {
			t.Fatalf("websocket timeline leaked %q: %s", sentinel, timeline)
		}
	}
	if !strings.Contains(string(timeline), "Details: redacted") || !strings.Contains(string(timeline), "upstream websocket operation failed") {
		t.Fatalf("timeline lacks structural redaction markers: %s", timeline)
	}
}

func TestWebsocketUpgradeRejectionCreatesRedactedRequestAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	const sentinel = "upgrade-rejection-secret"

	RecordAPIWebsocketUpgradeRejection(ctx, cfg, UpstreamRequestLog{
		URL:       "wss://provider.example/ws?token=" + sentinel,
		Method:    "WEBSOCKET",
		Headers:   http.Header{"Authorization": []string{"Bearer " + sentinel}},
		Body:      []byte(`{"input":"` + sentinel + `"}`),
		Provider:  "xai",
		AuthID:    sentinel,
		AuthValue: sentinel,
	}, http.StatusUnauthorized, http.Header{"X-Upstream-ID": []string{sentinel}}, []byte(`{"error":"`+sentinel+`"}`))

	rawRequest, okRequest := ginCtx.Get(apiRequestKey)
	requestLog, _ := rawRequest.([]byte)
	if !okRequest || len(requestLog) == 0 {
		t.Fatal("websocket rejection did not create an API request attempt")
	}
	if strings.Contains(string(requestLog), "<missing>") {
		t.Fatalf("websocket rejection created placeholder request attempt: %s", requestLog)
	}
	if strings.Contains(string(requestLog), sentinel) {
		t.Fatalf("websocket rejection request attempt leaked sentinel: %s", requestLog)
	}
	if !strings.Contains(string(requestLog), "=== API REQUEST 1 ===") || !strings.Contains(string(requestLog), "HTTP Method: GET") {
		t.Fatalf("websocket rejection request attempt lacks structural metadata: %s", requestLog)
	}

	rawResponse, okResponse := ginCtx.Get(apiResponseKey)
	responseLog, _ := rawResponse.([]byte)
	if !okResponse || !strings.Contains(string(responseLog), "Status: 401") {
		t.Fatalf("websocket rejection response was not linked to request attempt: %s", responseLog)
	}
	if strings.Contains(string(responseLog), sentinel) {
		t.Fatalf("websocket rejection response leaked sentinel: %s", responseLog)
	}
}
