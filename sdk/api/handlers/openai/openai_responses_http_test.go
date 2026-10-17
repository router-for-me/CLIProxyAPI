package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResponsesHTTPContinuationSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	captures := make(chan []byte, 16)
	release := make(chan struct{})
	var released atomic.Bool
	var calls atomic.Int32
	// Match the existing GPT reasoning signature envelope validation.
	envelope := make([]byte, 1+8+16+16+32)
	envelope[0] = 0x80
	for i := 9; i < len(envelope); i++ {
		envelope[i] = byte(i)
	}
	encryptedReasoning := base64.RawURLEncoding.EncodeToString(envelope)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captures <- body
		if r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("continuation switched credential: %q", r.Header.Get("Authorization"))
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 2 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"incremental\"}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		output := `[{"type":"message","role":"assistant","id":"message-` + fmt.Sprint(n) + `","content":[{"type":"output_text","text":"answer"}]}]`
		if n == 1 {
			output = `[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{}"},{"type":"reasoning","id":"reasoning-1","encrypted_content":"` + encryptedReasoning + `","summary":[]}]`
		}
		// Terminal omits output on the streaming turn: the framer must repair it
		// from output_item.done before saving and before forwarding completion.
		if n == 2 {
			fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", gjson.Parse(output).Array()[0].Raw)
			output = `[]`
		}
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"http-r%d\",\"status\":\"completed\",\"output\":%s}}\n\n", n, output)
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	const authID = "http-continuation-socket-auth"
	const model = "http-continuation-socket-model"
	credential, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "synthetic-key", "base_url": upstream.URL}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	cfg.Payload = config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "http-continuation-socket-model", Protocol: "codex"}}, Params: map[string]any{"temperature": 0.123}}}, Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "http-continuation-socket-model", Protocol: "codex"}}, Params: []string{"parallel_tool_calls"}}}}
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Set("userApiKey", c.GetHeader("Test-Principal"))
		if c.GetHeader("Test-Limit") != "" {
			c.Set(handlers.MaxRequestBodyBytesContextKey, int64(250))
		}
		h.Responses(c)
	})
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(principal, body string, limit bool) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, downstream.URL+"/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Test-Principal", principal)
		if limit {
			req.Header.Set("Test-Limit", "yes")
		}
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return resp
	}
	read := func(resp *http.Response) []byte {
		t.Helper()
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	first := request("alice", `{"model":"`+model+`","input":"prior secret\n\"quoted\"","instructions":"old-only","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`, false)
	firstBody := read(first)
	if first.StatusCode != 200 || !gjson.GetBytes(firstBody, "store").Bool() {
		t.Fatalf("first=%d %s", first.StatusCode, firstBody)
	}
	<-captures
	_, err = manager.Register(context.Background(), &coreauth.Auth{ID: authID + "-other", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "other-key", "base_url": upstream.URL}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID+"-other", "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID + "-other")
	second := request("alice", `{"model":"`+model+`","previous_response_id":"http-r1","stream":true,"instructions":"current-only","input":[{"type":"function_call_output","call_id":"call-1","output":"tool-result"}]}`, false)
	if second.StatusCode != 200 {
		t.Fatalf("stream status=%d %s", second.StatusCode, read(second))
	}
	reader := bufio.NewReader(second.Body)
	var prefix strings.Builder
	for !strings.Contains(prefix.String(), "incremental") {
		line, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		prefix.WriteString(line)
	}
	// Receiving this while upstream is blocked proves terminal persistence does
	// not buffer deltas until the final response.
	released.Store(true)
	close(release)
	rest, e := io.ReadAll(reader)
	second.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	streamed := prefix.String() + string(rest)
	if !strings.Contains(streamed, `"store":true`) || !strings.Contains(streamed, `"previous_response_id":"http-r1"`) {
		t.Fatalf("stream metadata: %s", streamed)
	}
	secondInput := <-captures
	if text := gjson.GetBytes(secondInput, "input.0.content.0.text").String(); text != "prior secret\n\"quoted\"" {
		t.Fatalf("replayed string input changed escaping: %q", text)
	}
	if !bytes.Contains(secondInput, []byte("prior secret")) || gjson.GetBytes(secondInput, "input.2.encrypted_content").String() != encryptedReasoning || !bytes.Contains(secondInput, []byte("tool-result")) || bytes.Contains(secondInput, []byte("old-only")) || gjson.GetBytes(secondInput, "previous_response_id").Exists() {
		t.Fatalf("expanded request=%s", secondInput)
	}
	if gjson.GetBytes(secondInput, "temperature").Float() != 0.123 || gjson.GetBytes(secondInput, "parallel_tool_calls").Exists() {
		t.Fatalf("final payload rules overwritten: %s", secondInput)
	}
	third := request("alice", `{"model":"`+model+`","previous_response_id":"http-r2","input":"third"}`, false)
	thirdBody := read(third)
	if third.StatusCode != 200 || !gjson.GetBytes(thirdBody, "store").Bool() {
		t.Fatalf("third=%d %s", third.StatusCode, thirdBody)
	}
	thirdInput := <-captures
	if !bytes.Contains(thirdInput, []byte("message-2")) || bytes.Contains(thirdInput, []byte("current-only")) {
		t.Fatalf("repaired chain=%s", thirdInput)
	}
	for _, tc := range []struct {
		principal, id string
		limit         bool
		status        int
	}{{"bob", "http-r1", false, 400}, {"alice", "unknown", false, 400}, {"alice", "http-r1", true, 413}} {
		before := calls.Load()
		resp := request(tc.principal, `{"model":"`+model+`","previous_response_id":"`+tc.id+`","stream":true,"input":"delta"}`, tc.limit)
		b := read(resp)
		if resp.StatusCode != tc.status || calls.Load() != before || strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
			t.Fatalf("rejection=%d calls=%d %s", resp.StatusCode, calls.Load()-before, b)
		}
		if tc.status == 400 && gjson.GetBytes(b, "error.code").String() != "previous_response_not_found" {
			t.Fatalf("missing error=%s", b)
		}
	}
	fork := request("alice", `{"model":"`+model+`","previous_response_id":"http-r1","store":false,"input":"fork"}`, false)
	forkBody := read(fork)
	forkInput := <-captures
	if fork.StatusCode != 200 || gjson.GetBytes(forkBody, "store").Bool() || bytes.Contains(forkInput, []byte("third")) || bytes.Contains(forkInput, []byte("message-2")) {
		t.Fatalf("fork=%s input=%s", forkBody, forkInput)
	}
	missing := request("alice", `{"model":"`+model+`","previous_response_id":"`+gjson.GetBytes(forkBody, "id").String()+`","input":"cannot continue"}`, false)
	if missing.StatusCode != 400 {
		t.Fatalf("store false continuation=%d", missing.StatusCode)
	}
	read(missing)
	disabled := credential.Clone()
	disabled.Disabled = true
	disabled.Status = coreauth.StatusDisabled
	if _, err = manager.Update(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	blocked := request("alice", `{"model":"`+model+`","previous_response_id":"http-r1","input":"must not switch accounts"}`, false)
	blockedBody := read(blocked)
	if blocked.StatusCode < 400 || calls.Load() != before {
		t.Fatalf("disabled pin silently fell back: %d %s", blocked.StatusCode, blockedBody)
	}
}

func TestResponsesHTTPContinuationMetadata(t *testing.T) {
	h := NewOpenAIResponsesAPIHandler(nil)
	cases := []struct {
		name, status, auth string
		store, want        bool
	}{{"completed", "completed", "auth", true, true}, {"no credential", "completed", "", true, false}, {"failed", "failed", "auth", true, false}, {"incomplete", "incomplete", "auth", true, false}, {"opt out", "completed", "auth", false, false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &responsesHTTPExecution{handler: h, scope: tc.name, input: []byte(`[]`), model: "model", store: tc.store, authID: tc.auth}
			b := s.response([]byte(`{"id":"r","status":"`+tc.status+`","output":[]}`), true)
			if gjson.GetBytes(b, "store").Bool() != tc.want {
				t.Fatalf("response=%s", b)
			}
			_, ok := h.httpContinuations.load(tc.name, "r")
			if ok != tc.want {
				t.Fatalf("retained=%v", ok)
			}
		})
	}
}

func TestResponsesHTTPContinuationScopeAndExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIResponsesAPIHandler(nil)
	now := time.Unix(100, 0)
	h.httpContinuations.now = func() time.Time { return now }
	h.httpContinuations.ttl = time.Second
	contextFor := func(key, provider string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", nil)
		c.Set("userApiKey", key)
		c.Set("accessProvider", provider)
		return c
	}
	alice := contextFor("secret", "provider")
	scope := responsesHTTPCallerScope(alice)
	if strings.Contains(scope, "secret") || scope == responsesHTTPCallerScope(contextFor("secret", "other")) {
		t.Fatal("scope not isolated/hashed")
	}
	if !h.httpContinuations.save(scope, "r", responsesHTTPContinuation{Input: []byte(`[]`), Output: []byte(`[]`), AuthID: "auth", Model: "model"}) {
		t.Fatal("save")
	}
	raw, ok := h.prepareResponsesHTTP(alice, []byte(`{"previous_response_id":"r","input":"hello","tools":[]}`))
	if !ok {
		t.Fatal("prepare")
	}
	if !json.Valid(raw) || responsesHTTPState(alice).pinnedID != "auth" || gjson.GetBytes(raw, "model").String() != "model" {
		t.Fatalf("pin/model/request=%s", raw)
	}
	now = now.Add(2 * time.Second)
	if _, ok = h.prepareResponsesHTTP(contextFor("secret", "provider"), []byte(`{"previous_response_id":"r","input":[]}`)); ok {
		t.Fatal("expired response accepted")
	}
}
