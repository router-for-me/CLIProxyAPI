package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestWriteModelListResponse_ClaudePolicyUsesRoutingIDs(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(strconv.FormatBool(replacement), func(t *testing.T) {
			handler := &BaseAPIHandler{}
			payload := claudemodels.BuildResponse([]map[string]any{{"id": "gpt-4o"}}, false)
			if replacement {
				handler.SetPluginHost(&handlerInterceptorTestHost{
					interceptResponse: func(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
						// A plugin must not restore a denied model, even under its cloaked ID.
						return pluginapi.ResponseInterceptResponse{Body: []byte(`{"data":[{"id":"claude-fable-5-dd-terces-tpg"},{"id":"claude-fable-5-dd-o4-tpg"}],"first_id":"claude-fable-5-dd-terces-tpg","last_id":"claude-fable-5-dd-o4-tpg"}`)}
					},
				})
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			ctx.Set(sdkaccess.ModelPolicyContextKey, sdkaccess.NewModelPolicy([]string{"gpt-*"}, []string{"gpt-secret"}))
			handler.WriteModelListResponse(ctx, "claude", payload)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			body := ojson.ParseBytes(recorder.Body.Bytes())
			models := body.Get("data").Array()
			wantID := claudemodels.EnsureClaudeModelIDPrefix("gpt-4o")
			if len(models) != 1 || models[0].Get("id").String() != wantID {
				t.Fatalf("models = %s, want only %s", body.Get("data").Raw, wantID)
			}
			if body.Get("first_id").String() != wantID || body.Get("last_id").String() != wantID {
				t.Fatalf("catalog boundaries do not match filtered models: %s", recorder.Body.String())
			}
		})
	}
}

func TestWriteModelListResponse_FilterDropsInterceptorContentLength(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		header string
		status int
	}{
		{"filtered", `{"data":[{"id":"gpt-4o"},{"id":"denied"}]}`, "Content-Length", http.StatusOK},
		{"lowercase", `{"data":[{"id":"gpt-4o"},{"id":"denied"}]}`, "content-length", http.StatusOK},
		{"invalid", `{"data":{}}`, "Content-Length", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &BaseAPIHandler{}
			handler.SetPluginHost(&handlerInterceptorTestHost{
				interceptResponse: func(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
					return pluginapi.ResponseInterceptResponse{Body: []byte(tc.body), Headers: http.Header{tc.header: {strconv.Itoa(len(tc.body))}}}
				},
			})
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			ctx.Set(sdkaccess.ModelPolicyContextKey, sdkaccess.NewModelPolicy([]string{"gpt-*"}, nil))
			handler.WriteModelListResponse(ctx, "openai", gin.H{"data": []any{}})
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}
			if length := recorder.Header().Get("Content-Length"); length != "" && length != strconv.Itoa(recorder.Body.Len()) {
				t.Fatalf("Content-Length = %s, actual bytes = %d", length, recorder.Body.Len())
			}
		})
	}
}

func TestFilterModelListResponseBody_FiltersBothArrays(t *testing.T) {
	ctx := ginWithPolicy(sdkaccess.NewModelPolicy([]string{"gpt-*"}, nil))
	body, errFilter := FilterModelListResponseBody(ctx, []byte(`{"data":[{"id":"denied"},null,{"id":"gpt-4o"},{"id":"denied-again"}],"models":[{"name":"models/denied"},{"name":"models/gpt-5"}],"extra":{"keep":true}}`))
	if errFilter != nil {
		t.Fatal(errFilter)
	}
	result := ojson.ParseBytes(body)
	data, models := result.Get("data").Array(), result.Get("models").Array()
	if len(data) != 1 || data[0].Get("id").String() != "gpt-4o" || len(models) != 1 || models[0].Get("name").String() != "models/gpt-5" || !result.Get("extra.keep").Bool() {
		t.Fatalf("unexpected filtered catalog: %s", body)
	}
}
