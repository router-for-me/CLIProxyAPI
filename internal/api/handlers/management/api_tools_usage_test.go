package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type managementUsageReviewSink struct {
	authID  string
	records []usage.Record
}

func (*managementUsageReviewSink) Synchronous() bool { return true }
func (s *managementUsageReviewSink) HandleUsage(_ context.Context, r usage.Record) {
	if r.AuthID == s.authID {
		s.records = append(s.records, r)
	}
}
func TestManagementUsageUsesProviderProtocol(t *testing.T) {
	for _, tc := range []struct{ name, provider, path, body string }{
		{"gemini", "gemini", "/v1beta/models/test:generateContent", `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":10,"totalTokenCount":130}}`},
		{"antigravity", "antigravity", "/v1internal:generateContent", `{"response":{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":10,"totalTokenCount":130}}}`},
		{"claude_stream", "claude", "/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":30}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(tc.body, "data:") {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			auth := &coreauth.Auth{ID: t.Name(), Provider: tc.provider}
			auth.EnsureIndex()
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			sink := &managementUsageReviewSink{authID: t.Name()}
			usage.RegisterPlugin(sink)
			h := &Handler{authManager: manager}
			router := gin.New()
			router.POST("/", h.APICall)
			body, _ := json.Marshal(map[string]any{"method": "POST", "url": upstream.URL + tc.path, "auth_index": auth.Index, "data": `{"model":"test-model"}`})
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if len(sink.records) != 1 {
				t.Fatalf("want one usage event, got %d (HTTP %d)", len(sink.records), recorder.Code)
			}
			d := sink.records[0].Detail
			if !d.UsageObserved || d.TotalTokens != 130 {
				t.Fatalf("lost provider usage: %+v", d)
			}
		})
	}
}

func TestManagementUsageDerivesModelFromURL(t *testing.T) {
	for _, tc := range []struct {
		name, provider, path, data, wantModel string
	}{
		{"gemini", "gemini", "/v1beta/models/gemini-2.5-pro:generateContent", `{"contents":[]}`, "gemini-2.5-pro"},
		{"gemini_stream", "gemini", "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", `{"contents":[]}`, "gemini-2.5-flash"},
		{"vertex", "vertex", "/v1/projects/demo/locations/global/publishers/google/models/gemini-2.5-pro:generateContent", `{"contents":[]}`, "gemini-2.5-pro"},
		{"antigravity", "antigravity", "/v1beta/models/gemini-2.5-pro:generateContent", `{"contents":[]}`, "gemini-2.5-pro"},
		{"without_provider", "", "/v1beta/models/gemini-2.5-pro:generateContent", `{"contents":[]}`, "gemini-2.5-pro"},
		{"explicit_model", "gemini", "/v1beta/models/path-model:generateContent", `{"model":"body-model"}`, "body-model"},
		{"unrelated_path", "gemini", "/v1/models/model:delete", `{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload := `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":30,"totalTokenCount":130}}`
				if tc.provider == "antigravity" {
					payload = `{"response":` + payload + `}`
				}
				if strings.Contains(tc.path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					payload = "data: " + payload + "\n\n"
				}
				_, _ = w.Write([]byte(payload))
			}))
			defer upstream.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			auth := &coreauth.Auth{ID: t.Name(), Provider: tc.provider}
			auth.EnsureIndex()
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			sink := &managementUsageReviewSink{authID: t.Name()}
			usage.RegisterPlugin(sink)
			h := &Handler{authManager: manager}
			router := gin.New()
			router.POST("/", h.APICall)
			body, _ := json.Marshal(map[string]any{"method": "POST", "url": upstream.URL + tc.path, "auth_index": auth.Index, "data": tc.data})
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
			}
			if tc.wantModel == "" {
				if len(sink.records) != 0 {
					t.Fatalf("unrelated URL generated usage: %+v", sink.records)
				}
				return
			}
			if len(sink.records) != 1 {
				t.Fatalf("want one usage event, got %d", len(sink.records))
			}
			r := sink.records[0]
			if r.Model != tc.wantModel || !r.Detail.UsageObserved || r.Detail.TotalTokens != 130 {
				t.Fatalf("lost model or usage: %+v", r)
			}
		})
	}
}
