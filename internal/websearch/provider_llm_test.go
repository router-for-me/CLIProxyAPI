package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// sseBody joins events with the blank-line separator the SSE spec requires
// for event dispatch.
func sseBody(events ...string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(events, "\n\n") + "\n\n")),
	}
}

func TestPerplexityAPIKeyModeDefaults(t *testing.T) {
	cfg := Config{PerplexityAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "model").String(); got != "sonar-pro" {
			t.Fatalf("model = %q", got)
		}
		if got := gjson.GetBytes(body, "search_mode").String(); got != "web" {
			t.Fatalf("search_mode = %q", got)
		}
		if got := gjson.GetBytes(body, "max_tokens").Int(); got != PerplexityDefaultMaxTokens {
			t.Fatalf("max_tokens = %d", got)
		}
		if got := gjson.GetBytes(body, "temperature").Float(); got != PerplexityDefaultTemperature {
			t.Fatalf("temperature = %v", got)
		}
		if got := gjson.GetBytes(body, "num_search_results").Int(); got != PerplexityDefaultNumSearchResults {
			t.Fatalf("num_search_results = %d", got)
		}
		return jsonResponse(200, `{"id":"r1","choices":[{"message":{"content":"answer"}}],
			"citations":["https://a.example","https://b.example","https://a.example"]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity", Limit: 2})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "api_key" || response.Answer != "answer" {
		t.Fatalf("response = %+v", response)
	}
	// Citations are deduped, then limit slices locally.
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
}

func TestPerplexityAPIKeyRecency(t *testing.T) {
	cfg := Config{PerplexityAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "search_recency_filter").String(); got != "week" {
			t.Fatalf("recency filter = %q", got)
		}
		return jsonResponse(200, `{"choices":[{"message":{"content":"a"}}],"citations":["https://a.example"]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyWeek, Provider: "perplexity"}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

func TestPerplexityOAuthMergesSSE(t *testing.T) {
	cfg := Config{PerplexityOAuth: "tok"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.Path, "perplexity_ask") {
			t.Fatalf("path = %s", req.URL.Path)
		}
		// The ask endpoint authenticates via a session cookie; a bearer is
		// silently ignored and the request falls back to the free model.
		if got := req.Header.Get("Cookie"); got != "__Secure-next-auth.session-token=tok" {
			t.Fatalf("cookie = %q, want the session token", got)
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatalf("ask path must not send a bearer: %q", req.Header.Get("Authorization"))
		}
		if got := req.Header.Get("User-Agent"); !strings.HasPrefix(got, "Perplexity/") {
			t.Fatalf("user agent = %q, want the native app UA for an OAuth session", got)
		}
		return sseBody(
			// Chunks stream in order; each event carries the fragment and
			// the offset it belongs at, and the merger splices by offset.
			`data: {"blocks":[{"intended_usage":"markdown","markdown_block":{"chunk_starting_offset":0,"chunks":["Hello "]}}]}`,
			`data: {"blocks":[{"intended_usage":"markdown","markdown_block":{"chunk_starting_offset":6,"chunks":["world"]}}]}`,
			`data: {"blocks":[{"intended_usage":"web_results","web_result_block":{"web_results":[{"url":"https://a.example","name":"A","snippet":"first","timestamp":"2026-01-02"},{"url":"https://b.example","name":"B"}]}}],"status":"COMPLETED"}`,
		), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "Hello world" {
		t.Fatalf("answer = %q, want chunks reassembled by offset", response.Answer)
	}
	if response.AuthMode != "oauth" {
		t.Fatalf("authMode = %q", response.AuthMode)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if response.Sources[0].Title != "A" || response.Sources[0].Snippet != "first" {
		t.Fatalf("source 0 = %+v, want title and snippet from the web_result block", response.Sources[0])
	}
}

// The backend labels the answer block inconsistently across stream
// revisions, so a label that merely contains "markdown" must still win.
func TestPerplexityAnswerBlockMatchedBySubstring(t *testing.T) {
	merged := newPerplexityEvent()
	mergePerplexityEvent(&merged, gjson.Parse(`{"blocks":[
		{"intended_usage":"ask_text","markdown_block":{"answer":"ask prose"}},
		{"intended_usage":"markdown_answer","markdown_block":{"answer":"grounded answer"}}
	]}`))
	if got := perplexityEventAnswer(merged); got != "grounded answer" {
		t.Fatalf("answer = %q, want the markdown-labelled block", got)
	}
}

// The same page reached with and without a trailing slash is one source,
// while distinct paths on one host stay distinct.
func TestPerplexitySourceKeyNormalizesTrailingSlash(t *testing.T) {
	if perplexitySourceKey("https://a.example/x/") != perplexitySourceKey("https://a.example/x") {
		t.Fatal("trailing slash should not fork a source")
	}
	if perplexitySourceKey("https://a.example/x") == perplexitySourceKey("https://a.example/y") {
		t.Fatal("distinct paths must stay distinct")
	}
	if perplexitySourceKey("not a url") == "" {
		t.Fatal("an unparseable key must still be usable")
	}
}

// The ask request body is a research-assistant envelope, not an OpenAI
// chat body; always-search flags keep the model from answering ungrounded.
func TestPerplexityAskRequestEnvelope(t *testing.T) {
	cfg := Config{PerplexityOAuth: "tok"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if gjson.GetBytes(body, "params.query_str").String() != "go" {
			t.Fatalf("query_str missing: %s", body)
		}
		if gjson.GetBytes(body, "query_str").Exists() != true {
			t.Fatalf("envelope must carry query_str: %s", body)
		}
		if gjson.GetBytes(body, "params.skip_search_enabled").Bool() {
			t.Fatalf("skip_search_enabled must be false so retrieval always runs: %s", body)
		}
		if !gjson.GetBytes(body, "params.always_search_override").Bool() {
			t.Fatalf("always_search_override must be true: %s", body)
		}
		return sseBody(`data: {"blocks":[{"intended_usage":"markdown","markdown_block":{"answer":"done"}}],"status":"COMPLETED"}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// Recency cannot combine with absolute date bounds; explicit bounds win.
func TestPerplexityRecencySuppressedByDateBounds(t *testing.T) {
	cfg := Config{PerplexityOAuth: "tok"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "params.search_after_date_filter").String(); got != "1/1/2024" {
			t.Fatalf("after date filter = %q, want Perplexity's M/D/YYYY form", got)
		}
		if gjson.GetBytes(body, "params.search_recency_filter").Type == gjson.String {
			t.Fatalf("recency must be null when date bounds are present: %s", body)
		}
		return sseBody(`data: {"blocks":[{"intended_usage":"markdown","markdown_block":{"answer":"ok"}}],"status":"COMPLETED"}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{
		Query: "go after:2024-01-01", Recency: RecencyWeek, Provider: "perplexity",
	}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

func TestPerplexityAnonymousExplicitOnly(t *testing.T) {
	cfg := Config{}.WithDefaults()
	provider := lookupProvider(ProviderPerplexity)
	if provider.Available(cfg, false) {
		t.Fatal("anonymous perplexity must not enter the automatic chain")
	}
	if !provider.Available(cfg, true) {
		t.Fatal("explicit selection should allow anonymous perplexity")
	}
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return sseBody(`data: {"text":"anon answer"}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "anonymous" || response.Answer != "anon answer" {
		t.Fatalf("response = %+v", response)
	}
}

func TestPerplexityStreamErrorAdvancesChain(t *testing.T) {
	cfg := Config{PerplexityOAuth: "tok"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return sseBody(`data: {"error_code":"RATE_LIMIT","error":"too many requests"}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "RATE_LIMIT") {
		t.Fatalf("error = %v", errExecute)
	}
}

// Grounding metadata is read per stream chunk: the chunk indices in a
// chunk's supports are relative to that chunk's own chunk list. Merging
// chunk lists across the stream attributes citations to the wrong pages.
func TestGeminiGroundingPerChunk(t *testing.T) {
	cfg := Config{GeminiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("x-goog-api-key"); got != "k" {
			t.Fatalf("api key header = %q", got)
		}
		return sseBody(
			// Chunk 1 cites its own chunk 0.
			`data: {"candidates":[{"content":{"parts":[{"text":"Go 1.24 "}]},"groundingMetadata":{"webSearchQueries":["go"],"groundingChunks":[{"web":{"uri":"https://a.example","title":"A"}}],"groundingSupports":[{"segment":{"text":"Go 1.24"},"groundingChunkIndices":[0]}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4}}`,
			// Chunk 2 restates the same source and adds its own; the
			// duplicate must not become a second source.
			`data: {"candidates":[{"content":{"parts":[{"text":"is out."}]},"groundingMetadata":{"webSearchQueries":["go","go 1.24"],"groundingChunks":[{"web":{"uri":"https://a.example","title":"A"}},{"web":{"uri":"https://b.example","title":"B"}}],"groundingSupports":[{"segment":{"text":"is out."},"groundingChunkIndices":[1]}]}}],"modelVersion":"gemini-2.5-flash-001"}`,
		), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "gemini"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "Go 1.24 is out." {
		t.Fatalf("answer = %q", response.Answer)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v, want the duplicate URI collapsed", response.Sources)
	}
	// Each citation must point at the page its own chunk declared.
	if len(response.Citations) != 2 {
		t.Fatalf("citations = %+v", response.Citations)
	}
	if response.Citations[0].URL != "https://a.example" || response.Citations[0].CitedText != "Go 1.24" {
		t.Fatalf("citation 0 = %+v", response.Citations[0])
	}
	if response.Citations[1].URL != "https://b.example" || response.Citations[1].CitedText != "is out." {
		t.Fatalf("citation 1 = %+v", response.Citations[1])
	}
	// Queries are deduped across chunks.
	if len(response.SearchQueries) != 2 {
		t.Fatalf("search queries = %v, want dedupe", response.SearchQueries)
	}
	// The resolved model version is reported, not the requested id.
	if response.Model != "gemini-2.5-flash-001" {
		t.Fatalf("model = %q, want the resolved modelVersion", response.Model)
	}
	if response.Usage.InputTokens != 11 || response.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

// A 5xx is retried with backoff before the provider is abandoned.
func TestGeminiRetriesServerError(t *testing.T) {
	restore := geminiBaseDelay
	geminiBaseDelay = time.Millisecond
	t.Cleanup(func() { geminiBaseDelay = restore })
	calls := 0
	cfg := Config{GeminiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return jsonResponse(503, `{"error":"unavailable"}`), nil
		}
		return sseBody(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "gemini"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "ok" {
		t.Fatalf("answer = %q", response.Answer)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 2 retries before success", calls)
	}
}

// A 4xx is terminal: retrying would burn the chain budget for nothing.
func TestGeminiDoesNotRetryClientError(t *testing.T) {
	calls := 0
	// Use a realistic key so the redaction check is meaningful.
	cfg := Config{GeminiAPIKey: "AIzaSecretValue123"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(400, `{"error":{"message":"bad request: AIzaSecretValue123"}}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "gemini"})
	if errExecute == nil {
		t.Fatal("expected a client-error failure")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want no retry on 4xx", calls)
	}
	// The API key must not be echoed back in the error body.
	if strings.Contains(errExecute.Error(), "AIzaSecretValue123") {
		t.Fatalf("api key leaked into the error: %v", errExecute)
	}
	if !strings.Contains(errExecute.Error(), "[redacted]") {
		t.Fatalf("error should mark the redaction: %v", errExecute)
	}
}

func TestAnthropicServerToolParsing(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("version = %q", got)
		}
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "tools.0.type").String(); got != "web_search_20250305" {
			t.Fatalf("tool type = %q", got)
		}
		if got := gjson.GetBytes(body, "max_tokens").Int(); got != AnthropicDefaultMaxTokens {
			t.Fatalf("max_tokens = %d", got)
		}
		return jsonResponse(200, `{"id":"msg_1","content":[
			{"type":"server_tool_use","id":"srv_1","name":"web_search"},
			{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[
				{"type":"web_search_result","title":"Go","url":"https://go.dev","page_age":"1 day"}]},
			{"type":"text","text":"Go is out."}],
			"usage":{"input_tokens":20,"output_tokens":6,"server_tool_use":{"web_search_requests":1}}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "anthropic"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "Go is out." || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if response.Sources[0].Published != "1 day" {
		t.Fatalf("page age not captured: %+v", response.Sources[0])
	}
	if response.Usage.SearchRequests != 1 || response.Usage.InputTokens != 20 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestAnthropic404Normalization(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(404, `{"error":{"message":"not found"}}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "anthropic"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "404 (model or endpoint not found)") {
		t.Fatalf("error = %v", errExecute)
	}
}

func TestAnthropicOmitsTemperatureForNewModels(t *testing.T) {
	if anthropicModelAcceptsSampling("claude-opus-4-8-20260101") {
		t.Fatal("newest models must not receive temperature")
	}
	if !anthropicModelAcceptsSampling("claude-haiku-4-5-20251001") {
		t.Fatal("older models should receive temperature")
	}
}

// Anthropic returns the executed query on the server_tool_use block and
// citations on the text block; both must be surfaced, and the base64
// encrypted_content must never become a snippet.
func TestAnthropicSurfacesQueriesAndCitations(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		// `limit` is the source cap, not an upstream parameter.
		if got := gjson.GetBytes(body, "messages.0.content").String(); got != "go 1.18 release notes" {
			t.Fatalf("query = %q", got)
		}
		if got := gjson.GetBytes(body, "tools.0.type").String(); got != "web_search_20250305" {
			t.Fatalf("tool type = %q", got)
		}
		return jsonResponse(200, `{"id":"msg_1","model":"claude-haiku-4-5","content":[
			{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"go 1.18 release"}},
			{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[
				{"type":"web_search_result","title":"Go 1.18","url":"https://go.dev/1.18","page_age":"3 days","encrypted_content":"QkFTRTY0IGVuY3J5cHRlZCBib2R5"}]},
			{"type":"text","text":"Go 1.18 added generics.","citations":[
				{"url":"https://go.dev/1.18","title":"Go 1.18","cited_text":"added generics"}]}],
			"usage":{"input_tokens":12,"output_tokens":4,"server_tool_use":{"web_search_requests":1}}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go 1.18 release notes", Provider: "anthropic"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.SearchQueries) != 1 || response.SearchQueries[0] != "go 1.18 release" {
		t.Fatalf("search queries = %v, want the executed query", response.SearchQueries)
	}
	if len(response.Citations) != 1 || response.Citations[0].CitedText != "added generics" {
		t.Fatalf("citations = %+v", response.Citations)
	}
	if len(response.Sources) != 1 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if strings.Contains(response.Sources[0].Snippet, "BASE64") {
		t.Fatalf("encrypted_content leaked into the snippet: %+v", response.Sources[0])
	}
	if response.Sources[0].Snippet != "" {
		t.Fatalf("snippet = %q, want empty rather than base64", response.Sources[0].Snippet)
	}
	if response.Usage.SearchRequests != 1 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if response.Model != "claude-haiku-4-5" || response.RequestID != "msg_1" {
		t.Fatalf("model = %q requestID = %q", response.Model, response.RequestID)
	}
}

// A `site:` include maps onto the tool's native allowed_domains with the
// path stripped, and the operator is not also left in the query text.
func TestAnthropicMapsSiteToNativeDomains(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		domains := gjson.GetBytes(body, "tools.0.allowed_domains")
		if got := domains.Array(); len(got) != 1 || got[0].String() != "github.com" {
			t.Fatalf("allowed_domains = %s, want the bare host", domains.Raw)
		}
		if strings.Contains(gjson.GetBytes(body, "messages.0.content").String(), "site:") {
			t.Fatalf("site: must not also appear in the query: %s", body)
		}
		return jsonResponse(200, `{"id":"msg_2","content":[
			{"type":"web_search_tool_result","content":[{"type":"web_search_result","title":"A","url":"https://github.com/a"}]},
			{"type":"text","text":"done"}]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "claude site:github.com/anthropics", Provider: "anthropic"}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// Includes and excludes are mutually exclusive on the API, so an include
// suppresses the exclude list rather than sending both.
func TestAnthropicIncludesSuppressExcludes(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if !gjson.GetBytes(body, "tools.0.allowed_domains").Exists() {
			t.Fatalf("expected allowed_domains: %s", body)
		}
		if gjson.GetBytes(body, "tools.0.blocked_domains").Exists() {
			t.Fatalf("blocked_domains must be omitted when includes exist: %s", body)
		}
		return jsonResponse(200, `{"id":"msg_3","content":[{"type":"text","text":"ok"}]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go site:go.dev -site:spam.example", Provider: "anthropic"}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}
