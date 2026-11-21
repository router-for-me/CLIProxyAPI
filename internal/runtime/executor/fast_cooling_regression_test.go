package executor_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type fastLocalTransport struct{ target *url.URL }

func (t fastLocalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	c.URL = &u
	return http.DefaultTransport.RoundTrip(c)
}

type fastCoolingHook struct {
	coreauth.NoopHook
	mu      sync.Mutex
	results []coreauth.Result
}

func (h *fastCoolingHook) OnResult(_ context.Context, r coreauth.Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = append(h.results, r)
}

func TestFastCredential429RecordsResultAndCools(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"speed":"fast"`) {
			t.Errorf("not Fast: %s", b)
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"credential quota exceeded"}}`)
	}))
	defer upstream.Close()
	hook := &fastCoolingHook{}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, hook)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	const model = "claude-opus-5"
	auth := &coreauth.Auth{ID: "fast-real-429", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "synthetic", "base_url": "https://api.anthropic.com"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	manager.RefreshSchedulerEntry(auth.ID)
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	target, _ := url.Parse(upstream.URL)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(fastLocalTransport{target: target}))
	_, err := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"claude-opus-5","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"hello"}],"stream":true}`)}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude})
	var direct *cliproxyexecutor.RequestTerminatedError
	if !errors.As(err, &direct) || direct.StatusCode() != 429 {
		t.Fatalf("want real Fast direct 429, got %T %v", err, err)
	}
	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(err, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("not credential scoped: %T %v", err, err)
	}
	current, _ := manager.GetByID(auth.ID)
	hook.mu.Lock()
	defer hook.mu.Unlock()
	cooled := current.Unavailable && current.NextRetryAfter.After(time.Now())
	t.Logf("attempts=%d results=%d credentialUnavailable=%t retryInFuture=%t", attempts.Load(), len(hook.results), current.Unavailable, current.NextRetryAfter.After(time.Now()))
	if attempts.Load() != 1 {
		t.Errorf("attempts=%d want1", attempts.Load())
	}
	if len(hook.results) != 1 {
		t.Errorf("results=%d want1", len(hook.results))
	} else if !hook.results[0].CredentialScope {
		t.Error("result lost credential scope")
	}
	if !cooled {
		t.Errorf("credential not cooled: unavailable=%t next=%s", current.Unavailable, current.NextRetryAfter)
	}
}
