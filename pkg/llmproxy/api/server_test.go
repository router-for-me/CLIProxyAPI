package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/stretchr/testify/require"
)

func TestNewServer(t *testing.T) {
	cfg := &config.Config{
		Port:  8080,
		Debug: true,
	}
	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	s := NewServer(cfg, authManager, accessManager, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	if s.engine == nil {
		t.Error("engine is nil")
	}

	if s.handlers == nil {
		t.Error("handlers is nil")
	}
}

func TestServer_RootEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Debug: true}
	s := NewServer(cfg, nil, nil, "config.yaml")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	s.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestWithMiddleware(t *testing.T) {
	called := false
	mw := func(c *gin.Context) {
		called = true
		c.Next()
	}

	cfg := &config.Config{Debug: true}
	s := NewServer(cfg, nil, nil, "config.yaml", WithMiddleware(mw))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	s.engine.ServeHTTP(w, req)

	if !called {
		t.Error("extra middleware was not called")
	}
}

func TestWithKeepAliveEndpoint(t *testing.T) {
	onTimeout := func() {
	}

	cfg := &config.Config{Debug: true}
	s := NewServer(cfg, nil, nil, "config.yaml", WithKeepAliveEndpoint(100*time.Millisecond, onTimeout))

	if !s.keepAliveEnabled {
		t.Error("keep-alive should be enabled")
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/keep-alive", nil)
	s.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	require.NoError(t, s.Stop(context.Background()))
}

func TestServer_SetupRoutes_IsIdempotent(t *testing.T) {
	cfg := &config.Config{Debug: true}
	s := NewServer(cfg, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	countRoute := func(method, path string) int {
		count := 0
		for _, r := range s.engine.Routes() {
			if r.Method == method && r.Path == path {
				count++
			}
		}
		return count
	}

	if got := countRoute(http.MethodGet, "/v1/responses"); got != 1 {
		t.Fatalf("expected 1 GET /v1/responses route, got %d", got)
	}
	if got := countRoute(http.MethodPost, "/v1/responses"); got != 1 {
		t.Fatalf("expected 1 POST /v1/responses route, got %d", got)
	}
	if got := countRoute(http.MethodGet, "/v1/models"); got != 1 {
		t.Fatalf("expected 1 GET /v1/models route, got %d", got)
	}
	if got := countRoute(http.MethodGet, "/v1/metrics/providers"); got != 1 {
		t.Fatalf("expected 1 GET /v1/metrics/providers route, got %d", got)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("setupRoutes panicked on idempotent call: %v", recovered)
		}
	}()
	s.setupRoutes()
}

func TestServer_SetupRoutes_DuplicateInvocationPreservesRouteCount(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	countRoute := func(method, path string) int {
		count := 0
		for _, r := range s.engine.Routes() {
			if r.Method == method && r.Path == path {
				count++
			}
		}
		return count
	}

	beforeResp := countRoute(http.MethodGet, "/v1/responses") + countRoute(http.MethodPost, "/v1/responses")
	beforeSvc := countRoute(http.MethodGet, "/v1/models") + countRoute(http.MethodGet, "/v1/metrics/providers")

	s.setupRoutes()

	afterResp := countRoute(http.MethodGet, "/v1/responses") + countRoute(http.MethodPost, "/v1/responses")
	afterSvc := countRoute(http.MethodGet, "/v1/models") + countRoute(http.MethodGet, "/v1/metrics/providers")
	if afterResp != beforeResp {
		t.Fatalf("/v1/responses route count changed after re-setup: before=%d after=%d", beforeResp, afterResp)
	}
	if afterSvc != beforeSvc {
		t.Fatalf("service routes changed after re-setup: before=%d after=%d", beforeSvc, afterSvc)
	}
}

func TestServer_AttachWebsocketRoute_IsIdempotent(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	wsPath := "/v1/internal/ws-dup"
	s.AttachWebsocketRoute(wsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	s.AttachWebsocketRoute(wsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, wsPath, nil)
	s.engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("unexpected status from ws route: got %d want %d", resp.Code, http.StatusNoContent)
	}

	const method = http.MethodGet
	count := 0
	for _, route := range s.engine.Routes() {
		if route.Method == method && route.Path == wsPath {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected websocket route to be registered once, got %d", count)
	}
}

func TestServer_RoutesNamespaceIsolation(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	for _, r := range s.engine.Routes() {
		if strings.HasPrefix(r.Path, "/agent/") {
			t.Fatalf("unexpected control-plane /agent route overlap: %s %s", r.Method, r.Path)
		}
	}
}

func TestServer_ResponsesRouteSupportsHttpAndWebsocketShapes(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	getResp := httptest.NewRecorder()
	s.engine.ServeHTTP(getResp, getReq)
	if got := getResp.Code; got != http.StatusBadRequest {
		t.Fatalf("GET /v1/responses should be websocket-capable and return 400 without upgrade, got %d", got)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	postResp := httptest.NewRecorder()
	s.engine.ServeHTTP(postResp, postReq)
	if postResp.Code == http.StatusNotFound {
		t.Fatalf("POST /v1/responses should exist")
	}
}

func TestServer_StartupSmokeEndpoints(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	t.Run("GET /v1/models", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		resp := httptest.NewRecorder()
		s.engine.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("GET /v1/models expected 200, got %d", resp.Code)
		}
		var body struct {
			Object string            `json:"object"`
			Data   []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON from /v1/models: %v", err)
		}
		if body.Object != "list" {
			t.Fatalf("expected /v1/models object=list, got %q", body.Object)
		}
		_ = body.Data
	})

	t.Run("GET /v1/metrics/providers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/metrics/providers", nil)
		resp := httptest.NewRecorder()
		s.engine.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("GET /v1/metrics/providers expected 200, got %d", resp.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON from /v1/metrics/providers: %v", err)
		}
		_ = body
	})
}

func TestServer_StartupSmokeEndpoints_UserAgentVariants(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	for _, tc := range []struct {
		name       string
		userAgent  string
		minEntries int
	}{
		{name: "openai-compatible default", userAgent: "", minEntries: 1},
		{name: "claude-cli user-agent", userAgent: "claude-cli/1.0", minEntries: 0},
		{name: "CLAUDE-CLI uppercase user-agent", userAgent: "Claude-CLI/1.0", minEntries: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if tc.userAgent != "" {
				req.Header.Set("User-Agent", tc.userAgent)
			}
			resp := httptest.NewRecorder()
			s.engine.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("GET /v1/models expected 200, got %d", resp.Code)
			}

			var body struct {
				Object string `json:"object"`
				Data   []any  `json:"data"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON from /v1/models: %v", err)
			}
			if body.Object != "list" {
				t.Fatalf("expected /v1/models object=list, got %q", body.Object)
			}
			if len(body.Data) < tc.minEntries {
				t.Fatalf("expected at least %d models, got %d", tc.minEntries, len(body.Data))
			}
		})
	}
}

func TestServer_StartupSmokeEndpoints_MetricsShapeIncludesKnownProvider(t *testing.T) {
	stats := coreusage.GetRequestStatistics()
	ctx := context.Background()
	stats.Record(ctx, sdkusage.Record{
		APIKey: "nim",
		Model:  "gpt-4.1-nano",
		Detail: sdkusage.Detail{TotalTokens: 77},
	})

	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics/providers", nil)
	resp := httptest.NewRecorder()
	s.engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /v1/metrics/providers expected 200, got %d", resp.Code)
	}

	var body map[string]map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON from /v1/metrics/providers: %v", err)
	}
	metrics, ok := body["nim"]
	if !ok {
		t.Fatalf("expected nim provider in metrics payload, got keys=%s", strings.Join(sortedMetricKeys(body), ","))
	}
	for _, field := range []string{"request_count", "success_count", "failure_count", "success_rate", "cost_per_1k_input", "cost_per_1k_output"} {
		if _, exists := metrics[field]; !exists {
			t.Fatalf("expected metric field %q for nim", field)
		}
	}
	requestCount, _ := metrics["request_count"].(float64)
	if requestCount < 1 {
		t.Fatalf("expected positive request_count for nim, got %v", requestCount)
	}
}

func sortedMetricKeys(m map[string]map[string]any) []string {
	if len(m) == 0 {
		return []string{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestServer_ControlPlane_MessageLifecycle(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	t.Run("POST /message creates session and returns accepted event context", func(t *testing.T) {
		reqBody := `{"message":"hello from client","capability":"continue"}`
		req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.engine.ServeHTTP(resp, req)
		if resp.Code != http.StatusAccepted {
			t.Fatalf("POST /message expected %d, got %d", http.StatusAccepted, resp.Code)
		}

		var body struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON from /message: %v", err)
		}
		if body.SessionID == "" {
			t.Fatal("expected non-empty session_id")
		}
		if body.Status != "done" {
			t.Fatalf("expected status=done, got %q", body.Status)
		}

		msgReq := httptest.NewRequest(http.MethodGet, "/messages?session_id="+body.SessionID, nil)
		msgResp := httptest.NewRecorder()
		s.engine.ServeHTTP(msgResp, msgReq)
		if msgResp.Code != http.StatusOK {
			t.Fatalf("GET /messages expected 200, got %d", msgResp.Code)
		}

		var msgBody struct {
			SessionID string `json:"session_id"`
			Messages  []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(msgResp.Body.Bytes(), &msgBody); err != nil {
			t.Fatalf("invalid JSON from /messages: %v", err)
		}
		if msgBody.SessionID != body.SessionID {
			t.Fatalf("expected session_id %q, got %q", body.SessionID, msgBody.SessionID)
		}
		if len(msgBody.Messages) != 1 || msgBody.Messages[0].Content != "hello from client" {
			t.Fatalf("expected single message content, got %#v", msgBody.Messages)
		}
	})

	t.Run("GET /status without session_id", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		s.engine.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("GET /status expected %d, got %d", http.StatusBadRequest, resp.Code)
		}
	})

	t.Run("GET /events emits status event", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(`{"message":"status probe"}`))
		req.Header.Set("Content-Type", "application/json")
		msgResp := httptest.NewRecorder()
		s.engine.ServeHTTP(msgResp, req)
		if msgResp.Code != http.StatusAccepted {
			t.Fatalf("POST /message expected %d, got %d", http.StatusAccepted, msgResp.Code)
		}
		var msg struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(msgResp.Body.Bytes(), &msg); err != nil {
			t.Fatalf("invalid JSON from /message: %v", err)
		}
		if msg.SessionID == "" {
			t.Fatal("expected session_id")
		}

		reqEvt := httptest.NewRequest(http.MethodGet, "/events?session_id="+msg.SessionID, nil)
		respEvt := httptest.NewRecorder()
		s.engine.ServeHTTP(respEvt, reqEvt)
		if respEvt.Code != http.StatusOK {
			t.Fatalf("GET /events expected %d, got %d", http.StatusOK, respEvt.Code)
		}
		if ct := respEvt.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("expected content-type text/event-stream, got %q", ct)
		}
		if !strings.Contains(respEvt.Body.String(), "data: {") {
			t.Fatalf("expected SSE payload, got %q", respEvt.Body.String())
		}
	})
}

func TestServer_ControlPlane_UnsupportedCapability(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(`{"message":"x","capability":"pause"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("expected status %d for unsupported capability, got %d", http.StatusNotImplemented, resp.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON from /message: %v", err)
	}
	if _, ok := body["unsupported capability"]; ok {
		t.Fatalf("error payload has wrong schema: %v", body)
	}
	if body["error"] != "unsupported capability" {
		t.Fatalf("expected unsupported capability error, got %v", body["error"])
	}
}

func TestServer_ControlPlane_NormalizeCapabilityAliases(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	for _, capability := range []string{"continue", "resume", "ask", "exec", "max"} {
		t.Run(capability, func(t *testing.T) {
			reqBody := `{"message":"alias test","capability":"` + capability + `"}`
			req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			s.engine.ServeHTTP(resp, req)
			if resp.Code != http.StatusAccepted {
				t.Fatalf("capability=%s expected %d, got %d", capability, http.StatusAccepted, resp.Code)
			}
			var body struct {
				SessionID    string `json:"session_id"`
				Status       string `json:"status"`
				MessageID    string `json:"message_id"`
				MessageCount int    `json:"message_count"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON from /message for %s: %v", capability, err)
			}
			if body.SessionID == "" {
				t.Fatalf("expected non-empty session_id for capability %s", capability)
			}
			if body.Status != "done" {
				t.Fatalf("expected status=done for capability %s, got %q", capability, body.Status)
			}
			if body.MessageID == "" {
				t.Fatalf("expected message_id for capability %s", capability)
			}
			if body.MessageCount != 1 {
				t.Fatalf("expected message_count=1 for capability %s, got %d", capability, body.MessageCount)
			}
		})
	}
}

func TestNormalizeControlPlaneCapability(t *testing.T) {
	tcs := []struct {
		name        string
		input       string
		normalized  string
		isSupported bool
	}{
		{name: "empty accepted", input: "", normalized: "", isSupported: true},
		{name: "continue canonical", input: "continue", normalized: "continue", isSupported: true},
		{name: "resume canonical", input: "resume", normalized: "resume", isSupported: true},
		{name: "ask alias", input: "ask", normalized: "continue", isSupported: true},
		{name: "exec alias", input: "exec", normalized: "continue", isSupported: true},
		{name: "max alias", input: "max", normalized: "continue", isSupported: true},
		{name: "max with spaces", input: "  MAX  ", normalized: "continue", isSupported: true},
		{name: "mixed-case", input: "ExEc", normalized: "continue", isSupported: true},
		{name: "unsupported", input: "pause", normalized: "pause", isSupported: false},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeControlPlaneCapability(tc.input)
			if ok != tc.isSupported {
				t.Fatalf("input=%q expected ok=%v, got=%v", tc.input, tc.isSupported, ok)
			}
			if got != tc.normalized {
				t.Fatalf("input=%q expected normalized=%q, got=%q", tc.input, tc.normalized, got)
			}
		})
	}
}

func TestServer_ControlPlane_NamespaceAndMethodIsolation(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	countRoute := func(method, path string) int {
		count := 0
		for _, r := range s.engine.Routes() {
			if r.Method == method && r.Path == path {
				count++
			}
		}
		return count
	}

	if got := countRoute(http.MethodGet, "/messages"); got != 1 {
		t.Fatalf("expected one GET /messages route for control-plane status lookup, got %d", got)
	}
	if got := countRoute(http.MethodPost, "/v1/messages"); got != 1 {
		t.Fatalf("expected one POST /v1/messages route for model plane, got %d", got)
	}

	notExpected := map[string]struct{}{
		http.MethodGet + " /agent/messages": {},
		http.MethodGet + " /agent/status":   {},
		http.MethodGet + " /agent/events":   {},
		http.MethodPost + " /agent/message": {},
	}
	for _, r := range s.engine.Routes() {
		key := r.Method + " " + r.Path
		if _, ok := notExpected[key]; ok {
			t.Fatalf("unexpected /agent namespace route discovered: %s", key)
		}
	}
}

func TestServer_ControlPlane_IdempotencyKey_ReplaysResponseAndPreventsDuplicateMessages(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	const idempotencyKey = "idempotency-replay-key"
	const sessionID = "cp-replay-session"

	reqBody := `{"session_id":"` + sessionID + `","message":"replay me","capability":"continue"}`
	req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp := httptest.NewRecorder()
	s.engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("first POST /message expected %d, got %d", http.StatusAccepted, resp.Code)
	}
	var first struct {
		SessionID    string `json:"session_id"`
		MessageID    string `json:"message_id"`
		MessageCount int    `json:"message_count"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &first); err != nil {
		t.Fatalf("invalid JSON from first /message: %v", err)
	}
	if first.SessionID != sessionID {
		t.Fatalf("expected session_id=%q, got %q", sessionID, first.SessionID)
	}
	if first.MessageID == "" {
		t.Fatal("expected message_id in first response")
	}
	if first.MessageCount != 1 {
		t.Fatalf("expected message_count=1 on first request, got %d", first.MessageCount)
	}

	replayReq := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set("Idempotency-Key", idempotencyKey)
	replayResp := httptest.NewRecorder()
	s.engine.ServeHTTP(replayResp, replayReq)
	if replayResp.Code != http.StatusAccepted {
		t.Fatalf("replay POST /message expected %d, got %d", http.StatusAccepted, replayResp.Code)
	}

	var replay struct {
		SessionID    string `json:"session_id"`
		MessageID    string `json:"message_id"`
		MessageCount int    `json:"message_count"`
	}
	if err := json.Unmarshal(replayResp.Body.Bytes(), &replay); err != nil {
		t.Fatalf("invalid JSON from replay /message: %v", err)
	}
	if replay.SessionID != sessionID {
		t.Fatalf("expected replay session_id=%q, got %q", sessionID, replay.SessionID)
	}
	if replay.MessageID != first.MessageID {
		t.Fatalf("expected replay to reuse message_id %q, got %q", first.MessageID, replay.MessageID)
	}
	if replay.MessageCount != first.MessageCount {
		t.Fatalf("expected replay message_count=%d, got %d", first.MessageCount, replay.MessageCount)
	}

	msgReq := httptest.NewRequest(http.MethodGet, "/messages?session_id="+sessionID, nil)
	msgResp := httptest.NewRecorder()
	s.engine.ServeHTTP(msgResp, msgReq)
	if msgResp.Code != http.StatusOK {
		t.Fatalf("GET /messages expected %d, got %d", http.StatusOK, msgResp.Code)
	}
	var msgBody struct {
		Messages []struct {
			MessageID string `json:"message_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(msgResp.Body.Bytes(), &msgBody); err != nil {
		t.Fatalf("invalid JSON from /messages: %v", err)
	}
	if len(msgBody.Messages) != 1 {
		t.Fatalf("expected one stored message, got %d", len(msgBody.Messages))
	}
	if msgBody.Messages[0].MessageID != first.MessageID {
		t.Fatalf("expected stored message_id=%q, got %q", first.MessageID, msgBody.Messages[0].MessageID)
	}
}

func TestServer_ControlPlane_IdempotencyKey_DifferentKeysCreateDifferentMessages(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	const sessionID = "cp-replay-session-dupe"
	reqBody := `{"session_id":"` + sessionID + `","message":"first","capability":"continue"}`

	keyOneReq := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
	keyOneReq.Header.Set("Content-Type", "application/json")
	keyOneReq.Header.Set("Idempotency-Key", "dup-key-one")
	keyOneResp := httptest.NewRecorder()
	s.engine.ServeHTTP(keyOneResp, keyOneReq)
	if keyOneResp.Code != http.StatusAccepted {
		t.Fatalf("first message expected %d, got %d", http.StatusAccepted, keyOneResp.Code)
	}

	keyTwoReq := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
	keyTwoReq.Header.Set("Content-Type", "application/json")
	keyTwoReq.Header.Set("Idempotency-Key", "dup-key-two")
	keyTwoResp := httptest.NewRecorder()
	s.engine.ServeHTTP(keyTwoResp, keyTwoReq)
	if keyTwoResp.Code != http.StatusAccepted {
		t.Fatalf("second message expected %d, got %d", http.StatusAccepted, keyTwoResp.Code)
	}

	msgReq := httptest.NewRequest(http.MethodGet, "/messages?session_id="+sessionID, nil)
	msgResp := httptest.NewRecorder()
	s.engine.ServeHTTP(msgResp, msgReq)
	if msgResp.Code != http.StatusOK {
		t.Fatalf("GET /messages expected %d, got %d", http.StatusOK, msgResp.Code)
	}
	var msgBody struct {
		Messages []struct {
			MessageID string `json:"message_id"`
			Content   string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(msgResp.Body.Bytes(), &msgBody); err != nil {
		t.Fatalf("invalid JSON from /messages: %v", err)
	}
	if len(msgBody.Messages) != 2 {
		t.Fatalf("expected two stored messages for different idempotency keys, got %d", len(msgBody.Messages))
	}
	if msgBody.Messages[0].MessageID == msgBody.Messages[1].MessageID {
		t.Fatalf("expected unique message IDs for different idempotency keys")
	}
}

func TestServer_ControlPlane_SessionReadFallsBackToMirrorWithoutPrimary(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	sessionID := "cp-mirror-session"
	reqBody := `{"session_id":"` + sessionID + `","message":"mirror test","capability":"continue"}`
	req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("POST /message expected %d, got %d", http.StatusAccepted, resp.Code)
	}

	s.controlPlaneSessionsMu.Lock()
	delete(s.controlPlaneSessions, sessionID)
	s.controlPlaneSessionsMu.Unlock()

	getReq := httptest.NewRequest(http.MethodGet, "/messages?session_id="+sessionID, nil)
	getResp := httptest.NewRecorder()
	s.engine.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET /messages expected %d from mirror fallback, got %d", http.StatusOK, getResp.Code)
	}
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(getResp.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON from /messages: %v", err)
	}
	if len(body.Messages) != 1 || body.Messages[0].Content != "mirror test" {
		t.Fatalf("expected mirror-backed message payload, got %v", body.Messages)
	}
}

func TestServer_ControlPlane_ConflictBranchesPreservePreviousPayload(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	sessionID := "cp-conflict-session"

	for _, msg := range []string{"first", "second"} {
		reqBody := `{"session_id":"` + sessionID + `","message":"` + msg + `","capability":"continue"}`
		req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.engine.ServeHTTP(resp, req)
		if resp.Code != http.StatusAccepted {
			t.Fatalf("POST /message for %q expected %d, got %d", msg, http.StatusAccepted, resp.Code)
		}
	}

	s.controlPlaneSessionsMu.RLock()
	conflicts := s.controlPlaneSessionHistory[sessionID]
	current := s.controlPlaneSessions[sessionID]
	s.controlPlaneSessionsMu.RUnlock()

	if current == nil || len(current.Messages) != 2 {
		t.Fatalf("expected current session with two messages, got %#v", current)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected one historical conflict snapshot after second update, got %d", len(conflicts))
	}
	if len(conflicts[0].Messages) != 1 || conflicts[0].Messages[0].Content != "first" {
		t.Fatalf("expected first payload preserved in conflict history, got %#v", conflicts[0])
	}
}

func TestServer_ControlPlane_MessagesEndpointReturnsCopy(t *testing.T) {
	s := NewServer(&config.Config{Debug: true}, nil, nil, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}

	sessionID := "cp-copy-session"
	reqBody := `{"session_id":"` + sessionID + `","message":"immutable","capability":"continue"}`
	req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("POST /message expected %d, got %d", http.StatusAccepted, resp.Code)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/messages?session_id="+sessionID, nil)
	getResp := httptest.NewRecorder()
	s.engine.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET /messages expected %d, got %d", http.StatusOK, getResp.Code)
	}
	var first struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(getResp.Body.Bytes(), &first); err != nil {
		t.Fatalf("invalid JSON from /messages: %v", err)
	}
	if len(first.Messages) == 0 {
		t.Fatalf("expected one message")
	}
	first.Messages[0]["content"] = "tampered"

	getReq2 := httptest.NewRequest(http.MethodGet, "/messages?session_id="+sessionID, nil)
	getResp2 := httptest.NewRecorder()
	s.engine.ServeHTTP(getResp2, getReq2)
	if getResp2.Code != http.StatusOK {
		t.Fatalf("second GET /messages expected %d, got %d", http.StatusOK, getResp2.Code)
	}
	var second struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(getResp2.Body.Bytes(), &second); err != nil {
		t.Fatalf("invalid JSON from second /messages: %v", err)
	}
	if second.Messages[0].Content != "immutable" {
		t.Fatalf("expected stored message content to remain immutable, got %q", second.Messages[0].Content)
	}
}
