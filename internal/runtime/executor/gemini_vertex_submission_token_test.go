package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type vertexSubmissionRoute struct {
	next     http.RoundTripper
	target   *url.URL
	prewrite *atomic.Bool
}

func (r vertexSubmissionRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.prewrite != nil && req.URL.Path != "/token" && r.prewrite.CompareAndSwap(false, true) {
		return nil, errors.New("synthetic failure before model write")
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	return r.next.RoundTrip(clone)
}

func TestVertexSubmissionTokenSuccessDoesNotBlockModelFailover(t *testing.T) {
	for _, refusal := range []int{429, 503, 0} {
		t.Run(fmt.Sprint(refusal), func(t *testing.T) {
			var tokens, generations atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					tokens.Add(1)
					_, _ = io.WriteString(w, `{"access_token":"synthetic","token_type":"Bearer","expires_in":3600}`)
					return
				}
				if generations.Add(1) == 1 && refusal != 0 {
					w.WriteHeader(refusal)
					_, _ = io.WriteString(w, `{"error":{"code":429,"message":"provider rejected generation"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"safe-fallback"}]},"finishReason":"STOP"}]}`)
			}))
			defer upstream.Close()
			sa := map[string]any{}
			for k, v := range ojson.ParseBytes(testVertexServiceAccountJSON(t, upstream.URL+"/token")).Map() {
				sa[k] = v.String()
			}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewGeminiVertexExecutor(&config.Config{}))
			const model = "gemini-2.5-flash"
			for _, suffix := range []string{"a", "b"} {
				auth := &coreauth.Auth{ID: t.Name() + suffix, Provider: "vertex", Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"project_id": "synthetic", "service_account": sa}}
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			target, _ := url.Parse(upstream.URL)
			var prewrite atomic.Bool
			ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
				var failure *atomic.Bool
				if refusal == 0 {
					failure = &prewrite
				}
				return vertexSubmissionRoute{next, target, failure}
			})
			resp, err := manager.Execute(ctx, []string{"vertex"}, coreexecutor.Request{Model: model, Payload: []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hello"}]}`)}, coreexecutor.Options{SourceFormat: translator.FormatOpenAI})
			t.Logf("token POSTs=%d generation POSTs=%d error=%v", tokens.Load(), generations.Load(), err)
			wantGenerations := int32(2)
			if refusal == 0 {
				wantGenerations = 1
			}
			if err != nil || generations.Load() != wantGenerations || tokens.Load() != 2 {
				t.Fatalf("token success suppressed safe model failover: %v payload=%s", err, resp.Payload)
			}
		})
	}
}
