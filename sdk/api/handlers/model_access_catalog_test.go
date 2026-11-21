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

func TestFilterModelListResponseBody_RejectsDuplicateKeys(t *testing.T) {
	policy := sdkaccess.NewModelPolicy([]string{"gpt-*"}, nil)
	for _, tc := range []struct {
		name string
		body string
	}{
		// ojson keeps the first "id" and the entry would survive, but a client that
		// keeps the last duplicate would receive the denied model.
		{"duplicate entry id", `{"data":[{"id":"gpt-4o","id":"denied-model"}]}`},
		{"duplicate escaped entry id", `{"data":[{"id":"gpt-4o","\u0069d":"denied-model"}]}`},
		{"duplicate nested key", `{"data":[{"id":"gpt-4o","limits":{"max":1,"max":2}}]}`},
		// Only the first array is filtered, so the second would leak every entry.
		{"duplicate data member", `{"data":[{"id":"gpt-4o"}],"data":[{"id":"denied-model"}]}`},
		{"duplicate models member", `{"models":[{"name":"models/gpt-4o"}],"models":[{"name":"models/denied-model"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ginWithPolicy(policy)
			if filtered, errFilter := FilterModelListResponseBody(ctx, []byte(tc.body)); errFilter == nil {
				t.Fatalf("expected a fail-closed error, got %s", filtered)
			}
		})
	}

	t.Run("unrestricted", func(t *testing.T) {
		body := []byte(`{"data":[{"id":"gpt-4o","id":"denied-model"}]}`)
		ctx := ginWithPolicy(nil)
		filtered, errFilter := FilterModelListResponseBody(ctx, body)
		if errFilter != nil {
			t.Fatalf("unrestricted requests must not be affected: %v", errFilter)
		}
		if string(filtered) != string(body) {
			t.Fatalf("unrestricted body changed: %s", filtered)
		}
	})
}

func TestWriteModelListResponse_RejectsAmbiguousInterceptorCatalog(t *testing.T) {
	handler := &BaseAPIHandler{}
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptResponse: func(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			return pluginapi.ResponseInterceptResponse{Body: []byte(`{"data":[{"id":"gpt-4o","id":"denied-model"}]}`)}
		},
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	ctx.Set(sdkaccess.ModelPolicyContextKey, sdkaccess.NewModelPolicy([]string{"gpt-*"}, nil))
	handler.WriteModelListResponse(ctx, "openai", gin.H{"data": []any{}})
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	if recorded := ojson.ParseBytes(recorder.Body.Bytes()); recorded.Get("data").Exists() {
		t.Fatalf("fail-closed response must not echo a catalog: %s", recorder.Body.String())
	}
}

func TestFilterModelListResponseBody_NormalizesThinkingSuffix(t *testing.T) {
	body := []byte(`{"data":[{"id":"gpt-5(high)"}]}`)
	filtered, errFilter := FilterModelListResponseBody(ginWithPolicy(sdkaccess.NewModelPolicy([]string{"gpt-5"}, nil)), body)
	if errFilter != nil {
		t.Fatal(errFilter)
	}
	if models := ojson.ParseBytes(filtered).Get("data").Array(); len(models) != 1 {
		t.Fatalf("allowed model must not be hidden by its thinking suffix: %s", filtered)
	}

	filtered, errFilter = FilterModelListResponseBody(ginWithPolicy(sdkaccess.NewModelPolicy(nil, []string{"gpt-5"})), body)
	if errFilter != nil {
		t.Fatal(errFilter)
	}
	if models := ojson.ParseBytes(filtered).Get("data").Array(); len(models) != 0 {
		t.Fatalf("denied model must not be advertised through its thinking suffix: %s", filtered)
	}
}

func TestFilterModelListResponseBody_RejectsConflictingIdentity(t *testing.T) {
	policy := sdkaccess.NewModelPolicy([]string{"gpt-*"}, nil)
	for _, body := range []string{
		`{"models":[{"id":"gpt-5","name":"models/private"}]}`,
		`{"data":[{"id":"gpt-5","slug":"private"}]}`,
	} {
		if filtered, errFilter := FilterModelListResponseBody(ginWithPolicy(policy), []byte(body)); errFilter == nil {
			t.Fatalf("expected a fail-closed error for %s, got %s", body, filtered)
		}
	}

	agreeing := []byte(`{"models":[{"id":"gpt-5","name":"models/gpt-5"}]}`)
	filtered, errFilter := FilterModelListResponseBody(ginWithPolicy(policy), agreeing)
	if errFilter != nil {
		t.Fatalf("agreeing identity fields must not be rejected: %v", errFilter)
	}
	if models := ojson.ParseBytes(filtered).Get("models").Array(); len(models) != 1 {
		t.Fatalf("agreeing identity entry must stay listed: %s", filtered)
	}
}
