package helps

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const testOpenRouterBaseURL = "https://openrouter.ai/api/v1"

func openRouterCacheControlTestDoc(payload, baseURL string, claudeSource bool, original string) []byte {
	doc := ojson.NewDoc([]byte(payload))
	ApplyOpenRouterCacheControlDoc(doc, baseURL, claudeSource, []byte(original))
	return doc.Bytes()
}

func TestOpenRouterCacheControlMarksSystemAndLatestTurn(t *testing.T) {
	out := openRouterCacheControlTestDoc(`{"messages":[
		{"role":"system","content":"You are \"helpful\"."},
		{"role":"user","content":"first"},
		{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"file contents"}]}`, testOpenRouterBaseURL, false, "")

	if got := ojson.GetBytes(out, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system not marked: %s", out)
	}
	if got := ojson.GetBytes(out, "messages.0.content.0.text").String(); got != `You are "helpful".` {
		t.Fatalf("system text lost or mangled: %s", out)
	}
	if got := ojson.GetBytes(out, "messages.0.content.0.type").String(); got != "text" || len(ojson.GetBytes(out, "messages.0.content").Array()) != 1 {
		t.Fatalf("string content must become one text part: %s", out)
	}
	if ojson.GetBytes(out, "messages.1.content.0.cache_control").Exists() || ojson.GetBytes(out, "messages.1.content").Type != ojson.String {
		t.Fatalf("earlier user message changed: %s", out)
	}
	if got := ojson.GetBytes(out, "messages.3.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("latest tool message not marked: %s", out)
	}
	if ojson.GetBytes(out, "messages.3.content.0.cache_control.ttl").Exists() {
		t.Fatalf("unexpected ttl without a client request: %s", out)
	}
}

func TestOpenRouterCacheControlMarksLastTextPart(t *testing.T) {
	out := openRouterCacheControlTestDoc(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:x"}},{"type":"text","text":"  "}]}]}`, testOpenRouterBaseURL, false, "")
	if !ojson.GetBytes(out, "messages.0.content.1.cache_control").Exists() {
		t.Fatalf("expected the last non-empty part (image) to be marked: %s", out)
	}
	if ojson.GetBytes(out, "messages.0.content.2.cache_control").Exists() || ojson.GetBytes(out, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("only one part should be marked: %s", out)
	}
}

func TestOpenRouterCacheControlMarksFileParts(t *testing.T) {
	out := openRouterCacheControlTestDoc(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"summarize"},{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,AA=="}}]}]}`, testOpenRouterBaseURL, false, "")
	if got := ojson.GetBytes(out, "messages.0.content.1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("expected the trailing file part to be marked: %s", out)
	}
	if ojson.GetBytes(out, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("only the file part should be marked: %s", out)
	}

	fileOnly := openRouterCacheControlTestDoc(`{"messages":[{"role":"user","content":[
		{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,AA=="}}]}]}`, testOpenRouterBaseURL, false, "")
	if !ojson.GetBytes(fileOnly, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("a file-only turn without a system message needs a breakpoint: %s", fileOnly)
	}
}

func TestOpenRouterCacheControlLeavesRequestsAlone(t *testing.T) {
	plain := `{"messages":[{"role":"user","content":"hi"}]}`
	if out := openRouterCacheControlTestDoc(plain, "https://api.example.com/v1", false, ""); string(out) != plain {
		t.Fatalf("non-OpenRouter provider changed: %s", out)
	}
	if out := openRouterCacheControlTestDoc(plain, "", false, ""); string(out) != plain {
		t.Fatalf("empty base URL changed: %s", out)
	}
	for name, marked := range map[string]string{
		"message part":  `{"messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`,
		"tool":          `{"tools":[{"type":"function","function":{"name":"read"},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`,
		"tool function": `{"tools":[{"type":"function","function":{"name":"read","cache_control":{"type":"ephemeral"}}}],"messages":[{"role":"user","content":"hi"}]}`,
	} {
		if out := openRouterCacheControlTestDoc(marked, testOpenRouterBaseURL, false, ""); string(out) != marked {
			t.Fatalf("request with its own markers on a %s changed: %s", name, out)
		}
	}
	responses := `{"input":[{"role":"user","content":"hi"}]}`
	if out := openRouterCacheControlTestDoc(responses, testOpenRouterBaseURL, false, ""); string(out) != responses {
		t.Fatalf("non chat-completions body changed: %s", out)
	}
}

func TestOpenRouterCacheControlKeepsClaudeOneHourRequest(t *testing.T) {
	claude := `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`
	out := openRouterCacheControlTestDoc(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`, testOpenRouterBaseURL, true, claude)
	for _, i := range []string{"0", "1"} {
		if got := ojson.GetBytes(out, "messages."+i+".content.0.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("message %s: ttl = %q, want 1h: %s", i, got, out)
		}
	}
	// a 1h request in another source format is not inferred
	out = openRouterCacheControlTestDoc(`{"messages":[{"role":"user","content":"hi"}]}`, testOpenRouterBaseURL, false, claude)
	if ojson.GetBytes(out, "messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("ttl applied for a non-Claude source: %s", out)
	}
}

func TestClaudeRequestAsksOneHourCache(t *testing.T) {
	for name, payload := range map[string]string{
		"top level": `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[]}`,
		"tool":      `{"tools":[{"name":"t","cache_control":{"type":"ephemeral","ttl":"1H"}}]}`,
		"system":    `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
		"message":   `{"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
	} {
		if !ClaudeRequestAsksOneHourCache([]byte(payload)) {
			t.Errorf("%s: want true", name)
		}
	}
	for name, payload := range map[string]string{
		"5m":      `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]}`,
		"none":    `{"messages":[{"role":"user","content":"hi"}]}`,
		"invalid": `{"system":`,
	} {
		if ClaudeRequestAsksOneHourCache([]byte(payload)) {
			t.Errorf("%s: want false", name)
		}
	}
	if !ClaudeRequestAsksOneHourCache(nil, []byte(`{"system":[{"cache_control":{"ttl":"1h"}}]}`)) {
		t.Error("a later payload must be checked when the first is empty")
	}
}

func TestApplyOpenRouterSessionHeader(t *testing.T) {
	header := func(baseURL string, clientHeaders http.Header, preset string, metadata ...map[string]any) string {
		r, _ := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", nil)
		if preset != "" {
			r.Header.Set("X-Session-Id", preset)
		}
		ApplyOpenRouterSessionHeader(r, baseURL, "openai-compatibility", clientHeaders, metadata...)
		return r.Header.Get("X-Session-Id")
	}
	execution := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "conversation-1"}
	canonical := map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "pi-session-42"}

	fromExecution := header(testOpenRouterBaseURL, nil, "", execution)
	if fromExecution == "" || fromExecution != header(testOpenRouterBaseURL, nil, "", execution) {
		t.Fatalf("execution session: %q, want a stable id", fromExecution)
	}
	// A client that names its own session has no derived or execution session, only the canonical one.
	fromCanonical := header(testOpenRouterBaseURL, nil, "", nil, canonical)
	if fromCanonical == "" || fromCanonical != header(testOpenRouterBaseURL, nil, "", canonical) || fromCanonical == fromExecution {
		t.Fatalf("canonical session: %q, want a stable id of its own", fromCanonical)
	}
	if other := header(testOpenRouterBaseURL, nil, "", map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "pi-session-43"}); other == fromCanonical {
		t.Fatal("different conversations must get different ids")
	}
	if got := header(testOpenRouterBaseURL, http.Header{"X-Session-Id": []string{"client-chosen"}}, "", execution); got != "client-chosen" {
		t.Fatalf("client header = %q, want it forwarded", got)
	}
	if got := header(testOpenRouterBaseURL, http.Header{"X-Session-Id": []string{"client-chosen"}}, "configured", execution); got != "configured" {
		t.Fatalf("header already on the request = %q, want it kept", got)
	}
	if got := header("https://api.example.com/v1", http.Header{"X-Session-Id": []string{"client-chosen"}}, "", execution); got != "" {
		t.Fatalf("other providers must not get the header, got %q", got)
	}
	if got := header(testOpenRouterBaseURL, nil, ""); got != "" {
		t.Fatalf("no session known: got %q, want none", got)
	}
}

func TestOpenRouterPromptCacheOptionsCarryClientTTL(t *testing.T) {
	// Responses client asking for 12h: the translated chat body has no prompt_cache_options.
	out := openRouterCacheControlTestDoc(`{"model":"openai/gpt-6.1-sol","messages":[{"role":"user","content":"hi"}]}`,
		testOpenRouterBaseURL, false, `{"model":"gpt-6.1-sol","input":"hi","prompt_cache_options":{"ttl":"12h"}}`)
	if got := ojson.GetBytes(out, "prompt_cache_options.ttl").String(); got != "12h" {
		t.Fatalf("ttl = %q, want 12h: %s", got, out)
	}
	if got := ojson.GetBytes(out, "prompt_cache_options.mode").String(); got != "explicit" {
		t.Fatalf("mode = %q, want explicit (OpenRouter ignores the ttl otherwise): %s", got, out)
	}
	if !ojson.GetBytes(out, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("explicit mode needs a breakpoint: %s", out)
	}

	// Chat client sending the ttl itself without a mode, and its own breakpoint.
	out = openRouterCacheControlTestDoc(`{"model":"openai/gpt-6.1-terra","prompt_cache_options":{"ttl":"12h"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`,
		testOpenRouterBaseURL, false, "")
	if got := ojson.GetBytes(out, "prompt_cache_options.mode").String(); got != "explicit" {
		t.Fatalf("mode = %q, want explicit: %s", got, out)
	}
}

func TestOpenRouterPromptCacheOptionsLeftAlone(t *testing.T) {
	cases := map[string]struct{ payload, original string }{
		"no ttl asked":        {`{"model":"openai/gpt-6.1-sol","messages":[{"role":"user","content":"hi"}]}`, `{"input":"hi"}`},
		"not an OpenAI model": {`{"model":"anthropic/claude-opus-5.5","messages":[{"role":"user","content":"hi"}]}`, `{"prompt_cache_options":{"ttl":"12h"}}`},
		"nothing to mark":     {`{"model":"openai/gpt-6.1-sol","messages":[{"role":"user","content":"  "}]}`, `{"prompt_cache_options":{"ttl":"12h"}}`},
	}
	for name, c := range cases {
		out := openRouterCacheControlTestDoc(c.payload, testOpenRouterBaseURL, false, c.original)
		if ojson.GetBytes(out, "prompt_cache_options").Exists() {
			t.Fatalf("%s: unexpected prompt_cache_options: %s", name, out)
		}
	}
	// A mode the client chose is kept.
	out := openRouterCacheControlTestDoc(`{"model":"openai/gpt-6.1-sol","prompt_cache_options":{"mode":"implicit","ttl":"30m"},
		"messages":[{"role":"user","content":"hi"}]}`, testOpenRouterBaseURL, false, "")
	if got := ojson.GetBytes(out, "prompt_cache_options.mode").String(); got != "implicit" {
		t.Fatalf("client mode replaced: %s", out)
	}
	// Other base URLs are never touched.
	out = openRouterCacheControlTestDoc(`{"model":"openai/gpt-6.1-sol","messages":[{"role":"user","content":"hi"}]}`,
		"https://api.openai.com/v1", false, `{"prompt_cache_options":{"ttl":"12h"}}`)
	if ojson.GetBytes(out, "prompt_cache_options").Exists() {
		t.Fatalf("non-OpenRouter request changed: %s", out)
	}
}
