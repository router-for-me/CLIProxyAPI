package engine

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type captureExecutor struct {
	mu     sync.Mutex
	authID string
	req    cliproxyexecutor.Request
	fail   error
}

func (c *captureExecutor) Identifier() string { return "claude" }
func (c *captureExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("unexpected non-stream call")
}
func (c *captureExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	c.mu.Lock()
	c.authID, c.req = a.ID, req
	fail := c.fail
	c.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`event: message_start`)}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"content_block_delta","delta":{"text":"OK"}}`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (c *captureExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (c *captureExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (c *captureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unsupported")
}

type statusErr struct{ code int }

func (s statusErr) Error() string   { return "upstream refused. details follow" }
func (s statusErr) StatusCode() int { return s.code }

// TestIgnitionGoesThroughExecutorPinned checks that ignition requests are
// handed to CPA's executor (where payload rules are applied last) in the
// provider's native format and pinned to the chosen account.
func TestIgnitionGoesThroughExecutorPinned(t *testing.T) {
	exec := &captureExecutor{}
	m := coreauth.NewManager(nil, nil, nil)
	m.RegisterExecutor(exec)
	for _, id := range []string{"prox-ign-a", "prox-ign-b"} {
		if _, err := m.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: "claude-haiku-5-5"}})
		defer registry.GetGlobalRegistry().UnregisterClient(id)
	}
	e := New()
	e.SetManager(m)
	h := igniteHost{e: e}
	if got := h.Models("prox-ign-b"); len(got) != 1 || got[0] != "claude-haiku-5-5" {
		t.Fatalf("models: %v", got)
	}
	res := h.Fire(context.Background(), "claude", "prox-ign-b", "claude-haiku-5-5")
	if res.Err != nil {
		t.Fatalf("fire: %v", res.Err)
	}
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if exec.authID != "prox-ign-b" {
		t.Fatalf("request not pinned: went to %q", exec.authID)
	}
	if exec.req.Format != sdktranslator.FormatClaude {
		t.Fatalf("format = %q", exec.req.Format)
	}
	var body map[string]any
	if err := json.Unmarshal(exec.req.Payload, &body); err != nil || body["stream"] != true || body["model"] != "claude-haiku-5-5" {
		t.Fatalf("payload: %s (%v)", exec.req.Payload, err)
	}
}

func TestClassifyFireError(t *testing.T) {
	if r := classifyFireError(statusErr{429}); r.Status != 429 || r.Cooldown {
		t.Fatalf("got %+v", r)
	}
	if r := classifyFireError(&coreauth.Error{Code: "model_cooldown", HTTPStatus: 429}); !r.Cooldown {
		t.Fatalf("expected cooldown: %+v", r)
	}
}

func TestIgnitionRequestCodex(t *testing.T) {
	raw, format, err := ignitionRequest("codex", "gpt-6-luna")
	if err != nil || format != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("format %q err %v", format, err)
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body["store"] != false || body["instructions"] == "" {
		t.Fatalf("payload: %s", raw)
	}
	if _, _, err := ignitionRequest("kimi", "x"); err == nil {
		t.Fatal("kimi must not be ignitable")
	}
}
