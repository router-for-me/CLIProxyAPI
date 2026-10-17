package openai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesHTTPContinuationCannotSwitchToPlugin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			host := &responsesPluginRouteHost{}
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
			base.SetModelRouterHost(host)
			h := NewOpenAIResponsesAPIHandler(base)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"plugin/model","previous_response_id":"saved","input":"delta","stream":%t}`, stream)))
			c.Set("userApiKey", "principal")
			if !h.httpContinuations.save(responsesHTTPCallerScope(c), "saved", responsesHTTPContinuation{Input: []byte(`[]`), Output: []byte(`[]`), AuthID: "bound-account", Model: "model"}) {
				t.Fatal("save")
			}
			h.Responses(c)
			if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Header().Get("Content-Type"), "event-stream") {
				t.Fatalf("response=%d %s", recorder.Code, recorder.Body)
			}
			if host.executeCalls != 0 || host.streamCalls != 0 {
				t.Fatalf("unpinned executor invoked: execute=%d stream=%d", host.executeCalls, host.streamCalls)
			}
		})
	}
}

func TestResponsesHTTPUncredentialedPluginDoesNotStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	host := &responsesPluginRouteHost{execPayload: []byte(`{"id":"plugin-response","status":"completed","output":[]}`)}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	base.SetModelRouterHost(host)
	h := NewOpenAIResponsesAPIHandler(base)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"plugin/model","input":"hello"}`))
	h.Responses(c)
	if recorder.Code != 200 || gjson.GetBytes(recorder.Body.Bytes(), "store").Bool() {
		t.Fatalf("plugin storage=%d %s", recorder.Code, recorder.Body)
	}
	if _, ok := h.httpContinuations.load(responsesHTTPCallerScope(c), "plugin-response"); ok {
		t.Fatal("uncredentialed response stored")
	}
}
