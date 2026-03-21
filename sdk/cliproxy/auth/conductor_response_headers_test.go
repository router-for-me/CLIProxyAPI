package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type responseHeaderTestExecutor struct {
	provider string
}

func (e responseHeaderTestExecutor) Identifier() string { return e.provider }

func (e responseHeaderTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{
		Payload: []byte("execute"),
		Headers: http.Header{"X-Upstream": {"execute"}},
	}, nil
}

func (e responseHeaderTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("stream")}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: http.Header{"X-Upstream": {"stream"}},
		Chunks:  ch,
	}, nil
}

func (e responseHeaderTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e responseHeaderTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{
		Payload: []byte("count"),
		Headers: http.Header{"X-Upstream": {"count"}},
	}, nil
}

func (e responseHeaderTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newResponseHeaderTestManager(t *testing.T, provider, model string) *Manager {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(responseHeaderTestExecutor{provider: provider})

	authID := "response-headers-" + t.Name()
	registerSchedulerModels(t, provider, model, authID)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: provider,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	return manager
}

func TestAnnotateExecutionHeadersClonesInput(t *testing.T) {
	t.Parallel()

	original := http.Header{"X-Upstream": {"ok"}}
	annotated := annotateExecutionHeaders(original, "codex", "claude-sonnet-4-6", "gpt-5")

	if got := annotated.Get("X-Upstream"); got != "ok" {
		t.Fatalf("annotated upstream header = %q, want %q", got, "ok")
	}
	if got := annotated.Get(responseHeaderProvider); got != "codex" {
		t.Fatalf("annotated provider header = %q, want %q", got, "codex")
	}
	if got := annotated.Get(responseHeaderRequestedModel); got != "claude-sonnet-4-6" {
		t.Fatalf("annotated requested-model header = %q, want %q", got, "claude-sonnet-4-6")
	}
	if got := annotated.Get(responseHeaderBackendModel); got != "gpt-5" {
		t.Fatalf("annotated backend-model header = %q, want %q", got, "gpt-5")
	}
	if got := original.Get(responseHeaderProvider); got != "" {
		t.Fatalf("original provider header = %q, want empty", got)
	}
	if got := original.Get(responseHeaderRequestedModel); got != "" {
		t.Fatalf("original requested-model header = %q, want empty", got)
	}
	if got := original.Get(responseHeaderBackendModel); got != "" {
		t.Fatalf("original backend-model header = %q, want empty", got)
	}
}

func TestManagerExecuteAddsResponseHeadersOnDirectSuccess(t *testing.T) {
	t.Parallel()

	const (
		provider = "claude"
		model    = "claude-sonnet-4-6"
	)

	manager := newResponseHeaderTestManager(t, provider, model)

	resp, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := resp.Headers.Get("X-Upstream"); got != "execute" {
		t.Fatalf("upstream header = %q, want %q", got, "execute")
	}
	if got := resp.Headers.Get(responseHeaderProvider); got != provider {
		t.Fatalf("provider header = %q, want %q", got, provider)
	}
	if got := resp.Headers.Get(responseHeaderRequestedModel); got != model {
		t.Fatalf("requested-model header = %q, want %q", got, model)
	}
	if got := resp.Headers.Get(responseHeaderBackendModel); got != model {
		t.Fatalf("backend-model header = %q, want %q", got, model)
	}
}

func TestManagerExecuteCountAddsResponseHeadersOnDirectSuccess(t *testing.T) {
	t.Parallel()

	const (
		provider = "claude"
		model    = "claude-sonnet-4-6"
	)

	manager := newResponseHeaderTestManager(t, provider, model)

	resp, err := manager.ExecuteCount(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteCount() error = %v", err)
	}
	if got := resp.Headers.Get("X-Upstream"); got != "count" {
		t.Fatalf("upstream header = %q, want %q", got, "count")
	}
	if got := resp.Headers.Get(responseHeaderProvider); got != provider {
		t.Fatalf("provider header = %q, want %q", got, provider)
	}
	if got := resp.Headers.Get(responseHeaderRequestedModel); got != model {
		t.Fatalf("requested-model header = %q, want %q", got, model)
	}
	if got := resp.Headers.Get(responseHeaderBackendModel); got != model {
		t.Fatalf("backend-model header = %q, want %q", got, model)
	}
}

func TestManagerExecuteStreamAddsResponseHeadersOnDirectSuccess(t *testing.T) {
	t.Parallel()

	const (
		provider = "claude"
		model    = "claude-sonnet-4-6"
	)

	manager := newResponseHeaderTestManager(t, provider, model)

	streamResult, err := manager.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if got := streamResult.Headers.Get("X-Upstream"); got != "stream" {
		t.Fatalf("upstream header = %q, want %q", got, "stream")
	}
	if got := streamResult.Headers.Get(responseHeaderProvider); got != provider {
		t.Fatalf("provider header = %q, want %q", got, provider)
	}
	if got := streamResult.Headers.Get(responseHeaderRequestedModel); got != model {
		t.Fatalf("requested-model header = %q, want %q", got, model)
	}
	if got := streamResult.Headers.Get(responseHeaderBackendModel); got != model {
		t.Fatalf("backend-model header = %q, want %q", got, model)
	}

	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "stream" {
		t.Fatalf("stream payload = %q, want %q", string(payload), "stream")
	}
}
