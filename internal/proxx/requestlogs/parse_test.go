package requestlogs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustMeta(t *testing.T, name string) fileMeta {
	t.Helper()
	meta, err := readMeta(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("readMeta(%s): %v", name, err)
	}
	if !meta.valid {
		t.Fatalf("readMeta(%s): not a valid request log", name)
	}
	return meta
}

func mustDetail(t *testing.T, path string) *Detail {
	t.Helper()
	d, err := readDetail(path)
	if err != nil {
		t.Fatalf("readDetail(%s): %v", path, err)
	}
	return d
}

func i64(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestParseNonStream(t *testing.T) {
	m := mustMeta(t, "openai-nonstream.log")
	if m.method != "POST" || m.path != "/v1/chat/completions" || m.status != 200 || m.stream {
		t.Fatalf("meta = %+v", m)
	}
	if m.requestedModel != "model-ok" || m.attempts != 1 || m.authProvider != "openai-compatible-pok" {
		t.Fatalf("model/attempts/provider = %q %d %q", m.requestedModel, m.attempts, m.authProvider)
	}
	if i64(m.tokens.in) != 3 || i64(m.tokens.out) != 4 {
		t.Fatalf("tokens = %d/%d", i64(m.tokens.in), i64(m.tokens.out))
	}
	if m.time.IsZero() {
		t.Fatal("timestamp not parsed")
	}
	d := mustDetail(t, filepath.Join("testdata", "openai-nonstream.log"))
	if !strings.Contains(d.Client.Body, `"model":"model-ok"`) || d.Client.Method != "POST" || d.Client.URL != "/v1/chat/completions" {
		t.Fatalf("client = %+v", d.Client)
	}
	if got := d.Client.Headers["Authorization"]; len(got) != 1 || got[0] != "Bearer te...ey" {
		t.Fatalf("masked auth header = %v", got)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].Status == nil || *d.Attempts[0].Status != 200 {
		t.Fatalf("attempts = %+v", d.Attempts)
	}
	a := d.Attempts[0]
	if !strings.HasSuffix(a.URL, "/pok/v1/chat/completions") || a.Method != "POST" || !strings.Contains(a.Body, `"model":"up-ok"`) || a.Timestamp == "" {
		t.Fatalf("attempt = %+v", a)
	}
	if !strings.Contains(a.ResponseBody, "hello from up-ok") || a.ResponseHeaders["Content-Type"][0] != "application/json" {
		t.Fatalf("attempt response = %q %v", a.ResponseBody, a.ResponseHeaders)
	}
	if d.Response.Status != 200 || !strings.HasPrefix(d.Response.Body, `{"id":"chatcmpl-1"`) || strings.HasSuffix(d.Response.Body, "\n") {
		t.Fatalf("response = %+v", d.Response)
	}
	if d.Response.SSEEvents != nil || d.Response.Assembled != nil {
		t.Fatal("non-stream response must have no SSE events / assembled")
	}
}

func TestParseOpenAIStream(t *testing.T) {
	m := mustMeta(t, "openai-stream.log")
	if !m.stream || m.status != 200 || i64(m.tokens.in) != 5 || i64(m.tokens.out) != 6 {
		t.Fatalf("meta = %+v tokens %d/%d", m, i64(m.tokens.in), i64(m.tokens.out))
	}
	d := mustDetail(t, filepath.Join("testdata", "openai-stream.log"))
	if len(d.Response.SSEEvents) != 5 || d.Response.SSEEvents[4].Data != "[DONE]" {
		t.Fatalf("events = %+v", d.Response.SSEEvents)
	}
	if d.Response.Assembled == nil || d.Response.Assembled.Text != "streamed hello from up-ok" {
		t.Fatalf("assembled = %+v", d.Response.Assembled)
	}
}

func TestParseFailover(t *testing.T) {
	m := mustMeta(t, "failover.log")
	if m.attempts != 2 || m.servedHeader != "model-ok" || m.requestedModel != "auto" || m.authProvider != "openai-compatible-pok" {
		t.Fatalf("meta = %+v", m)
	}
	d := mustDetail(t, filepath.Join("testdata", "failover.log"))
	if len(d.Attempts) != 2 {
		t.Fatalf("attempts = %d", len(d.Attempts))
	}
	first, second := d.Attempts[0], d.Attempts[1]
	if first.Index != 1 || *first.Status != 429 || !strings.Contains(first.URL, "/p429/") || first.ResponseHeaders["Retry-After"][0] != "30" {
		t.Fatalf("first = %+v", first)
	}
	if second.Index != 2 || *second.Status != 200 || !strings.Contains(second.URL, "/pok/") || !strings.Contains(second.ResponseBody, "hello from up-ok") {
		t.Fatalf("second = %+v", second)
	}
}

func TestParseErrorFile(t *testing.T) {
	m := mustMeta(t, "error-400.log")
	if m.status != 400 || m.attempts != 1 {
		t.Fatalf("meta = %+v", m)
	}
	d := mustDetail(t, filepath.Join("testdata", "error-400.log"))
	if len(d.Attempts) != 1 || *d.Attempts[0].Status != 400 || d.Attempts[0].ResponseHeaders["Content-Type"][0] != "application/json" {
		t.Fatalf("attempts = %+v", d.Attempts)
	}
	if !strings.Contains(d.Response.Body, "invalid parameter foo") || d.Response.Status != 400 {
		t.Fatalf("response = %+v", d.Response)
	}
}

// The *-v2.log fixtures were written by the upstream logger after v8.0.79, whose final RESPONSE
// section is "Status / [Error: ...] / blank / Headers: / ... / blank / Body: / ...".
func TestParseResponseLayoutV2(t *testing.T) {
	ns := mustMeta(t, "openai-nonstream-v2.log")
	if ns.status != 200 || ns.stream || ns.requestID == "" || i64(ns.tokens.in) != 3 || i64(ns.tokens.out) != 4 {
		t.Fatalf("non-stream meta = %+v tokens %d/%d", ns, i64(ns.tokens.in), i64(ns.tokens.out))
	}
	d := mustDetail(t, filepath.Join("testdata", "openai-nonstream-v2.log"))
	if d.Response.Status != 200 || d.Response.Headers["Content-Type"][0] != "application/json" ||
		!strings.HasPrefix(d.Response.Body, `{"id":"chatcmpl-1"`) || strings.HasSuffix(d.Response.Body, "\n") || len(d.Response.Errors) != 0 {
		t.Fatalf("non-stream response = %+v", d.Response)
	}
	if _, ok := d.Response.Headers["Headers:"]; ok {
		t.Fatal("layout marker parsed as a header")
	}

	st := mustMeta(t, "openai-stream-v2.log")
	if !st.stream || st.status != 200 || i64(st.tokens.in) != 5 || i64(st.tokens.out) != 6 {
		t.Fatalf("stream meta = %+v tokens %d/%d", st, i64(st.tokens.in), i64(st.tokens.out))
	}
	sd := mustDetail(t, filepath.Join("testdata", "openai-stream-v2.log"))
	if len(sd.Response.SSEEvents) != 5 || sd.Response.SSEEvents[4].Data != "[DONE]" ||
		sd.Response.Assembled == nil || sd.Response.Assembled.Text != "streamed hello from up-ok" {
		t.Fatalf("stream response = %+v", sd.Response)
	}

	fo := mustMeta(t, "failover-v2.log")
	if fo.attempts != 2 || fo.servedHeader != "model-ok" || fo.requestedModel != "auto" || fo.authProvider != "openai-compatible-pok" {
		t.Fatalf("failover meta = %+v", fo)
	}

	er := mustMeta(t, "error-400-v2.log")
	ed := mustDetail(t, filepath.Join("testdata", "error-400-v2.log"))
	if er.status != 400 || ed.Response.Status != 400 || !strings.HasPrefix(ed.Response.Body, `{"error":`) {
		t.Fatalf("error meta = %+v response = %+v", er, ed.Response)
	}
}

func TestResponseLayoutV2Diagnostics(t *testing.T) {
	head := "=== REQUEST INFO ===\nURL: /v1/responses\nMethod: POST\n\n\n=== REQUEST BODY ===\n{\"model\":\"m\"}\n\n\n"
	path := writeTemp(t, t.TempDir(), "d.log", head+"=== RESPONSE ===\nStatus: 502\nError: dial tcp: refused\n\nHeaders:\n<none>\n\nBody:\n")
	d := mustDetail(t, path)
	if d.Response.Status != 502 || len(d.Response.Headers) != 0 || d.Response.Body != "" ||
		len(d.Response.Errors) != 1 || d.Response.Errors[0] != "dial tcp: refused" {
		t.Fatalf("response = %+v", d.Response)
	}

	// Websocket transcripts get a diagnostics-only section: no status, headers or body.
	path = writeTemp(t, t.TempDir(), "ws.log", head+"=== RESPONSE ===\nError: stream reset\n")
	d = mustDetail(t, path)
	if d.Response.Status != 0 || len(d.Response.Headers) != 0 || d.Response.Body != "" ||
		len(d.Response.Errors) != 1 || d.Response.Errors[0] != "stream reset" {
		t.Fatalf("diagnostic-only response = %+v", d.Response)
	}
}

func TestLegacyResponseBodyStartingWithHeadersMarker(t *testing.T) {
	// Old layout: headers come before the blank line, so a body line "Headers:" stays body.
	body := "Headers:\nnot: a header"
	path := writeTemp(t, t.TempDir(), "l.log", logFile(`{"model":"m"}`, "text/plain", body, ""))
	d := mustDetail(t, path)
	if d.Response.Body != body || d.Response.Headers["Content-Type"][0] != "text/plain" {
		t.Fatalf("response = %+v", d.Response)
	}
}

// logFile renders a minimal request log in the request logger's format.
func logFile(reqBody, contentType, respBody string, extra string) string {
	var b strings.Builder
	b.WriteString("=== REQUEST INFO ===\nVersion: dev\nURL: /v1/messages\nMethod: POST\nTimestamp: 2026-11-16T12:00:00.5+05:30\n\n\n")
	b.WriteString("=== HEADERS ===\nContent-Type: application/json\n\n\n")
	b.WriteString("=== REQUEST BODY ===\n" + reqBody + "\n\n\n")
	b.WriteString(extra)
	b.WriteString("=== RESPONSE ===\nStatus: 200\nContent-Type: " + contentType + "\n\n" + respBody + "\n")
	return b.String()
}

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assembledFor(t *testing.T, respBody string) *Detail {
	t.Helper()
	path := writeTemp(t, t.TempDir(), "x.log", logFile(`{"model":"m","stream":true}`, "text/event-stream", respBody, ""))
	return mustDetail(t, path)
}

func TestAssembleClaude(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10,"output_tokens":1}}}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me think"}}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello "}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world"}}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`, ``,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`, ``,
		`event: message_stop`, `data: {"type":"message_stop"}`, ``,
	}, "\n")
	d := assembledFor(t, body)
	a := d.Response.Assembled
	if a == nil || a.Text != "Hello world" || a.Reasoning != "let me think" {
		t.Fatalf("assembled = %+v", a)
	}
	if len(a.ToolCalls) != 1 || a.ToolCalls[0] != (ToolCall{ID: "toolu_1", Name: "get_weather", Arguments: `{"city":"Paris"}`}) {
		t.Fatalf("tool calls = %+v", a.ToolCalls)
	}
	if len(d.Response.SSEEvents) != 11 || d.Response.SSEEvents[0].Event != "message_start" {
		t.Fatalf("events = %d", len(d.Response.SSEEvents))
	}
	path := writeTemp(t, t.TempDir(), "y.log", logFile(`{"model":"claude-x","stream":true}`, "text/event-stream", body, ""))
	m, err := readMeta(path)
	if err != nil || i64(m.tokens.in) != 10 || i64(m.tokens.out) != 42 || m.requestedModel != "claude-x" || !m.stream {
		t.Fatalf("claude meta = %+v err=%v", m, err)
	}
}

func TestAssembleGemini(t *testing.T) {
	body := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"pondering","thought":true}]}}]}`, ``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hi "}]}}]}`, ``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"there"},{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":2}}`, ``,
	}, "\n")
	a := assembledFor(t, body).Response.Assembled
	if a == nil || a.Text != "Hi there" || a.Reasoning != "pondering" || len(a.ToolCalls) != 1 || a.ToolCalls[0].Name != "lookup" || a.ToolCalls[0].Arguments != `{"q":"x"}` {
		t.Fatalf("assembled = %+v", a)
	}
}

func TestAssembleResponses(t *testing.T) {
	body := strings.Join([]string{
		`event: response.created`, `data: {"type":"response.created","response":{"id":"resp_1"}}`, ``,
		`event: response.reasoning_summary_text.delta`, `data: {"type":"response.reasoning_summary_text.delta","delta":"hmm"}`, ``,
		`event: response.output_text.delta`, `data: {"type":"response.output_text.delta","delta":"Hello"}`, ``,
		`event: response.output_text.delta`, `data: {"type":"response.output_text.delta","delta":"!"}`, ``,
		`event: response.output_item.added`, `data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"run","arguments":""}}`, ``,
		`event: response.function_call_arguments.delta`, `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"a\":"}`, ``,
		`event: response.function_call_arguments.delta`, `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"1}"}`, ``,
		`event: response.completed`, `data: {"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":8,"output_tokens_details":{"reasoning_tokens":5},"input_tokens_details":{"cached_tokens":4}}}}`, ``,
	}, "\n")
	a := assembledFor(t, body).Response.Assembled
	if a == nil || a.Text != "Hello!" || a.Reasoning != "hmm" || len(a.ToolCalls) != 1 || a.ToolCalls[0] != (ToolCall{ID: "call_1", Name: "run", Arguments: `{"a":1}`}) {
		t.Fatalf("assembled = %+v", a)
	}
	path := writeTemp(t, t.TempDir(), "z.log", logFile(`{"model":"gpt-x","stream":true}`, "text/event-stream", body, ""))
	m, _ := readMeta(path)
	if i64(m.tokens.in) != 9 || i64(m.tokens.out) != 8 || i64(m.tokens.reasoning) != 5 || i64(m.tokens.cached) != 4 {
		t.Fatalf("responses tokens = %d %d %d %d", i64(m.tokens.in), i64(m.tokens.out), i64(m.tokens.reasoning), i64(m.tokens.cached))
	}
}

func TestHeaderLikeLinesInResponseBodyAreBody(t *testing.T) {
	body := "line one\n\n=== API REQUEST 1 ===\n\n=== RESPONSE ===\nStatus: 500"
	path := writeTemp(t, t.TempDir(), "w.log", logFile(`{"model":"m"}`, "text/plain", body, ""))
	d := mustDetail(t, path)
	if d.Response.Body != body || d.Response.Status != 200 || len(d.Attempts) != 0 {
		t.Fatalf("response = %+v attempts=%d", d.Response, len(d.Attempts))
	}
}

func TestBodyTruncation(t *testing.T) {
	big := `{"model":"m","messages":"` + strings.Repeat("a", BodyLimit+10) + `"}`
	path := writeTemp(t, t.TempDir(), "big.log", logFile(big, "application/json", `{"ok":true}`, ""))
	d := mustDetail(t, path)
	if !d.Client.BodyTruncated || len(d.Client.Body) != BodyLimit || d.Response.BodyTruncated {
		t.Fatalf("truncated=%v len=%d", d.Client.BodyTruncated, len(d.Client.Body))
	}
	m, _ := readMeta(path)
	if m.requestedModel != "m" {
		t.Fatalf("model from big body = %q", m.requestedModel)
	}
}

func TestInvalidFileIsSkipped(t *testing.T) {
	path := writeTemp(t, t.TempDir(), "main.log", "[2026-11-16] info started\n")
	m, err := readMeta(path)
	if err != nil || m.valid {
		t.Fatalf("app log parsed as request log: %+v %v", m, err)
	}
}
