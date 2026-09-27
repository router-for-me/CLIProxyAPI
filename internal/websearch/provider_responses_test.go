package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const responsesSearchFixture = `{
	"id":"resp_1","object":"response","status":"completed","model":"test",
	"output":[
		{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"golang generics"}},
		{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[
			{"type":"output_text","text":"Generics landed in Go 1.18.","annotations":[
				{"type":"url_citation","url":"https://go.dev/doc/tutorial/generics","title":"Generics tutorial"},
				{"type":"url_citation","url":"https://go.dev/ref/spec","title":"Spec"}
			]}
		]}
	],
	"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}
}`

func TestXAISearchParsesResponsesOutput(t *testing.T) {
	cfg := Config{XAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/responses") {
			t.Fatalf("path = %s", req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer k" {
			t.Fatalf("auth = %q", got)
		}
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "model").String(); got != DefaultXAISearchModel {
			t.Fatalf("model = %q", got)
		}
		if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "low" {
			t.Fatalf("reasoning effort = %q", got)
		}
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "xai"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "Generics landed in Go 1.18." {
		t.Fatalf("answer = %q", response.Answer)
	}
	if len(response.Sources) != 2 || response.Sources[0].URL != "https://go.dev/doc/tutorial/generics" {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if len(response.SearchQueries) != 1 || response.SearchQueries[0] != "golang generics" {
		t.Fatalf("queries = %v", response.SearchQueries)
	}
}

func TestCodexSearchSendsDomainFilters(t *testing.T) {
	cfg := Config{CodexAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "tools.0.search_context_size").String(); got != "high" {
			t.Fatalf("context size missing: %s", body)
		}
		if got := gjson.GetBytes(body, "tools.0.filters.allowed_domains.0").String(); got != "go.dev" {
			t.Fatalf("allowed domains missing: %s", body)
		}
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go site:go.dev", Provider: "codex"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

func TestXAISearchMapsExcludedDomains(t *testing.T) {
	cfg := Config{XAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		// The xAI Responses tool nests domain filters under `filters`;
		// a top-level field is silently ignored by the backend.
		if got := gjson.GetBytes(body, "tools.0.filters.excluded_domains.0").String(); got != "spam.example" {
			t.Fatalf("excluded domains missing: %s", body)
		}
		if gjson.GetBytes(body, "tools.0.excluded_domains").Exists() {
			t.Fatalf("filters must not be written at the tool top level: %s", body)
		}
		if gjson.GetBytes(body, "tools.0.filters.allowed_domains").Exists() {
			t.Fatalf("allow and block must be exclusive: %s", body)
		}
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go -site:spam.example", Provider: "xai"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

func TestResponsesSearchFallsBackToAnswerLinks(t *testing.T) {
	body := []byte(`{"output":[{"type":"message","content":[
		{"type":"output_text","text":"See [docs](https://go.dev/doc) and https://example.com/more."}]
	}]}`)
	response := parseResponsesSearch(body)
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if response.Sources[0].URL != "https://go.dev/doc" || response.Sources[0].Title != "docs" {
		t.Fatalf("markdown link = %+v", response.Sources[0])
	}
	if response.Sources[1].URL != "https://example.com/more" {
		t.Fatalf("bare URL = %+v", response.Sources[1])
	}
}

func TestResponsesSearchAuthFailure(t *testing.T) {
	cfg := Config{CodexAPIKey: "bad"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(401, `{"error":"bad key"}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "codex"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "Codex authorization failed") {
		t.Fatalf("error = %v", errExecute)
	}
}

func TestLLMProvidersNeedKeys(t *testing.T) {
	cfg := Config{}.WithDefaults()
	if lookupProvider("xai").Available(cfg, false) {
		t.Fatal("xai available without key")
	}
	if lookupProvider("codex").Available(cfg, true) {
		t.Fatal("codex available without key")
	}
	if !lookupProvider("duckduckgo").Available(cfg, false) {
		t.Fatal("duckduckgo should always be available")
	}
}

func TestChainPrefersLLMProvidersWhenKeyed(t *testing.T) {
	cfg := Config{XAIAPIKey: "k", BraveAPIKey: "k"}.WithDefaults()
	var order []string
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		order = append(order, req.URL.Host)
		return jsonResponse(200, responsesSearchFixture), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Provider != ProviderXAI {
		t.Fatalf("provider = %q, want xai first", response.Provider)
	}
	if len(order) != 1 || !strings.Contains(order[0], "x.ai") {
		t.Fatalf("request order = %v", order)
	}
}
