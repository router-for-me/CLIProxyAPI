package websearch

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpenRouterGroundingRequestAndCitations(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/chat/completions" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer k" {
			t.Fatalf("auth = %q", got)
		}
		body := readBody(t, req)
		// The web plugin carries its own result cap, defaulting to 5
		// rather than the generic source limit.
		if got := gjson.GetBytes(body, "plugins.0.id").String(); got != "web" {
			t.Fatalf("plugin = %q", got)
		}
		if got := gjson.GetBytes(body, "plugins.0.max_results").Int(); got != 5 {
			t.Fatalf("max_results = %d, want the plugin default", got)
		}
		if got := gjson.GetBytes(body, "messages.0.content").String(); got != "go" {
			t.Fatalf("query = %q", got)
		}
		return jsonResponse(200, `{
			"id":"gen-1","model":"openai/gpt-5-mini",
			"choices":[{"message":{
				"content":"Generics shipped in Go 1.18.",
				"annotations":[
					{"type":"url_citation","url_citation":{"url":"https://go.dev/generics","title":"Generics","content":"go1.18"}},
					{"type":"url_citation","url_citation":{"url":"https://go.dev/generics","title":"dupe"}},
					{"type":"url_citation","url_citation":{"url":"https://go.dev/blog","title":"Blog"}}
				]}}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "openrouter"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "Generics shipped in Go 1.18." {
		t.Fatalf("answer = %q", response.Answer)
	}
	// Each citation yields a source AND a citation, deduped by URL.
	if len(response.Sources) != 2 || len(response.Citations) != 2 {
		t.Fatalf("sources = %d, citations = %d", len(response.Sources), len(response.Citations))
	}
	if response.Sources[0].Snippet != "go1.18" || response.Citations[0].CitedText != "go1.18" {
		t.Fatalf("citation content = %+v / %+v", response.Sources[0], response.Citations[0])
	}
	if response.Model != "openai/gpt-5-mini" || response.RequestID != "gen-1" {
		t.Fatalf("model = %q requestID = %q", response.Model, response.RequestID)
	}
	if response.Usage.InputTokens != 11 || response.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

// OMP rejects only when the backend returns neither an answer nor
// citations; a cited answer is accepted even with no sources.
func TestOpenRouterRejectsEmptyResult(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"id":"gen-2","choices":[{"message":{"content":"","annotations":[]}}]}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "openrouter"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "no answer or citations") {
		t.Fatalf("error = %v, want the empty-result rejection", errExecute)
	}
}

// An answer with no citations is still returned: OpenRouter decides what
// counts as grounded, and over-rejecting would drop valid answers.
func TestOpenRouterAcceptsUncitedAnswer(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"id":"gen-2","choices":[{"message":{"content":"I think 1.18","annotations":[]}}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "openrouter"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "I think 1.18" {
		t.Fatalf("answer = %q", response.Answer)
	}
}

func TestOpenRouterRejectsInvalidShape(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"error":{"message":"upstream exploded"}}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "openrouter"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "invalid response") {
		t.Fatalf("error = %v", errExecute)
	}
}

func TestOpenRouterAcceptsContentPartArray(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"id":"gen-3","choices":[{"message":{
			"content":[{"text":"part one "},{"text":"  "},{"text":"part two"}],
			"annotations":[{"url_citation":{"url":"https://x.example"}}]}}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "openrouter"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "part one\npart two" {
		t.Fatalf("answer = %q, want non-blank parts joined", response.Answer)
	}
}

func TestOpenRouterNeedsKey(t *testing.T) {
	cfg := Config{}.WithDefaults()
	if lookupProvider(ProviderOpenRouter).Available(cfg, true) {
		t.Fatal("openrouter must not be available without a key")
	}
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	if got := (Config{}).OpenRouterKey(); got != "env-key" {
		t.Fatalf("key = %q", got)
	}
}
