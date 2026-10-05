package test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var upstreamWebsocketUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// contract describes one harness-facing protocol and how the fake upstream answers it.
type contract struct {
	name      string
	harness   string
	provider  string
	model     string
	path      string
	upstream  string
	request   func(stream bool) string
	nonStream upstreamReply
	stream    upstreamReply
	usage     usageExpectation
}

// usageExpectation is the usage the fixture reports, as the accounting event must record it.
type usageExpectation struct{ input, output, total int64 }

type upstreamReply struct {
	contentType string
	body        string
}

const weatherTool = `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`

var contracts = []contract{
	{
		name: "openai-chat-completions", harness: "grok", provider: "fixture-chat", model: "grok-fixture-chat",
		path: "/v1/chat/completions", upstream: "/chat/completions",
		request: func(stream bool) string {
			return fmt.Sprintf(`{"model":"grok-fixture-chat","stream":%t,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"weather in Oslo?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":%s}}]}`, stream, weatherTool)
		},
		nonStream: upstreamReply{"application/json", `{"id":"chatcmpl-fixture","object":"chat.completion","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`},
		stream: upstreamReply{"text/event-stream", sse(
			`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
			`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Oslo\"}"}}]}}]}`,
			`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
			`[DONE]`)},
		usage: usageExpectation{12, 5, 17},
	},
	{
		name: "openai-responses", harness: "codex", provider: "codex", model: "gpt-5.4",
		path: "/v1/responses", upstream: "/responses",
		request: func(stream bool) string {
			return fmt.Sprintf(`{"model":"gpt-5.4","stream":%t,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"weather in Oslo?"}]}],"tools":[{"type":"function","name":"get_weather","parameters":%s}]}`, stream, weatherTool)
		},
		nonStream: upstreamReply{"text/event-stream", responsesFixtureStream},
		stream:    upstreamReply{"text/event-stream", responsesFixtureStream},
		usage:     usageExpectation{20, 7, 27},
	},
	{
		name: "anthropic-messages", harness: "claude", provider: "claude", model: "claude-sonnet-fixture",
		path: "/v1/messages", upstream: "/v1/messages",
		request: func(stream bool) string {
			return fmt.Sprintf(`{"model":"claude-sonnet-fixture","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"weather in Oslo?"}],"tools":[{"name":"get_weather","description":"weather","input_schema":%s}]}`, stream, weatherTool)
		},
		nonStream: upstreamReply{"application/json", `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-sonnet-fixture","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Oslo"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":6,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}`},
		stream: upstreamReply{"text/event-stream", namedSSE(
			"message_start", `{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-sonnet-fixture","content":[],"stop_reason":null,"usage":{"input_tokens":11,"output_tokens":1,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}}`,
			"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
			"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Oslo\"}"}}`,
			"content_block_stop", `{"type":"content_block_stop","index":0}`,
			"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":6}}`,
			"message_stop", `{"type":"message_stop"}`)},
		usage: usageExpectation{17, 6, 23},
	},
}

var responsesFixtureEvents = []string{
	`{"type":"response.created","response":{"id":"resp_fixture","object":"response","status":"in_progress","model":"gpt-5.4"}}`,
	`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":""}}`,
	`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"city\":\"Oslo\"}"}`,
	`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"city\":\"Oslo\"}"}`,
	`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}`,
	`{"type":"response.completed","response":{"id":"resp_fixture","object":"response","status":"completed","model":"gpt-5.4","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}],"usage":{"input_tokens":20,"output_tokens":7,"total_tokens":27}}}`,
}

var responsesFixtureStream = sse(responsesFixtureEvents...)

func sse(events ...string) string {
	var out strings.Builder
	for _, event := range events {
		fmt.Fprintf(&out, "data: %s\n\n", event)
	}
	return out.String()
}

func namedSSE(pairs ...string) string {
	var out strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", pairs[i], pairs[i+1])
	}
	return out.String()
}

// upstreamCall is what the fake provider received from the gateway.
type upstreamCall struct {
	Account string
	Path    string
	Body    string
	Header  http.Header
}

// fakeUpstream is a provider fixture. Behavior is replaceable per test through handle.
type fakeUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []upstreamCall
	handle func(w http.ResponseWriter, r *http.Request, call upstreamCall) bool
	// canceled receives the account name whenever the gateway abandons an upstream request.
	canceled chan string
}

func newFakeUpstream(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, call upstreamCall) bool) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{handle: handle, canceled: make(chan string, 16)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if account == "" {
			account = r.Header.Get("X-Api-Key")
		}
		call := upstreamCall{Account: account, Path: r.URL.Path, Body: string(body), Header: r.Header.Clone()}
		if websocket.IsWebSocketUpgrade(r) {
			f.serveWebsocket(w, r, call)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		if f.handle != nil && f.handle(w, r, call) {
			return
		}
		f.serveContract(w, r, call)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// serveWebsocket answers one response.create with the Responses fixture events as text frames.
func (f *fakeUpstream) serveWebsocket(w http.ResponseWriter, r *http.Request, call upstreamCall) {
	conn, err := upstreamWebsocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	_, message, err := conn.ReadMessage()
	if err != nil {
		return
	}
	call.Body = string(message)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	for _, event := range responsesFixtureEvents {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
			return
		}
	}
	// A real provider keeps the session open for the next turn until the client leaves.
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (f *fakeUpstream) serveContract(w http.ResponseWriter, r *http.Request, call upstreamCall) {
	for _, c := range contracts {
		if !strings.HasSuffix(call.Path, c.upstream) {
			continue
		}
		reply := c.nonStream
		if strings.Contains(call.Body, `"stream":true`) {
			reply = c.stream
		}
		if c.name == "openai-responses" {
			reply = c.stream
		}
		w.Header().Set("Content-Type", reply.contentType)
		_, _ = io.WriteString(w, reply.body)
		return
	}
	http.NotFound(w, r)
}

func (f *fakeUpstream) snapshot() []upstreamCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upstreamCall(nil), f.calls...)
}

// holdStream sends head, flushes, then blocks until the gateway abandons the request.
func (f *fakeUpstream) holdStream(w http.ResponseWriter, r *http.Request, call upstreamCall, contentType, head string) {
	w.Header().Set("Content-Type", contentType)
	_, _ = io.WriteString(w, head)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	<-r.Context().Done()
	f.canceled <- call.Account
}

// observed is everything a harness client could see from one request.
type observed struct {
	Status int
	Header http.Header
	Body   string
	// Chunks counts the body reads that returned data, as a coarse streaming-shape check.
	Chunks int
}

var (
	uuidPattern    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	volatileHeader = map[string]bool{"Date": true, "Content-Length": true, "X-Request-Id": true, "X-Cpa-Trace-Id": true}
)

// shape strips only values that legitimately vary per request.
func (o observed) shape() string {
	names := make([]string, 0, len(o.Header))
	for name, values := range o.Header {
		if volatileHeader[name] {
			continue
		}
		names = append(names, name+"="+strings.Join(values, ","))
	}
	sort.Strings(names)
	return fmt.Sprintf("%d\n%s\n%s", o.Status, strings.Join(names, "\n"), uuidPattern.ReplaceAllString(o.Body, "<uuid>"))
}

func post(t *testing.T, ctx context.Context, url, body string, headers map[string]string) observed {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gatewayClientKey)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var buf bytes.Buffer
	chunks := 0
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			buf.Write(line)
			chunks++
		}
		if err != nil {
			break
		}
	}
	return observed{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: buf.String(), Chunks: chunks}
}

// readResponsesWebsocket sends one response.create and collects frames through the terminal event.
func readResponsesWebsocket(t *testing.T, gatewayURL, create string) []string {
	t.Helper()
	url := "ws" + strings.TrimPrefix(gatewayURL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": {"Bearer " + gatewayClientKey}})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(create)); err != nil {
		t.Fatal(err)
	}
	var frames []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket after %d frames: %v", len(frames), err)
		}
		frames = append(frames, string(payload))
		if strings.Contains(string(payload), `"type":"response.completed"`) || strings.Contains(string(payload), `"type":"error"`) {
			return frames
		}
	}
	t.Fatalf("websocket did not terminate; frames=%v", frames)
	return nil
}
