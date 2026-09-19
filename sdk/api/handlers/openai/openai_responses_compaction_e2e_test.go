package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	internalutil "github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type openAICompatResponsesE2ERequest struct {
	path string
	body []byte
}

type openAICompatResponsesE2EUpstream struct {
	mu       sync.Mutex
	requests []openAICompatResponsesE2ERequest
}

func (u *openAICompatResponsesE2EUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, errRead := io.ReadAll(r.Body)
	if errRead != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	u.mu.Lock()
	u.requests = append(u.requests, openAICompatResponsesE2ERequest{path: r.URL.Path, body: append([]byte(nil), body...)})
	u.mu.Unlock()

	switch r.URL.Path {
	case "/v1/chat/completions":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-e2e","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"summary text"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	case "/v1/responses/compact":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (u *openAICompatResponsesE2EUpstream) snapshot() []openAICompatResponsesE2ERequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	requests := make([]openAICompatResponsesE2ERequest, len(u.requests))
	for i := range u.requests {
		requests[i] = openAICompatResponsesE2ERequest{
			path: u.requests[i].path,
			body: append([]byte(nil), u.requests[i].body...),
		}
	}
	return requests
}

func TestOpenAICompatResponsesCompactionHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openAICompatResponsesE2EUpstream{}
	upstreamServer := httptest.NewServer(upstream)
	defer upstreamServer.Close()

	const model = "e2e-openai-compat-compaction-model"
	passThroughRouter := newOpenAICompatResponsesE2ERouter(t, upstreamServer.URL, model, false, "pass-through")

	triggerPayload := []byte(fmt.Sprintf(`{"model":%q,"stream":false,"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`, model))
	case1Response := serveOpenAICompatResponsesE2E(t, passThroughRouter, "/v1/responses", triggerPayload)
	if case1Response.Code != http.StatusOK {
		t.Fatalf("case 1 status = %d, want %d; response=%s", case1Response.Code, http.StatusOK, case1Response.Body.String())
	}
	assertOpenAICompatResponsesE2EOutput(t, []byte(case1Response.Body.String()))
	if got := gjson.Get(case1Response.Body.String(), "object").String(); got != "response" {
		t.Fatalf("case 1 object = %q, want response; response=%s", got, case1Response.Body.String())
	}
	if got := gjson.Get(case1Response.Body.String(), "status").String(); got != "completed" {
		t.Fatalf("case 1 status field = %q, want completed; response=%s", got, case1Response.Body.String())
	}
	capsule := gjson.Get(case1Response.Body.String(), "output.0.encrypted_content").String()
	if capsule == "" {
		t.Fatalf("case 1 capsule is empty; response=%s", case1Response.Body.String())
	}
	case1Requests := upstream.snapshot()
	if len(case1Requests) != 1 || case1Requests[0].path != "/v1/chat/completions" {
		t.Fatalf("case 1 upstream requests = %#v, want one chat/completions request", case1Requests)
	}
	if strings.Contains(string(case1Requests[0].body), "compaction_trigger") {
		t.Fatalf("case 1 trigger reached upstream: %s", case1Requests[0].body)
	}
	t.Logf("case 1 upstream request body: %s", case1Requests[0].body)
	t.Logf("case 1 response body: %s", case1Response.Body.String())

	streamPayload := []byte(fmt.Sprintf(`{"model":%q,"stream":true,"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`, model))
	case2Response := serveOpenAICompatResponsesE2E(t, passThroughRouter, "/v1/responses", streamPayload)
	if case2Response.Code != http.StatusOK {
		t.Fatalf("case 2 status = %d, want %d; response=%s", case2Response.Code, http.StatusOK, case2Response.Body.String())
	}
	case2Frames := parseOpenAICompatResponsesE2ESSE(t, case2Response.Body.String())
	if len(case2Frames) == 0 || case2Frames[len(case2Frames)-1].event != "response.completed" {
		t.Fatalf("case 2 events do not end with response.completed: %q", case2Response.Body.String())
	}
	terminal := case2Frames[len(case2Frames)-1].data
	assertOpenAICompatResponsesE2EOutput(t, []byte(gjson.GetBytes(terminal, "response").Raw))

	replayPayload := []byte(fmt.Sprintf(`{"model":%q,"input":[{"type":"compaction","encrypted_content":%q},{"type":"message","role":"user","content":"next task"}]}`, model, capsule))
	case3Response := serveOpenAICompatResponsesE2E(t, passThroughRouter, "/v1/responses", replayPayload)
	if case3Response.Code != http.StatusOK {
		t.Fatalf("case 3 status = %d, want %d; response=%s", case3Response.Code, http.StatusOK, case3Response.Body.String())
	}
	case3Requests := upstream.snapshot()
	if len(case3Requests) != 3 || case3Requests[2].path != "/v1/chat/completions" {
		t.Fatalf("case 3 upstream requests = %#v, want third request to chat/completions", case3Requests)
	}
	case3Body := string(case3Requests[2].body)
	if !strings.Contains(case3Body, "Context summary from previous turns:") && !strings.Contains(case3Body, "summary text") {
		t.Fatalf("case 3 summary context missing from upstream request: %s", case3Body)
	}
	if strings.Contains(case3Body, `"type":"compaction"`) || strings.Contains(case3Body, capsule) {
		t.Fatalf("case 3 opaque compaction item reached upstream: %s", case3Body)
	}
	t.Logf("case 3 upstream request body: %s", case3Requests[2].body)
	t.Logf("case 3 response body: %s", case3Response.Body.String())

	case4Payload := []byte(fmt.Sprintf(`{"model":%q,"input":[{"type":"message","role":"user","content":"compact"}]}`, model))
	case4Response := serveOpenAICompatResponsesE2E(t, passThroughRouter, "/v1/responses/compact", case4Payload)
	if case4Response.Code != http.StatusNotFound {
		t.Fatalf("case 4 status = %d, want %d; response=%s", case4Response.Code, http.StatusNotFound, case4Response.Body.String())
	}
	if case4Response.Body.String() != `{"detail":"Not Found"}` {
		t.Fatalf("case 4 body = %q, want upstream 404 body", case4Response.Body.String())
	}

	synthesizeRouter := newOpenAICompatResponsesE2ERouter(t, upstreamServer.URL, model, true, "synthesize")
	compactCallsBeforeCase5 := countOpenAICompatResponsesE2EPath(upstream.snapshot(), "/v1/responses/compact")
	case5Response := serveOpenAICompatResponsesE2E(t, synthesizeRouter, "/v1/responses/compact", case4Payload)
	if case5Response.Code != http.StatusOK {
		t.Fatalf("case 5 status = %d, want %d; response=%s", case5Response.Code, http.StatusOK, case5Response.Body.String())
	}
	assertOpenAICompatResponsesE2EOutput(t, []byte(case5Response.Body.String()))
	compactCallsAfterCase5 := countOpenAICompatResponsesE2EPath(upstream.snapshot(), "/v1/responses/compact")
	if compactCallsAfterCase5 != compactCallsBeforeCase5 {
		t.Fatalf("case 5 called upstream /responses/compact: before=%d after=%d", compactCallsBeforeCase5, compactCallsAfterCase5)
	}
	case5Requests := upstream.snapshot()
	if len(case5Requests) == 0 || case5Requests[len(case5Requests)-1].path != "/v1/chat/completions" {
		t.Fatalf("case 5 last upstream request = %#v, want chat/completions", case5Requests)
	}
	t.Logf("case 5 upstream request body: %s", case5Requests[len(case5Requests)-1].body)
	t.Logf("case 5 response body: %s", case5Response.Body.String())

	capsule, errSeal := helps.SealAntigravityCompaction("foreign summary", model)
	if errSeal != nil {
		t.Fatalf("seal capsule: %v", errSeal)
	}
	requestCountBeforeCapsule := len(upstream.snapshot())
	capsulePayload := []byte(fmt.Sprintf(`{"model":%q,"input":[{"type":"compaction","encrypted_content":%q},{"type":"message","role":"user","content":"continue"}]}`, model, capsule))
	case6Response := serveOpenAICompatResponsesE2E(t, passThroughRouter, "/v1/responses", capsulePayload)
	if case6Response.Code != http.StatusOK {
		t.Fatalf("case 6 status = %d, want %d; response=%s", case6Response.Code, http.StatusOK, case6Response.Body.String())
	}
	case6Requests := upstream.snapshot()
	if len(case6Requests) != requestCountBeforeCapsule+1 {
		t.Fatalf("case 6 upstream request count = %d, want %d", len(case6Requests), requestCountBeforeCapsule+1)
	}
	// CPA capsules use one global format, so this router can open it: case 6 must inline the
	// summary and keep only the ciphertext out of the upstream request.
	case6Body := string(case6Requests[len(case6Requests)-1].body)
	if strings.Contains(case6Body, capsule) || strings.Contains(case6Body, `"type":"compaction"`) {
		t.Fatalf("case 6 cpa capsule reached upstream: %s", case6Body)
	}
	if !strings.Contains(case6Body, "foreign summary") {
		t.Fatalf("case 6 foreign capsule was dropped instead of inlined: %s", case6Body)
	}
	t.Logf("case 6 upstream request body: %s", case6Body)
}

func newOpenAICompatResponsesE2ERouter(t *testing.T, upstreamURL, model string, synthesize bool, suffix string) *gin.Engine {
	t.Helper()
	compatName := "compat-e2e-" + suffix
	provider := internalutil.OpenAICompatibleProviderKey(compatName)
	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:                          compatName,
			BaseURL:                       upstreamURL + "/v1",
			SynthesizeResponsesCompaction: synthesize,
		}},
	}
	executor := runtimeexecutor.NewOpenAICompatExecutor(provider, cfg)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-e2e-" + suffix,
		Provider: provider,
		Label:    compatName,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url":     upstreamURL + "/v1",
			"api_key":      "e2e-key",
			"compat_name":  compatName,
			"provider_key": provider,
			"config_index": "0",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/responses/compact", h.Compact)
	return router
}

func serveOpenAICompatResponsesE2E(t *testing.T, router http.Handler, path string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

type openAICompatResponsesE2EFrame struct {
	event string
	data  []byte
}

func parseOpenAICompatResponsesE2ESSE(t *testing.T, body string) []openAICompatResponsesE2EFrame {
	t.Helper()
	var frames []openAICompatResponsesE2EFrame
	for _, rawFrame := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if strings.TrimSpace(rawFrame) == "" {
			continue
		}
		var frame openAICompatResponsesE2EFrame
		for _, line := range strings.Split(rawFrame, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.data = []byte(strings.TrimPrefix(line, "data: "))
			}
		}
		if frame.event == "" || len(frame.data) == 0 {
			t.Fatalf("invalid SSE frame: %q", rawFrame)
		}
		frames = append(frames, frame)
	}
	return frames
}

func assertOpenAICompatResponsesE2EOutput(t *testing.T, payload []byte) {
	t.Helper()
	output := gjson.GetBytes(payload, "output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("output = %s, want exactly one item; payload=%s", output.Raw, payload)
	}
	item := output.Get("0")
	if item.Get("type").String() != "compaction" {
		t.Fatalf("output item = %s, want compaction; payload=%s", item.Raw, payload)
	}
	if item.Get("encrypted_content").String() == "" {
		t.Fatalf("output item has empty encrypted_content: %s", payload)
	}
}

func countOpenAICompatResponsesE2EPath(requests []openAICompatResponsesE2ERequest, path string) int {
	count := 0
	for _, request := range requests {
		if request.path == path {
			count++
		}
	}
	return count
}
