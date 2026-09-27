package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// A remote MCP host must see the full streamable-HTTP lifecycle in order:
// initialize, the initialized notification, then tools/call — with the
// server-issued session id threaded through every later request.
func TestMCPSessionHandshakeOrderAndSessionID(t *testing.T) {
	var methods []string
	var sessionHeaders []string
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		methods = append(methods, gjson.GetBytes(body, "method").String())
		sessionHeaders = append(sessionHeaders, req.Header.Get("Mcp-Session-Id"))
		resp := jsonResponse(200, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		if len(methods) == 1 {
			resp.Header.Set("Mcp-Session-Id", "sess-42")
		}
		return resp, nil
	}})
	session := newMCPSession(context.Background(), cfg, "Z.AI", "https://mcp.example/mcp", "tool", nil)
	_, errCall := session.callTool(context.Background(), "Z.AI", "tool", [][]byte{[]byte(`{}`)})
	if errCall != nil {
		t.Fatalf("callTool error = %v", errCall)
	}
	want := []string{"initialize", "notifications/initialized", "tools/call"}
	if len(methods) != len(want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
	for index, method := range want {
		if methods[index] != method {
			t.Fatalf("methods = %v, want %v", methods, want)
		}
	}
	// Only initialize may precede the handshake's session id.
	if sessionHeaders[0] != "" {
		t.Fatalf("initialize must not send a session id, got %q", sessionHeaders[0])
	}
	if sessionHeaders[1] != "sess-42" || sessionHeaders[2] != "sess-42" {
		t.Fatalf("session id not threaded: %v", sessionHeaders)
	}
}

// The initialize request must carry the protocol version and clientInfo the
// upstream Z.AI client sends.
func TestMCPInitializeCarriesProtocolAndClientInfo(t *testing.T) {
	var initializeBody []byte
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if gjson.GetBytes(body, "method").String() == "initialize" {
			initializeBody = body
		}
		return jsonResponse(200, `{"jsonrpc":"2.0","id":1,"result":{}}`), nil
	}})
	_ = newMCPSession(context.Background(), cfg, "Z.AI", "https://mcp.example/mcp", "tool", nil)
	if got := gjson.GetBytes(initializeBody, "params.protocolVersion").String(); got != "2025-03-26" {
		t.Fatalf("protocolVersion = %q", got)
	}
	if got := gjson.GetBytes(initializeBody, "params.clientInfo.name").String(); got != mcpClientName {
		t.Fatalf("clientInfo.name = %q, want %q", got, mcpClientName)
	}
	if got := gjson.GetBytes(initializeBody, "params.clientInfo.version").String(); got != mcpClientVersion {
		t.Fatalf("clientInfo.version = %q", got)
	}
}

// A JSON-RPC `error` code is a negative protocol number, not an HTTP
// status. -32602 (invalid params) must read as a 400-class rejection so the
// caller can retry another argument shape.
func TestMCPJSONRPCErrorCodeMapsToHTTPStatus(t *testing.T) {
	_, errTool := mcpToolResult([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown field search_query"}}`), "Z.AI")
	if errTool == nil {
		t.Fatal("expected a provider error")
	}
	providerErr, ok := errTool.(*ProviderError)
	if !ok {
		t.Fatalf("error type = %T", errTool)
	}
	if providerErr.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for JSON-RPC -32602", providerErr.Status)
	}
	if !strings.Contains(providerErr.Message, "unknown field") {
		t.Fatalf("message = %q", providerErr.Message)
	}
}

// The clientInfo name is the one the upstream Z.AI client sends, spelled
// out here so the test fails if the constant is ever changed to something
// the host may not recognize.
func TestMCPClientInfoNameMatchesUpstream(t *testing.T) {
	if mcpClientName != "omp-coding-agent" {
		t.Fatalf("clientInfo.name = %q, want %q", mcpClientName, "omp-coding-agent")
	}
}

// Z.AI answers a non-enveloped failure. Its error text may arrive under
// `msg`, `message`, or `error_message`; whichever it uses must surface as a
// provider error rather than as an empty "no results" outcome. `error_message`
// is the shape the old code dropped, reporting a bogus success.
func TestMCPDirectErrorEnvelopeIsNotAnEmptyResult(t *testing.T) {
	for _, body := range []string{
		`{"code":1002,"msg":"tool call failed","success":false}`,
		`{"code":1002,"message":"tool call failed","success":false}`,
		`{"code":1002,"error_message":"tool call failed","success":false}`,
	} {
		result, errTool := mcpToolResult([]byte(body), "Z.AI")
		if errTool == nil {
			t.Fatalf("body %s: expected a provider error, got result %s", body, result)
		}
		if !strings.Contains(errTool.Error(), "tool call failed") {
			t.Fatalf("body %s: error = %v, want the host's own message", body, errTool)
		}
	}
}

// A tool-level `isError` reply is a failure, and its status comes from the
// embedded "MCP error -NNNN" number's magnitude.
func TestMCPIsErrorResultIsAProviderError(t *testing.T) {
	_, errTool := mcpToolResult([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"MCP error -32602: bad args"}]}}`), "Z.AI")
	if errTool == nil {
		t.Fatal("expected a provider error for isError")
	}
	providerErr, ok := errTool.(*ProviderError)
	if !ok {
		t.Fatalf("error type = %T", errTool)
	}
	if providerErr.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", providerErr.Status)
	}
}

// An SSE-framed reply's last data event is the authoritative JSON-RPC
// response.
func TestMCPSSEBodyTakesLastDataEvent(t *testing.T) {
	body := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"final\"}]}}\n\n")
	result, errTool := mcpToolResult(parseMCPSSEBody(body), "Z.AI")
	if errTool != nil {
		t.Fatalf("mcpToolResult error = %v", errTool)
	}
	if !strings.Contains(string(result), "final") {
		t.Fatalf("result = %s, want the last event", result)
	}
}

// Parallel reports per-hit `excerpts[]`, joined with a blank line, and
// `publish_date` — neither of which the walker's field list knew.
func TestSourcesFromJSONParallelShape(t *testing.T) {
	body := []byte(`{"search_id":"s1","results":[
		{"title":"A","url":"https://a.example","excerpts":["first","second"],"publish_date":"2026-01-02"},
		{"title":"B","url":"https://b.example"}
	]}`)
	sources := sourcesFromJSON(gjson.ParseBytes(body), 10)
	if len(sources) != 2 {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].Snippet != "first\n\nsecond" {
		t.Fatalf("snippet = %q, want excerpts joined with a blank line", sources[0].Snippet)
	}
	if sources[0].Published != "2026-01-02" {
		t.Fatalf("published = %q, want the publish_date field", sources[0].Published)
	}
	if sources[1].URL != "https://b.example" {
		t.Fatalf("sources = %+v", sources)
	}
}

// Exa reports `highlights[]` as a list joined with a single space — not the
// blank line Parallel uses for `excerpts[]`, so one separator for both
// providers would misreport whichever did not match.
func TestSourcesFromJSONExaHighlights(t *testing.T) {
	body := []byte(`{"results":[{"title":"A","url":"https://a.example","publishedDate":"2026-01-02","highlights":["h1","h2"]}]}`)
	sources := sourcesFromJSON(gjson.ParseBytes(body), 10)
	if len(sources) != 1 {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].Snippet != "h1 h2" {
		t.Fatalf("snippet = %q, want highlights joined with a space", sources[0].Snippet)
	}
	if sources[0].Published != "2026-01-02" {
		t.Fatalf("published = %q", sources[0].Published)
	}
}
