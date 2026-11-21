package claude

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const ccsWorker = "gpt-ccs-test-worker"
const ccsAdvisor = "gpt-ccs-test-advisor"
const nativeAdvisor = `{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5"}`
const ccsBody = `{"model":"` + ccsWorker + `","stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[` + nativeAdvisor + `]}`
const interactive = `"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2; cc_entrypoint=cli; cc_turn_origin=human;"}],"messages":[{"role":"user","content":"review my project"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}]`

type ccsExecutor struct {
	preFTKeepAliveClaudeExecutor
	provider string
	execute  func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
}

func (e *ccsExecutor) Identifier() string { return e.provider }

func TestCCSAdvisorRejectsMalformedUpstreamBeforeSSE(t *testing.T) {
	t.Setenv("CCS_CODEX_ADVISOR_MODEL", ccsAdvisor)
	h, e := ccsHandler(t, "codex", ccsWorker, ccsAdvisor)
	e.execute = func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
		return coreexecutor.Response{Payload: []byte(`{"type":"message","content":[null]}`)}, nil
	}
	w := ccsRequest(h, ccsBody, false)
	if w.Code != http.StatusBadGateway || ojson.Get(w.Body.String(), "type").String() != "error" {
		t.Fatalf("invalid response committed as success: %d %s", w.Code, w.Body)
	}
}

func TestCCSAdvisorCancellation(t *testing.T) {
	t.Setenv("CCS_CODEX_ADVISOR_MODEL", ccsAdvisor)
	h, e := ccsHandler(t, "codex", ccsWorker, ccsAdvisor)
	start := make(chan struct{})
	e.execute = func(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		close(start)
		<-ctx.Done()
		return coreexecutor.Response{}, ctx.Err()
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", strings.NewReader(ccsBody))
	done := make(chan struct{})
	go func() { defer close(done); h.ClaudeMessages(c) }()
	select {
	case <-start:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach the executor")
	}
	if w.Body.Len() != 0 {
		t.Fatal(w.Body.String())
	}
}

func TestCCSAdvisorConsultation(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			t.Setenv("CCS_CODEX_ADVISOR_MODEL", ccsAdvisor)
			h, e := ccsHandler(t, "codex", ccsWorker, ccsAdvisor)
			calls := 0
			e.execute = func(_ context.Context, a *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
				calls++
				if a.Provider != "codex" || o.Stream || ojson.GetBytes(r.Payload, "stream").Bool() || o.Headers.Get("X-Session-Test") != "roundtrip" {
					return coreexecutor.Response{}, errors.New("lost parent auth or execution options")
				}
				var content, stop string
				switch calls {
				case 1:
					if r.Model != ccsWorker || ojson.GetBytes(r.Payload, "tools.0.input_schema.type").String() != "object" {
						return coreexecutor.Response{}, errors.New("worker lacks local tool")
					}
					content = `{"type":"tool_use","name":"advisor","id":"t1","input":{"question":"what now?"}}`
					stop = "tool_use"
				case 2:
					if r.Model != ccsAdvisor || ojson.GetBytes(r.Payload, "tools").Exists() {
						return coreexecutor.Response{}, errors.New("wrong advisor subcall")
					}
					content = `{"type":"text","text":"my advice"}`
					stop = "end_turn"
				case 3:
					if r.Model != ccsWorker || !strings.Contains(string(r.Payload), "my advice") {
						return coreexecutor.Response{}, errors.New("worker did not receive advice")
					}
					content = `{"type":"text","text":"final answer"}`
					stop = "end_turn"
				default:
					return coreexecutor.Response{}, errors.New("unexpected call")
				}
				return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"id":"msg","role":"assistant","type":"message","model":%q,"content":[%s],"stop_reason":%q,"usage":{"input_tokens":5,"output_tokens":2}}`, r.Model, content, stop))}, nil
			}
			body := strings.Replace(ccsBody, `"stream":true`, fmt.Sprintf(`"stream":%t`, stream), 1)
			w := ccsRequest(h, body, false)
			if w.Code != 200 || calls != 3 {
				t.Fatalf("%d calls=%d %s", w.Code, calls, w.Body)
			}
			for _, s := range []string{"server_tool_use", "advisor_tool_result", "my advice", "final answer"} {
				if !strings.Contains(w.Body.String(), s) {
					t.Fatalf("missing %q: %s", s, w.Body)
				}
			}
			if stream && !strings.Contains(w.Body.String(), "input_json_delta") {
				t.Fatal(w.Body.String())
			}
			if !stream && ojson.Get(w.Body.String(), "content.2.text").String() != "final answer" {
				t.Fatal(w.Body.String())
			}
		})
	}
}
func (e *ccsExecutor) Execute(ctx context.Context, a *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
	return e.execute(ctx, a, r, o)
}

func ccsHandler(t *testing.T, provider string, models ...string) (*ClaudeCodeAPIHandler, *ccsExecutor) {
	t.Helper()
	e := &ccsExecutor{provider: provider}
	e.chunks = make(chan coreexecutor.StreamChunk, 1)
	e.chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	close(e.chunks)
	e.execute = func(ctx context.Context, a *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
		if a.Provider != provider || o.Stream || ojson.GetBytes(r.Payload, "stream").Bool() || o.Headers.Get("X-Session-Test") != "roundtrip" {
			return coreexecutor.Response{}, fmt.Errorf("invalid subcall context or stream")
		}
		return coreexecutor.Response{Payload: []byte(`{"id":"msg_test","model":"` + r.Model + `","role":"assistant","type":"message","content":[{"type":"text","text":"worker"},{"type":"tool_use","id":"t1","name":"Read","input":{"path":"a.go"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":8,"output_tokens":3}}`)}, nil
	}
	m := coreauth.NewManager(nil, nil, nil)
	m.RegisterExecutor(e)
	a := &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	var infos []*registry.ModelInfo
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, provider, infos)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	return NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m)), e
}

func ccsRequest(h *ClaudeCodeAPIHandler, body string, local bool) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	c.Request.Header.Set("X-Session-Test", "roundtrip")
	if local {
		c.Request.Header.Set("X-Ccs-Local-Advisor", "1")
	}
	h.ClaudeMessages(c)
	return w
}

func TestCCSAdvisorTransport(t *testing.T) {
	const declared = `"messages":[{"role":"user","content":"test"}],"tools":[` + nativeAdvisor + `]`
	for _, tc := range []struct {
		name, provider, model, env, fields string
		local, emulate                     bool
	}{
		{"explicit Codex", "codex", ccsWorker, ccsAdvisor, declared, false, true},
		{"disabled", "codex", ccsWorker, "", declared, true, false},
		{"GPT on a different provider", "another", ccsWorker, ccsAdvisor, declared, true, false},
		{"no profile header", "codex", ccsWorker, ccsAdvisor, interactive, false, false},
		{"human turn", "codex", ccsWorker, ccsAdvisor, interactive, true, true},
		{"automated classifier", "codex", ccsWorker, ccsAdvisor, strings.Replace(interactive, "cc_turn_origin=human", "cc_turn_origin=automation", 1), true, false},
		{"structured query", "codex", ccsWorker, ccsAdvisor, interactive + `,"output_config":{"format":{"type":"json_schema"}}`, true, false},
		{"scheduled workload", "codex", ccsWorker, ccsAdvisor, strings.Replace(interactive, "cc_turn_origin=human;", "cc_turn_origin=human; cc_workload=cron;", 1), true, false},
		{"alias with tools", "codex", "claude-sonnet-5-5", ccsAdvisor, declared, true, true},
		{"alias without tools", "codex", "claude-sonnet-5-5", ccsAdvisor, `"messages":[]`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CCS_CODEX_ADVISOR_MODEL", tc.env)
			t.Setenv("CCS_CODEX_WORKER_MODEL", ccsWorker)
			h, _ := ccsHandler(t, tc.provider, ccsWorker, ccsAdvisor)
			w := ccsRequest(h, `{"model":"`+tc.model+`","stream":true,`+tc.fields+`}`, tc.local)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			body := w.Body.String()
			if !tc.emulate {
				if body != "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n" {
					t.Fatalf("native stream changed: %s", body)
				}
				return
			}
			var events []string
			for _, event := range strings.Split(strings.TrimSpace(body), "\n\n") {
				name, data, ok := strings.Cut(event, "\ndata: ")
				if !ok || name != "event: "+ojson.Get(data, "type").String() {
					t.Fatalf("invalid SSE: %s", event)
				}
				events = append(events, ojson.Get(data, "type").String())
				if ojson.Get(data, "delta.type").String() == "input_json_delta" && ojson.Get(ojson.Get(data, "delta.partial_json").String(), "path").String() != "a.go" {
					t.Fatal(data)
				}
			}
			if strings.Join(events, ",") != "message_start,content_block_start,content_block_delta,content_block_stop,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop" || !strings.Contains(body, "input_json_delta") {
				t.Fatalf("invalid generated stream: %s", body)
			}
		})
	}
}
