package websearch

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Brave strips date-bound tokens from the query and carries them in the
// native absolute freshness range; markup decorations are disabled so
// titles do not reach the model wrapped in tags.
func TestBraveCanonicalizesQueryAndFreshness(t *testing.T) {
	cfg := Config{BraveAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		if got := query.Get("q"); got != "release notes site:go.dev" {
			t.Fatalf("q = %q, want date tokens stripped and site: kept", got)
		}
		if got := query.Get("freshness"); got != "2024-01-01to2025-01-01" {
			t.Fatalf("freshness = %q, want the absolute range", got)
		}
		if got := query.Get("text_decorations"); got != "false" {
			t.Fatalf("text_decorations = %q", got)
		}
		// Brave reports the request id in a response header; the JSON body
		// has no `request_id` field, so one must come from the header.
		resp := jsonResponse(200, `{"web":{"results":[
			{"title":"Go","url":"https://go.dev","description":"docs","extra_snippets":["more","even more"]},
			{"title":"Bad","url":"javascript:alert(1)"}]},
			"request_id":"ignored-body-value"}`)
		resp.Header.Set("X-Request-Id", "req-1")
		return resp, nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{
		Query: "release notes after:2024-01-01 before:2025-01-01 site:go.dev", Provider: "brave",
	})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 {
		t.Fatalf("sources = %+v, want the non-http URL rejected", response.Sources)
	}
	// The paid-for extra_snippets must not be discarded.
	if !strings.Contains(response.Sources[0].Snippet, "even more") {
		t.Fatalf("snippet = %q, want extra snippets merged", response.Sources[0].Snippet)
	}
	if response.RequestID != "req-1" {
		t.Fatalf("requestID = %q, want the X-Request-Id header value", response.RequestID)
	}
}

func TestBraveRejectsOverlongQuery(t *testing.T) {
	cfg := Config{BraveAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"web":{"results":[]}}`), nil
	}})
	long := strings.Repeat("a", 501)
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: long, Provider: "brave"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "cannot exceed") {
		t.Fatalf("error = %v, want a length rejection before the request", errExecute)
	}
}

// Tavily keeps topic at the default general index and uses time_range as a
// temporal filter, then retries without any time filter when a time-scoped
// query comes back empty.
func TestTavilyRetriesWithoutTimeFilter(t *testing.T) {
	calls := 0
	cfg := Config{TavilyAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		calls++
		body := readBody(t, req)
		if calls == 1 {
			if got := gjson.GetBytes(body, "time_range").String(); got != "week" {
				t.Fatalf("time_range = %q", got)
			}
			return jsonResponse(200, `{"results":[]}`), nil
		}
		if gjson.GetBytes(body, "time_range").Exists() {
			t.Fatalf("retry must drop the time filter: %s", body)
		}
		return jsonResponse(200, `{"answer":"a","results":[{"title":"T","url":"https://t.example"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyWeek, Provider: "tavily"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want one unfiltered retry", calls)
	}
	if response.Answer != "a" || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
}

// Site directives map onto Tavily's domain fields rather than the query.
func TestTavilyMapsDomainsAndDates(t *testing.T) {
	cfg := Config{TavilyAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer k" {
			t.Fatalf("auth = %q", got)
		}
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "include_domains.0").String(); got != "go.dev" {
			t.Fatalf("include_domains = %s", gjson.GetBytes(body, "include_domains").Raw)
		}
		if got := gjson.GetBytes(body, "start_date").String(); got != "2024-01-01" {
			t.Fatalf("start_date = %q", got)
		}
		if got := gjson.GetBytes(body, "messages").Exists(); got {
			t.Fatalf("body must not carry messages: %s", body)
		}
		if strings.Contains(gjson.GetBytes(body, "query").String(), "site:") {
			t.Fatalf("site: must not also appear in the query: %s", body)
		}
		// An answer means the time filter produced results, so no retry.
		return jsonResponse(200, `{"answer":"found","results":[{"title":"T","url":"https://t.example"}]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{
		Query: "go site:go.dev after:2024-01-01", Recency: RecencyWeek, Provider: "tavily",
	}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}
