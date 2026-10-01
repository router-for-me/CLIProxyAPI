package claudemaster

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestPrepareBackendModelMapOnlyChangesTopLevelModel(t *testing.T) {
	const source, target = "claude-sonnet-5-5", "claude-test-target-model"
	for _, tc := range []struct {
		name, path string
		stream     bool
	}{
		{name: "messages", path: "/v1/messages"},
		{name: "stream", path: "/v1/messages", stream: true},
		{name: "count_tokens", path: "/v1/messages/count_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := fmt.Sprintf("{\n  \"model\": %q,\n  \"messages\": [{\"role\":\"user\",\"content\":\"claude-sonnet-5-5\"}],\n  \"tools\": [{\"name\":\"Bash\",\"input_schema\":{\"properties\":{\"model\":{\"const\":\"claude-sonnet-5-5\"}}}}],\n  \"metadata\": {\"user_id\":\"master-identity\",\"future_identity\":true},\n  \"system\": [{\"type\":\"text\",\"text\":\"x-anthropic-billing-header: cc_version=99.7.1; cch=00000;\"}],\n  \"thinking\": {\"type\":\"enabled\",\"budget_tokens\":1024}, \"future_option\": {\"model\":\"claude-sonnet-5-5\"},\n  \"stream\": %t\n}\n", source, tc.stream)
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(input))
			clean, model, err := prepareBackendRequest(request, BackendOptions{
				UseRequestModel: true, ModelMap: map[string]string{source: target},
			})
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(input, `"model": "`+source+`"`, `"model": "`+target+`"`, 1)
			if model != target || string(clean) != want {
				t.Fatalf("mapping changed more than the top-level model: model=%q\nwant: %q\n got: %q", model, want, clean)
			}
			body, errRead := io.ReadAll(request.Body)
			if errRead != nil || !bytes.Equal(body, clean) || request.ContentLength != int64(len(clean)) {
				t.Fatalf("rewritten request framing/body differ: length=%d body=%q err=%v", request.ContentLength, body, errRead)
			}
		})
	}
}

func TestPrepareBackendModelMapIsExactAndOneHop(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
		modelMap           map[string]string
	}{
		{name: "one_hop", source: "sonnet", want: "opus", modelMap: map[string]string{"sonnet": "opus", "opus": "haiku"}},
		{name: "colon_is_model_data", source: "anthropic:sonnet", want: "future:opus", modelMap: map[string]string{"anthropic:sonnet": "future:opus"}},
		{name: "no_provider_colon_match", source: "anthropic:sonnet", want: "anthropic:sonnet", modelMap: map[string]string{"anthropic": "opus"}},
		{name: "no_provider_slash_match", source: "anthropic/sonnet", want: "anthropic/sonnet", modelMap: map[string]string{"anthropic": "opus"}},
		{name: "no_suffix_match", source: "sonnet(high)", want: "sonnet(high)", modelMap: map[string]string{"sonnet": "opus"}},
		{name: "case_sensitive", source: "Sonnet", want: "Sonnet", modelMap: map[string]string{"sonnet": "opus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, tc.source)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(input))
			clean, model, err := prepareBackendRequest(request, BackendOptions{UseRequestModel: true, ModelMap: tc.modelMap})
			if err != nil {
				t.Fatal(err)
			}
			if model != tc.want || gjson.GetBytes(clean, "model").String() != tc.want {
				t.Fatalf("mapped model = %q / %q, want %q", model, gjson.GetBytes(clean, "model").String(), tc.want)
			}
			if tc.want == tc.source && string(clean) != input {
				t.Fatal("unmatched model changed native bytes")
			}
		})
	}
}

func TestPrepareBackendModelMapUnmappedPreservesBytes(t *testing.T) {
	const input = "{\n  \"model\": \"claude\\u002dunmapped\", \"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\n  \"future_option\": 1.00, \"metadata\": {\"user_id\":\"native-identity\"}\n}\n"
	for _, modelMap := range []map[string]string{nil, {}, {"claude-other": "claude-replacement"}} {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(input))
		clean, model, err := prepareBackendRequest(request, BackendOptions{UseRequestModel: true, ModelMap: modelMap})
		if err != nil {
			t.Fatal(err)
		}
		if model != "claude-unmapped" || string(clean) != input {
			t.Fatalf("unmatched mapping reserialized native body: model=%q body=%q", model, clean)
		}
	}
}

func TestPrepareBackendModelMapRejectsDuplicateModel(t *testing.T) {
	for _, input := range []string{
		`{"model":"other","model":"source","messages":[]}`,
		`{"mo\u0064el":"other","model":"source","messages":[]}`,
		`{"model":"source","model":"source","messages":[]}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(input))
		_, _, err := prepareBackendRequest(request, BackendOptions{UseRequestModel: true, ModelMap: map[string]string{"source": "target"}})
		if err == nil {
			t.Fatalf("accepted ambiguous mapped model: %s", input)
		}
	}
}

type backendModelMapCapture struct{ backendCapture }

func (*backendModelMapCapture) Identifier() string { return "claude" }

func TestBackendModelMapExecutionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		stream     bool
	}{
		{name: "messages", path: "/v1/messages"},
		{name: "stream", path: "/v1/messages", stream: true},
		{name: "count_tokens", path: "/v1/messages/count_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const source, target = "claude-source", "claude-target"
			authID := t.Name() + "-subscription"
			capture := &backendModelMapCapture{}
			manager := coreauth.NewManager(nil, &backendSelector{authID: authID, provider: "claude"}, nil)
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(capture)
			registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
			t.Cleanup(func() {
				manager.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
				registry.GetGlobalRegistry().UnregisterClient(authID)
			})
			if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive}); err != nil {
				t.Fatal(err)
			}
			handler := newBackendHandler(t.Context(), BackendOptions{
				Provider: "claude", UseRequestModel: true,
				ModelMap: map[string]string{source: target, target: "must-not-chain"},
			}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
			input := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, source, tc.stream)
			writer := httptest.NewRecorder()
			handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(input)))
			if writer.Code != http.StatusOK {
				t.Fatalf("execution status=%d body=%q", writer.Code, writer.Body.String())
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if capture.calls != 1 || capture.req.Model != target || gjson.GetBytes(capture.req.Payload, "model").String() != target || gjson.GetBytes(capture.opts.OriginalRequest, "model").String() != target {
				t.Fatalf("execution and request models differ: calls=%d execution=%q payload=%q original=%q", capture.calls, capture.req.Model, capture.req.Payload, capture.opts.OriginalRequest)
			}
			if got := capture.opts.Metadata[coreexecutor.RequestedModelMetadataKey]; got != target {
				t.Fatalf("availability/cooldown model=%v, want %q", got, target)
			}
			if got := capture.opts.Metadata[coreexecutor.AuthSelectionModelMetadataKey]; got != backendAuthSelectionModel {
				t.Fatalf("auth registry model=%v, want %q", got, backendAuthSelectionModel)
			}
		})
	}
}
