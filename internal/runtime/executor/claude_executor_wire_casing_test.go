package executor

import (
	"bufio"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// claudeCode2_1_220WireHeaderOrder is the header name sequence captured from a
// real Claude Code 2.1.220 OAuth POST /v1/messages over HTTP/1.1, minus the four
// names the Node HTTP layer appends after the sorted block (Connection, Host,
// Accept-Encoding, Content-Length) and minus User-Agent. Go hardcodes Host,
// User-Agent and Content-Length ahead of the sorted block, so those four
// positions cannot be matched without replacing the request serialiser; the real
// client carries User-Agent inside the sorted block at index 3.
var claudeCode2_1_220WireHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	"x-client-request-id",
}

func newClaudeWireProbeRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	auth := &cliproxyauth.Auth{ID: "wire", Metadata: map[string]any{"access_token": "sk-ant-oat01-wire"}}
	req := httptest.NewRequest(http.MethodPost, rawURL, strings.NewReader("{}"))
	req.Header = http.Header{}
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	if err := applyClaudeHeaders(req, auth, "sk-ant-oat01-wire", false, nil, body, nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders: %v", err)
	}
	// Mirror the production sequence: the casing pass runs at the send boundary,
	// not inside applyClaudeHeaders, so Header.Get keeps working everywhere else.
	applyClaudeWireHeaderCasing(req)
	return req
}

// The casing pass must stay at the send boundary. Running it inside
// applyClaudeHeaders would make these headers invisible to Header.Get for the
// rest of the pipeline, which is how the first attempt broke ten other tests.
func TestApplyClaudeHeaders_LeavesHeadersCanonicalForThePipeline(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "wire", Metadata: map[string]any{"access_token": "sk-ant-oat01-wire"}}
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader("{}"))
	req.Header = http.Header{}
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	if err := applyClaudeHeaders(req, auth, "sk-ant-oat01-wire", false, nil, body, nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders: %v", err)
	}
	for canonical := range claudeWireHeaderCasing {
		if req.Header.Get(canonical) == "" {
			t.Fatalf("%s is unreadable through Header.Get right after applyClaudeHeaders", canonical)
		}
	}
}

// serializedHeaderNames reads the names off the actual serialized request, which
// is the only representation the server ever sees.
func serializedHeaderNames(t *testing.T, req *http.Request) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := req.Write(&buf); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var names []string
	scanner := bufio.NewScanner(&buf)
	scanner.Scan() // request line
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		name, _, found := strings.Cut(line, ":")
		if !found {
			t.Fatalf("malformed header line %q", line)
		}
		names = append(names, name)
	}
	return names
}

// The wire casing is a fingerprint in its own right: CPA negotiates ALPN
// http/1.1, so names are not lowercased by HPACK and reach Anthropic verbatim.
func TestApplyClaudeHeaders_WireCasingMatchesRealClient(t *testing.T) {
	req := newClaudeWireProbeRequest(t, "https://api.anthropic.com/v1/messages?beta=true")
	got := serializedHeaderNames(t, req)

	transportOwned := map[string]bool{
		"Host": true, "Content-Length": true, "Connection": true, "Accept-Encoding": true,
		// Go writes User-Agent before the sorted block; the real client keeps it
		// inside it. Tracked separately below.
		"User-Agent": true,
	}
	var sdkNames []string
	for _, name := range got {
		if !transportOwned[name] {
			sdkNames = append(sdkNames, name)
		}
	}

	want := claudeCode2_1_220WireHeaderOrder
	if len(sdkNames) != len(want) {
		t.Fatalf("header count = %d, want %d\n got %v", len(sdkNames), len(want), sdkNames)
	}
	for i := range want {
		if sdkNames[i] != want[i] {
			t.Fatalf("wire header %d = %q, want %q\n got  %v\n want %v", i, sdkNames[i], want[i], sdkNames, want)
		}
	}
}

// Documents the one ordering gap the casing fix cannot close. If Go ever stops
// hoisting User-Agent, or the serialiser is replaced, this test fails and the
// name can move back into claudeCode2_1_220WireHeaderOrder.
func TestApplyClaudeHeaders_UserAgentStillHoistedByGo(t *testing.T) {
	req := newClaudeWireProbeRequest(t, "https://api.anthropic.com/v1/messages?beta=true")
	names := serializedHeaderNames(t, req)
	uaIndex, acceptIndex := -1, -1
	for i, name := range names {
		switch name {
		case "User-Agent":
			uaIndex = i
		case "Accept":
			acceptIndex = i
		}
	}
	if uaIndex == -1 || acceptIndex == -1 {
		t.Fatalf("missing User-Agent or Accept: %v", names)
	}
	if uaIndex > acceptIndex {
		t.Fatal("User-Agent now sorts with the block: fold it back into the expected wire order")
	}
	if got := req.Header.Get("User-Agent"); !strings.HasPrefix(got, "claude-cli/") {
		t.Fatalf("User-Agent = %q, want the Claude Code identity", got)
	}
}

// Guards the property that makes the casing fix sufficient: the real client's
// order is a plain bytewise sort, which is also what Go emits.
func TestClaudeWireHeaderOrderIsBytewiseSorted(t *testing.T) {
	sorted := append([]string(nil), claudeCode2_1_220WireHeaderOrder...)
	sort.Strings(sorted)
	for i := range sorted {
		if sorted[i] != claudeCode2_1_220WireHeaderOrder[i] {
			t.Fatalf("captured order is not a bytewise sort at %d: %q vs %q", i, claudeCode2_1_220WireHeaderOrder[i], sorted[i])
		}
	}
}

// Every fingerprint rule is keyed on the upstream host, never on the caller.
func TestApplyClaudeHeaders_WireCasingIsAnthropicOnly(t *testing.T) {
	req := newClaudeWireProbeRequest(t, "https://api.moonshot.cn/v1/messages")
	for _, name := range serializedHeaderNames(t, req) {
		if name == "anthropic-beta" || name == "x-app" || name == "X-Stainless-OS" {
			t.Fatalf("Anthropic wire casing leaked to a third-party gateway: %q", name)
		}
	}
	if req.Header.Get("Anthropic-Version") == "" {
		t.Fatal("third-party gateway lost its canonical headers")
	}
}

// The rewritten keys are unreachable through Header.Get, so the pass has to run
// after every other mutation. This pins that the values survived the rewrite.
func TestApplyClaudeHeaders_WireCasingPreservesValues(t *testing.T) {
	req := newClaudeWireProbeRequest(t, "https://api.anthropic.com/v1/messages?beta=true")
	for canonical, wire := range claudeWireHeaderCasing {
		if _, stillCanonical := req.Header[canonical]; stillCanonical {
			t.Fatalf("%s was not rewritten to %s", canonical, wire)
		}
		if len(req.Header[wire]) == 0 || req.Header[wire][0] == "" {
			t.Fatalf("%s lost its value during the rewrite", wire)
		}
	}
}

// The three Claude request paths must all leave through doClaudeUpstreamRequest.
// A direct client.Do would skip the wire-casing pass silently, and no behavioural
// test can catch that for a path it does not exercise, so the invariant is
// checked structurally.
func TestClaudeExecutorHasSingleUpstreamSendBoundary(t *testing.T) {
	paths := []string{
		"claude_executor_execute.go",
		"claude_executor_stream.go",
		"claude_executor_tokens.go",
	}
	for _, name := range paths {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(src)
		if strings.Contains(text, "httpClient.Do(") {
			t.Errorf("%s bypasses the send boundary with a direct httpClient.Do", name)
		}
		if !strings.Contains(text, "doClaudeUpstreamRequest(") {
			t.Errorf("%s does not route through doClaudeUpstreamRequest", name)
		}
	}
}
