package websearch

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// The Gemini grounding tool is declared as camelCase `googleSearch`; the
// snake_case spelling is accepted without error and leaves the request
// ungrounded, so the response carries no groundingMetadata at all.
func TestGeminiSearchDeclaresGoogleSearchTool(t *testing.T) {
	cfg := Config{GeminiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if !gjson.GetBytes(body, "tools.0.googleSearch").Exists() {
			t.Fatalf("googleSearch tool missing: %s", body)
		}
		if gjson.GetBytes(body, "tools.0.google_search").Exists() {
			t.Fatalf("snake_case google_search is ignored by the API: %s", body)
		}
		return sseBody(`data: {"candidates":[{"content":{"parts":[{"text":"grounded"}]},"groundingMetadata":{"webSearchQueries":["go"],"groundingChunks":[{"web":{"uri":"https://go.dev","title":"Go"}}]}}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "gemini"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://go.dev" {
		t.Fatalf("sources = %+v", response.Sources)
	}
}

// Google's Search grounding understands the classic operator set, so
// directives are canonicalized (`domain:` -> `site:`, `since:` -> `after:`)
// rather than passed through as literal search terms.
func TestGeminiSearchCanonicalizesDirectives(t *testing.T) {
	cfg := Config{GeminiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "contents.0.parts.0.text").String(); got != "release notes site:go.dev" {
			t.Fatalf("query = %q, want directives canonicalized", got)
		}
		return sseBody(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"groundingMetadata":{"webSearchQueries":["q"],"groundingChunks":[{"web":{"uri":"https://go.dev"}}]}}]}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "release notes domain:go.dev", Provider: "gemini"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// Claude namespaces MCP-style tools as `mcp__<server>__<tool>`, so the search
// query is identified by the name suffix. Other hosted tools share the
// `server_tool_use` block type and must not be reported as search queries.
func TestAnthropicSearchIdentifiesServerToolByName(t *testing.T) {
	body := []byte(`{"id":"msg_1","model":"claude-haiku-4-5","content":[
		{"type":"server_tool_use","id":"srvtoolu_1","name":"mcp__web-search__web_search","input":{"query":"golang generics"}},
		{"type":"server_tool_use","id":"srvtoolu_2","name":"code_execution","input":{"query":"not a search"}},
		{"type":"text","text":"Generics landed in Go 1.18."}
	],"usage":{"server_tool_use":{"web_search_requests":1}}}`)
	response := parseAnthropicResponse(gjson.ParseBytes(body), "claude-haiku-4-5")
	if len(response.SearchQueries) != 1 || response.SearchQueries[0] != "golang generics" {
		t.Fatalf("queries = %v, want only the namespaced web_search query", response.SearchQueries)
	}
	if response.Usage.SearchRequests != 1 {
		t.Fatalf("search requests = %d", response.Usage.SearchRequests)
	}
}

// The Responses API omits the retrieved pages unless `include` asks for them.
func TestCodexSearchRequestsWebSearchSources(t *testing.T) {
	cfg := Config{CodexAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "include").Array(); len(got) != 1 || got[0].String() != "web_search_call.action.sources" {
			t.Fatalf("include = %s, want web_search_call.action.sources", body)
		}
		return jsonResponse(200, `{"id":"resp_1","output":[
			{"type":"web_search_call","action":{"type":"search","query":"golang generics","sources":[
				{"type":"web_search_result","url":"https://go.dev/doc","title":"Generics"}]}}
		]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "codex"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://go.dev/doc" {
		t.Fatalf("sources = %+v, want the retrieved search-call source", response.Sources)
	}
}

// The Responses API rejects the bare-string `input` form; the query must be a
// typed `input_text` message item.
func TestCodexSearchSendsTypedInputMessage(t *testing.T) {
	cfg := Config{CodexAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if gjson.GetBytes(body, "input").Type == gjson.String {
			t.Fatalf("input must not be a bare string: %s", body)
		}
		if got := gjson.GetBytes(body, "input.0.content.0.text").String(); got != "go" {
			t.Fatalf("input_text = %q", got)
		}
		if got := gjson.GetBytes(body, "input.0.content.0.type").String(); got != "input_text" {
			t.Fatalf("part type = %q", got)
		}
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "codex"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// Without top-level `instructions` the model may answer from parametric
// knowledge and never call the hosted tool.
func TestResponsesSearchSendsInstructions(t *testing.T) {
	for _, provider := range []struct {
		id  string
		cfg Config
	}{
		{"xai", Config{XAIAPIKey: "k"}.WithDefaults()},
		{"codex", Config{CodexAPIKey: "k"}.WithDefaults()},
	} {
		t.Run(provider.id, func(t *testing.T) {
			cfg := provider.cfg
			cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
				body := readBody(t, req)
				if !strings.Contains(gjson.GetBytes(body, "instructions").String(), "web search") {
					t.Fatalf("instructions missing: %s", body)
				}
				return jsonResponse(200, responsesSearchFixture), nil
			}})
			if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: provider.id}); errExecute != nil {
				t.Fatalf("Execute error = %v", errExecute)
			}
		})
	}
}

// xAI takes a role/content pair; the system role carries the instructions.
func TestXAISearchSendsRoleContentInput(t *testing.T) {
	cfg := Config{XAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "input.0.role").String(); got != "system" {
			t.Fatalf("input.0.role = %q", got)
		}
		if got := gjson.GetBytes(body, "input.1.content").String(); got != "go" {
			t.Fatalf("input.1.content = %q", got)
		}
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "xai"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// max_output_tokens is a top-level Responses field, not a tool parameter.
func TestXAISearchSendsTopLevelMaxOutputTokens(t *testing.T) {
	cfg := Config{XAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "max_output_tokens").Int(); got != 700 {
			t.Fatalf("max_output_tokens = %d, want top-level 700", got)
		}
		if gjson.GetBytes(body, "tools.0.max_output_tokens").Exists() {
			t.Fatalf("max_output_tokens must not live on the tool: %s", body)
		}
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "xai", MaxTokens: 700})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// Retrieved search-call sources are reported under `action.sources`, and each
// cited page becomes both a source and a citation.
func TestParseResponsesSearchReadsActionSources(t *testing.T) {
	body := []byte(`{"output":[
		{"type":"web_search_call","action":{"type":"search","query":"go","sources":[
			{"type":"web_search_result","url":"https://go.dev/doc","title":"Docs","caption":"Guide"}
		]}},
		{"type":"message","content":[
			{"type":"output_text","text":"Answer.","annotations":[
				{"type":"url_citation","url":"https://go.dev/ref","title":"Ref","cited_text":"spec text"}
			]}
		]}
	]}`)
	response := parseResponsesSearch(body)
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if response.Sources[0].URL != "https://go.dev/doc" || response.Sources[0].Title != "Docs" {
		t.Fatalf("search-call source = %+v", response.Sources[0])
	}
	if len(response.Citations) != 2 {
		t.Fatalf("citations = %+v", response.Citations)
	}
	if response.Citations[1].CitedText != "spec text" {
		t.Fatalf("annotation cited text = %q", response.Citations[1].CitedText)
	}
}

// Some Responses-compatible backends return a bare `url` per source.
func TestParseResponsesSearchFallsBackToSourceWebsiteURL(t *testing.T) {
	body := []byte(`{"output":[
		{"type":"web_search_call","action":{"query":"go","sources":[
			{"source_website_url":"https://go.dev/alt","caption":"Alt"}
		]}}
	]}`)
	response := parseResponsesSearch(body)
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://go.dev/alt" {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if response.Sources[0].Title != "Alt" {
		t.Fatalf("caption should become the title: %+v", response.Sources[0])
	}
}

// Codex re-emits directives as canonical Google operators rather than relying
// on the web_search domain filter to survive the ChatGPT backend. The `domain:`
// alias is the observable part: it must be rewritten to `site:`.
func TestCodexSearchReEmitsDirectivesInQuery(t *testing.T) {
	cfg := Config{CodexAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "input.0.content.0.text").String(); got != "notes site:go.dev" {
			t.Fatalf("query = %q, want the domain alias rewritten to site:", got)
		}
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "notes domain:go.dev", Provider: "codex"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}
