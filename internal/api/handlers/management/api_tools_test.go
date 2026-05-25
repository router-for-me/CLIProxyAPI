package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestInferAPICallAuthUsesHeaderKeyAndRequestURL(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	wrongBaseAuth := &coreauth.Auth{
		ID:       "claude-wrong-base",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "shared-key",
			"base_url": "https://a.example.com/anthropic",
		},
	}
	matchingAuth := &coreauth.Auth{
		ID:       "claude-matching-base",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "shared-key",
			"base_url": "https://b.example.com/anthropic",
		},
	}
	if _, errRegister := manager.Register(context.Background(), wrongBaseAuth); errRegister != nil {
		t.Fatalf("register wrong base auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), matchingAuth); errRegister != nil {
		t.Fatalf("register matching auth: %v", errRegister)
	}

	parsedURL, errParse := url.Parse("https://b.example.com/anthropic/v1/messages")
	if errParse != nil {
		t.Fatalf("parse URL: %v", errParse)
	}
	h := &Handler{authManager: manager}
	got := h.inferAPICallAuth(parsedURL, map[string]string{"x-api-key": "shared-key"})
	if got == nil {
		t.Fatal("expected inferred auth")
	}
	if got.ID != matchingAuth.ID {
		t.Fatalf("inferred auth ID = %q, want %q", got.ID, matchingAuth.ID)
	}
}

func TestInferAPICallAuthSupportsAuthorizationBearerFallback(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "openai-compat-auth",
		Provider: "compat",
		Attributes: map[string]string{
			"api_key": "compat-key",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	parsedURL, errParse := url.Parse("https://compat.example.com/v1/chat/completions")
	if errParse != nil {
		t.Fatalf("parse URL: %v", errParse)
	}
	h := &Handler{authManager: manager}
	got := h.inferAPICallAuth(parsedURL, map[string]string{"Authorization": "Bearer compat-key"})
	if got == nil {
		t.Fatal("expected inferred auth")
	}
	if got.ID != auth.ID {
		t.Fatalf("inferred auth ID = %q, want %q", got.ID, auth.ID)
	}
}

func TestAcquireAPICallHostSlotHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	parsedURL, errParse := url.Parse("https://limiter-test.example.com/v1/messages")
	if errParse != nil {
		t.Fatalf("parse URL: %v", errParse)
	}
	releases := make([]func(), 0, maxAPICallConcurrentPerHost)
	for i := 0; i < maxAPICallConcurrentPerHost; i++ {
		release, ok := acquireAPICallHostSlot(context.Background(), parsedURL)
		if !ok {
			t.Fatalf("slot %d was not acquired", i)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, ok := acquireAPICallHostSlot(ctx, parsedURL); ok {
		release()
		t.Fatal("expected canceled context to prevent slot acquisition")
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestAPICallAcceptsKebabAuthIndexForTokenSubstitution(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-live" {
			t.Fatalf("Authorization = %q, want Bearer sk-live", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude:apikey:test",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key": "sk-live",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	authIndex := auth.EnsureIndex()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"auth-index":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `","header":{"Authorization":"Bearer $TOKEN$"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h := &Handler{authManager: manager}
	h.APICall(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}

func TestAPICallReturnsJSONResponseWhenStreamDisabled(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		if got := string(body); got != `{"ping":"pong"}` {
			t.Fatalf("request body = %q, want %q", got, `{"ping":"pong"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "ok")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	recorder := performAPICallRequest(t, &Handler{}, apiCallRequest{
		Method: http.MethodPost,
		URL:    upstream.URL,
		Header: map[string]string{
			"Content-Type": "application/json",
		},
		Data: `{"ping":"pong"}`,
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response apiCallResponse
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Body != `{"ok":true}` {
		t.Fatalf("body = %q, want %q", response.Body, `{"ok":true}`)
	}
	if got := response.Header["X-Upstream"]; len(got) != 1 || got[0] != "ok" {
		t.Fatalf("header X-Upstream = %v, want [ok]", got)
	}
}

func TestAPICallRequestFailureReturnsSafeDetail(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	addr := listener.Addr().String()
	if errClose := listener.Close(); errClose != nil {
		t.Fatalf("close listener: %v", errClose)
	}

	recorder := performAPICallRequest(t, &Handler{}, apiCallRequest{
		Method: http.MethodPost,
		URL:    "http://" + addr,
	})

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	var body map[string]string
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if body["error"] != "request failed" {
		t.Fatalf("error = %q, want request failed", body["error"])
	}
	if strings.TrimSpace(body["detail"]) == "" {
		t.Fatalf("expected non-empty safe detail, got %q", body["detail"])
	}
	if strings.Contains(body["detail"], "http://") {
		t.Fatalf("detail should not include full request URL: %q", body["detail"])
	}
}

func TestAPICallStreamsNDJSONEventsWhenStreamEnabled(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: first\n\n"))
		_, _ = w.Write([]byte("data: second\n\n"))
	}))
	defer upstream.Close()

	recorder := performAPICallRequest(t, &Handler{}, apiCallRequest{
		Method: http.MethodPost,
		URL:    upstream.URL,
		Stream: true,
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Fatalf("content type = %q, want %q", got, "application/x-ndjson")
	}

	events := decodeAPICallStreamEvents(t, recorder.Body.Bytes())
	if len(events) < 3 {
		t.Fatalf("event count = %d, want at least 3", len(events))
	}
	if events[0].Type != "response" {
		t.Fatalf("event[0].type = %q, want response", events[0].Type)
	}
	if events[0].StatusCode != http.StatusOK {
		t.Fatalf("event[0].status_code = %d, want %d", events[0].StatusCode, http.StatusOK)
	}
	var chunkBody string
	for _, event := range events[1 : len(events)-1] {
		if event.Type != "chunk" {
			t.Fatalf("middle event.type = %q, want chunk", event.Type)
		}
		chunkBody += event.Chunk
	}
	if chunkBody != "data: first\n\ndata: second\n\n" {
		t.Fatalf("chunk body = %q, want %q", chunkBody, "data: first\n\ndata: second\n\n")
	}
	if events[len(events)-1].Type != "done" {
		t.Fatalf("last event.type = %q, want done", events[len(events)-1].Type)
	}
}

func TestAPICallStreamReturnsStatusErrorForUpstreamFailure(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer upstream.Close()

	recorder := performAPICallRequest(t, &Handler{}, apiCallRequest{
		Method: http.MethodPost,
		URL:    upstream.URL,
		Stream: true,
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	events := decodeAPICallStreamEvents(t, recorder.Body.Bytes())
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2; events=%+v", len(events), events)
	}
	if events[0].Type != "response" || events[0].StatusCode != http.StatusUnauthorized {
		t.Fatalf("event[0] = %+v, want unauthorized response", events[0])
	}
	if events[1].Type != "error" || events[1].Error != "HTTP 401: invalid api key" {
		t.Fatalf("event[1] = %+v, want status error event", events[1])
	}
}

func TestAPICallStreamReturnsErrorEventOnReadFailure(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		conn, buf, errHijack := hijacker.Hijack()
		if errHijack != nil {
			t.Fatalf("hijack: %v", errHijack)
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = buf.WriteString("Content-Type: text/event-stream\r\n")
		_, _ = buf.WriteString("Content-Length: 8\r\n")
		_, _ = buf.WriteString("\r\n")
		_, _ = buf.WriteString("data")
		_ = buf.Flush()
	}))
	defer upstream.Close()

	recorder := performAPICallRequest(t, &Handler{}, apiCallRequest{
		Method: http.MethodPost,
		URL:    upstream.URL,
		Stream: true,
	})

	events := decodeAPICallStreamEvents(t, recorder.Body.Bytes())
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	if events[0].Type != "response" {
		t.Fatalf("event[0].type = %q, want response", events[0].Type)
	}
	if events[1].Type != "chunk" || events[1].Chunk != "data" {
		t.Fatalf("event[1] = %+v, want partial chunk", events[1])
	}
	if events[2].Type != "error" || events[2].Error != "failed to read response" {
		t.Fatalf("event[2] = %+v, want error event", events[2])
	}
}

func performAPICallRequest(t *testing.T, h *Handler, payload apiCallRequest) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal payload: %v", errMarshal)
	}

	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.APICall(ctx)

	return recorder
}

func decodeAPICallStreamEvents(t *testing.T, body []byte) []apiCallStreamEvent {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(body), []byte{'\n'})
	events := make([]apiCallStreamEvent, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event apiCallStreamEvent
		if errUnmarshal := json.Unmarshal(line, &event); errUnmarshal != nil {
			t.Fatalf("unmarshal stream event %q: %v", string(line), errUnmarshal)
		}
		events = append(events, event)
	}
	return events
}
