package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// newOpenCodeGoTestServer spins an upstream stub recording the request path,
// auth header, and opencode identity headers of the first request it sees.
func newOpenCodeGoTestServer(t *testing.T, response string) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.apiKey = r.Header.Get("x-api-key")
		rec.anthropicVersion = r.Header.Get("anthropic-version")
		rec.userAgent = r.Header.Get("User-Agent")
		rec.session = r.Header.Get("x-opencode-session")
		rec.request = r.Header.Get("x-opencode-request")
		rec.client = r.Header.Get("x-opencode-client")
		rec.project = r.Header.Get("x-opencode-project")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

type recorder struct {
	path             string
	auth             string
	apiKey           string
	anthropicVersion string
	userAgent        string
	session          string
	request          string
	client           string
	project          string
}

func openCodeGoTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "opencode-go",
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			"base_url": baseURL,
			"api_key":  "sk-test",
		},
	}
}

func openCodeGoTestOpts() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FromString("openai"),
	}
}

func TestOpenCodeGoExecutor_Execute_OpenAIWire(t *testing.T) {
	srv, rec := newOpenCodeGoTestServer(t, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocgo",
			BaseURL: srv.URL,
			Models: []config.OpenCodeGoModel{
				{Name: "glm-5.2", Alias: "glm-5.2"},
			},
		}},
	}
	e := NewOpenCodeGoExecutor(cfg)
	resp, err := e.Execute(context.Background(), openCodeGoTestAuth(srv.URL), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
	}, openCodeGoTestOpts())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(rec.path, "/chat/completions") {
		t.Errorf("upstream path = %s, want /chat/completions", rec.path)
	}
	if rec.auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", rec.auth)
	}
	if len(strings.TrimSpace(string(resp.Payload))) == 0 {
		t.Error("empty response payload")
	}
}

func TestOpenCodeGoExecutor_Execute_AnthropicWire(t *testing.T) {
	srv, rec := newOpenCodeGoTestServer(t, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocgo",
			BaseURL: srv.URL,
			Models: []config.OpenCodeGoModel{
				{Name: "minimax-m3", Alias: "minimax-m3", WireFormat: "anthropic"},
			},
		}},
	}
	e := NewOpenCodeGoExecutor(cfg)
	// Native Claude-format caller: the anthropic wire path runs without
	// upstream SSE translation (source format == wire format).
	opts := openCodeGoTestOpts()
	opts.SourceFormat = sdktranslator.FormatClaude
	resp, err := e.Execute(context.Background(), openCodeGoTestAuth(srv.URL), cliproxyexecutor.Request{
		Model:   "minimax-m3",
		Payload: []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":64}`),
	}, opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(rec.path, "/messages") {
		t.Errorf("upstream path = %s, want /messages (anthropic wire)", rec.path)
	}
	// The Claude executor sends Bearer for non-Anthropic-first-party
	// upstreams (isAnthropicUpstreamURL gates x-api-key), which matches
	// opencode.ai's expectations — OpenCode Zen takes Bearer on both wires.
	if rec.auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", rec.auth)
	}
	if rec.anthropicVersion == "" {
		t.Error("anthropic-version header missing")
	}
	if len(strings.TrimSpace(string(resp.Payload))) == 0 {
		t.Error("empty response payload")
	}
}

func TestOpenCodeGoExecutor_Execute_UnknownModelFallsBackToOpenAI(t *testing.T) {
	srv, rec := newOpenCodeGoTestServer(t, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocgo",
			BaseURL: srv.URL,
			Models: []config.OpenCodeGoModel{
				{Name: "glm-5.2", Alias: "glm-5.2"},
			},
		}},
	}
	e := NewOpenCodeGoExecutor(cfg)
	if _, err := e.Execute(context.Background(), openCodeGoTestAuth(srv.URL), cliproxyexecutor.Request{
		Model:   "not-in-config",
		Payload: []byte(`{"model":"not-in-config","messages":[{"role":"user","content":"hi"}]}`),
	}, openCodeGoTestOpts()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(rec.path, "/chat/completions") {
		t.Errorf("upstream path = %s, want openai fallback", rec.path)
	}
}

func TestOpenCodeGoExecutor_Execute_ParenSuffixStrippedBeforeLookup(t *testing.T) {
	srv, rec := newOpenCodeGoTestServer(t, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocgo",
			BaseURL: srv.URL,
			Models: []config.OpenCodeGoModel{
				{Name: "minimax-m3", Alias: "minimax-m3", WireFormat: "anthropic"},
			},
		}},
	}
	e := NewOpenCodeGoExecutor(cfg)
	// NixLLM effort suffixes are parenthesized: model(high).
	opts := openCodeGoTestOpts()
	opts.SourceFormat = sdktranslator.FormatClaude
	if _, err := e.Execute(context.Background(), openCodeGoTestAuth(srv.URL), cliproxyexecutor.Request{
		Model:   "minimax-m3(high)",
		Payload: []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":64}`),
	}, opts); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(rec.path, "/messages") {
		t.Errorf("upstream path = %s, want anthropic wire via suffix-stripped lookup", rec.path)
	}
}

// TestOpenCodeGoExecutor_Execute_StampsCLIIdentity verifies the synthesized
// opencode identity headers reach the upstream on every request — the
// Console Go tier rejects requests missing x-opencode-session with
// "MissingSessionID".
func TestOpenCodeGoExecutor_Execute_StampsCLIIdentity(t *testing.T) {
	srv, rec := newOpenCodeGoTestServer(t, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocgo",
			BaseURL: srv.URL,
			Models: []config.OpenCodeGoModel{
				{Name: "glm-5.2", Alias: "glm-5.2"},
			},
		}},
	}
	e := NewOpenCodeGoExecutor(cfg)
	if _, err := e.Execute(context.Background(), openCodeGoTestAuth(srv.URL), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello world"}]}`),
	}, openCodeGoTestOpts()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.session == "" {
		t.Error("x-opencode-session missing — upstream rejects with MissingSessionID")
	}
	if rec.request == "" {
		t.Error("x-opencode-request missing")
	}
	if rec.client != "desktop" {
		t.Errorf("x-opencode-client = %q, want desktop", rec.client)
	}
	if rec.project != "global" {
		t.Errorf("x-opencode-project = %q, want global", rec.project)
	}
	if rec.userAgent != "opencode" {
		t.Errorf("User-Agent = %q, want opencode (generic client UAs get 429'd)", rec.userAgent)
	}
}

// TestOpenCodeGoExecutor_Execute_SessionFingerprintStable verifies the
// session id is a deterministic conversation fingerprint: identical turns
// produce identical ids, different turns do not.
func TestOpenCodeGoExecutor_Execute_SessionFingerprintStable(t *testing.T) {
	srv, rec1 := newOpenCodeGoTestServer(t, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocgo",
			BaseURL: srv.URL,
			Models:  []config.OpenCodeGoModel{{Name: "glm-5.2"}},
		}},
	}
	e := NewOpenCodeGoExecutor(cfg)
	payload := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello world"}]}`)
	if _, err := e.Execute(context.Background(), openCodeGoTestAuth(srv.URL), cliproxyexecutor.Request{Model: "glm-5.2", Payload: payload}, openCodeGoTestOpts()); err != nil {
		t.Fatalf("Execute 1: %v", err)
	}
	first := rec1.session

	rec2 := &recorder{}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec2.session = r.Header.Get("x-opencode-session")
		_, _ = w.Write([]byte(`{"id":"2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer srv2.Close()
	cfg2 := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{Name: "ocgo", BaseURL: srv2.URL, Models: []config.OpenCodeGoModel{{Name: "glm-5.2"}}}},
	}
	if _, err := NewOpenCodeGoExecutor(cfg2).Execute(context.Background(), openCodeGoTestAuth(srv2.URL), cliproxyexecutor.Request{Model: "glm-5.2", Payload: payload}, openCodeGoTestOpts()); err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	if first == "" || rec2.session == "" {
		t.Fatalf("empty session ids: %q / %q", first, rec2.session)
	}
	if first != rec2.session {
		t.Fatalf("identical turns produced different session ids: %q vs %q", first, rec2.session)
	}

	// A different first user message changes the fingerprint.
	other := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"different turn"}]}`)
	rec3 := &recorder{}
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec3.session = r.Header.Get("x-opencode-session")
		_, _ = w.Write([]byte(`{"id":"3","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer srv3.Close()
	cfg3 := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{Name: "ocgo", BaseURL: srv3.URL, Models: []config.OpenCodeGoModel{{Name: "glm-5.2"}}}},
	}
	if _, err := NewOpenCodeGoExecutor(cfg3).Execute(context.Background(), openCodeGoTestAuth(srv3.URL), cliproxyexecutor.Request{Model: "glm-5.2", Payload: other}, openCodeGoTestOpts()); err != nil {
		t.Fatalf("Execute 3: %v", err)
	}
	if rec3.session == first {
		t.Fatalf("different turns produced the same session id %q", first)
	}
}

// TestOpenCodeGoExecutor_Execute_ClientSessionHeaderWins verifies an
// operator-supplied x-opencode-session (via the row's custom headers)
// overrides the synthesized fingerprint.
func TestOpenCodeGoExecutor_Execute_ClientSessionHeaderWins(t *testing.T) {
	srv, rec := newOpenCodeGoTestServer(t, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocgo",
			BaseURL: srv.URL,
			Headers: map[string]string{
				"x-opencode-session": "my-pinned-session",
				"User-Agent":         "opencode-cli/1.2.3",
			},
			Models: []config.OpenCodeGoModel{{Name: "glm-5.2"}},
		}},
	}
	e := NewOpenCodeGoExecutor(cfg)
	auth := openCodeGoTestAuth(srv.URL)
	// The synthesizer stamps custom headers into attrs as "header:*".
	for k, v := range cfg.OpenCodeGo[0].Headers {
		auth.Attributes["header:"+k] = v
	}
	if _, err := e.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
	}, openCodeGoTestOpts()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.session != "my-pinned-session" {
		t.Errorf("x-opencode-session = %q, want the client-supplied value", rec.session)
	}
	if rec.userAgent != "opencode-cli/1.2.3" {
		t.Errorf("User-Agent = %q, want the CLI UA preserved", rec.userAgent)
	}
}
