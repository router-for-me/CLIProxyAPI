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

func TestKagiConcatenatesBucketsAndTags(t *testing.T) {
	cfg := Config{KagiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer k" {
			t.Fatalf("auth = %q", got)
		}
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "workflow").String(); got != "search" {
			t.Fatalf("workflow = %q", got)
		}
		if got := gjson.GetBytes(body, "limit").Int(); got != 10 {
			t.Fatalf("limit = %d", got)
		}
		return jsonResponse(200, `{"meta":{"trace":"t1"},
			"data":{"search":[{"title":"S","url":"https://s.example","snippet":"ss"}],
			"video":[{"title":"V","url":"https://v.example"}],
			"news":[{"title":"N","url":"https://n.example"}],
			"infobox":[{"title":"I","url":"https://i.example"}],
			"direct_answer":[{"snippet":"42"}],
			"adjacent_question":[{"props":{"question":"related one"}},{"title":"fallback from title"}],
			"related_search":[{"props":{"question":"related two"}}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "kagi"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "42" {
		t.Fatalf("answer = %q", response.Answer)
	}
	if len(response.Sources) != 4 {
		t.Fatalf("sources = %+v, want all four buckets", response.Sources)
	}
	// Non-web buckets are prefixed, matching upstream's tag order.
	for index, tag := range []string{"[Video]", "[News]", "[Info]"} {
		if !strings.HasPrefix(response.Sources[index+1].Title, tag) {
			t.Fatalf("source %d = %q, want the %s tag", index+1, response.Sources[index+1].Title, tag)
		}
	}
	// Both question shapes resolve: props.question and the title fallback.
	if len(response.Related) != 3 || response.RequestID != "t1" {
		t.Fatalf("related = %v requestID = %q", response.Related, response.RequestID)
	}
}

func TestKagiRecencyBecomesAfterFilter(t *testing.T) {
	cfg := Config{KagiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		after := gjson.GetBytes(body, "filters.after").String()
		if len(after) != 10 || !strings.HasSuffix(after, "-01-01") && len(after) != 10 {
			t.Fatalf("after = %q, want a YYYY-MM-DD date", after)
		}
		return jsonResponse(200, `{"data":{"search":[{"title":"S","url":"https://s.example"}]}}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyMonth, Provider: "kagi"}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

func TestFirecrawlKeylessAndQueryReformatting(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "" {
			t.Fatalf("keyless mode must not send auth, got %q", got)
		}
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "sources.0.type").String(); got != "web" {
			t.Fatalf("sources = %s", body)
		}
		if got := gjson.GetBytes(body, "query").String(); !strings.Contains(got, "site:go.dev") || !strings.Contains(got, "filetype:pdf") {
			t.Fatalf("operators not preserved: %q", got)
		}
		if got := gjson.GetBytes(body, "tbs").String(); got != "qdr:d" {
			t.Fatalf("tbs = %q", got)
		}
		// Firecrawl reports its request id at the top level and its list
		// under data.web, not data.requestId/data.results.
		return jsonResponse(200, `{"id":"r1","data":{"web":[{"title":"F","url":"https://f.example","description":"d"}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go site:go.dev filetype:pdf", Recency: RecencyDay, Provider: "firecrawl"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "keyless" || response.RequestID != "r1" || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
}

func TestFirecrawlUsesKeyWhenPresent(t *testing.T) {
	cfg := Config{FirecrawlAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer k" {
			t.Fatalf("auth = %q", got)
		}
		return jsonResponse(200, `{"id":"r2","data":[{"title":"F","url":"https://f.example"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "firecrawl"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "api_key" {
		t.Fatalf("authMode = %q", response.AuthMode)
	}
	if response.RequestID != "r2" {
		t.Fatalf("requestID = %q, want the top-level id", response.RequestID)
	}
}

func TestKimiRejectsOpenPlatformKey(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "open-platform")
	if got := (Config{}).WithDefaults().KimiKey(); got != "" {
		t.Fatalf("open platform key must not authenticate Kimi search, got %q", got)
	}
	t.Setenv("MOONSHOT_SEARCH_API_KEY", "search-key")
	if got := (Config{}).WithDefaults().KimiKey(); got != "search-key" {
		t.Fatalf("search key = %q", got)
	}
}

func TestKimiRequestShape(t *testing.T) {
	cfg := Config{KimiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "text_query").String(); got != "go" {
			t.Fatalf("text_query = %q", got)
		}
		// Page crawling is opt-in; enabling it by default makes every
		// search fetch full page bodies.
		if gjson.GetBytes(body, "enable_page_crawling").Bool() {
			t.Fatalf("page crawling must default to off: %s", body)
		}
		if got := gjson.GetBytes(body, "timeout_seconds").Int(); got != 30 {
			t.Fatalf("timeout_seconds = %d", got)
		}
		// Kimi reports its list under search_results.
		return jsonResponse(200, `{"search_results":[{"title":"K","url":"https://k.example","snippet":"s","date":"2026-01-02"}]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "kimi"}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

func TestParallelAuthenticatedShape(t *testing.T) {
	cfg := Config{ParallelAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("parallel-beta"); got != "search-extract-2025-10-10" {
			t.Fatalf("beta header = %q", got)
		}
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "mode").String(); got != "fast" {
			t.Fatalf("mode = %q", got)
		}
		if got := gjson.GetBytes(body, "max_chars_per_result").Int(); got != 10000 {
			t.Fatalf("max_chars_per_result = %d", got)
		}
		if got := gjson.GetBytes(body, "search_queries.#").Int(); got != 1 {
			t.Fatalf("search_queries count = %d, want exactly one", got)
		}
		return jsonResponse(200, `{"search_id":"s1","results":[{"title":"P","url":"https://p.example"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go site:go.dev", Provider: "parallel"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "api_key" || response.RequestID != "s1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestParallelKeylessUsesMCP(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.String(), "parallel.ai") {
			t.Fatalf("url = %s", req.URL.String())
		}
		return jsonResponse(200, `{"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"K\",\"url\":\"https://k.example\"}]}"}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "parallel"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "keyless" || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
}

func TestTinyFishPaginatesAndClamps(t *testing.T) {
	pages := 0
	cfg := Config{TinyFishAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-API-Key"); got != "k" {
			t.Fatalf("key header = %q", got)
		}
		query := req.URL.Query()
		if got := query.Get("recency_minutes"); got != "" && got != "1440" {
			t.Fatalf("recency_minutes = %q", got)
		}
		// The page size must be sent explicitly, or the API repeats page 0.
		if got := query.Get("num_results"); got != "10" {
			t.Fatalf("num_results = %q, want the capped page size", got)
		}
		pages++
		page := query.Get("page")
		if page == "1" {
			// A full page followed by a short one ends the walk.
			return jsonResponse(200, `{"results":[{"url":"https://p2.example","title":"P2"}]}`), nil
		}
		results := make([]string, 0, 10)
		for i := 0; i < 10; i++ {
			results = append(results, `{"url":"https://p1-`+string(rune('a'+i))+`.example","title":"P1`+string(rune('a'+i))+`"}`)
		}
		return jsonResponse(200, `{"results":[`+strings.Join(results, ",")+`]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{
		Query: "go", Recency: RecencyDay, NumSearchResults: 11, Provider: "tinyfish",
	})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if pages != 2 {
		t.Fatalf("pages = %d, want the walk to continue past a full first page", pages)
	}
	// 11 requested, 11 unique collected: no duplicate from the repeated page 0.
	if len(response.Sources) != 11 {
		t.Fatalf("sources = %d, want 11 without duplicates", len(response.Sources))
	}
}

func TestJinaSlicesLocally(t *testing.T) {
	cfg := Config{JinaAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer k" {
			t.Fatalf("auth = %q", got)
		}
		if !strings.HasSuffix(req.URL.Path, "/go+generics") {
			t.Fatalf("path = %s", req.URL.Path)
		}
		return jsonResponse(200, `{"data":[{"title":"A","url":"https://a.example"},{"title":"B","url":"https://b.example"},{"title":"C","url":"https://c.example"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go generics", Limit: 2, Provider: "jina"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v, want local slice to 2", response.Sources)
	}
}

func TestSyntheticAndOllamaShapes(t *testing.T) {
	synthetic := Config{SyntheticAPIKey: "k"}.WithDefaults()
	synthetic = synthetic.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		// Synthetic documents a bearer token, not an `apikey` header.
		if got := req.Header.Get("Authorization"); got != "Bearer k" {
			t.Fatalf("authorization = %q", got)
		}
		return jsonResponse(200, `{"results":[{"title":"S","url":"https://s.example"}]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), synthetic, SearchRequest{Query: "go", Provider: "synthetic"}); errExecute != nil {
		t.Fatalf("synthetic error = %v", errExecute)
	}

	ollama := Config{OllamaAPIKey: "k"}.WithDefaults()
	ollama = ollama.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if got := gjson.GetBytes(body, "max_results").Int(); got != 5 {
			t.Fatalf("max_results = %d, want the default cap", got)
		}
		return jsonResponse(200, `{"results":[{"title":"O","url":"https://o.example"}]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), ollama, SearchRequest{Query: "go", Provider: "ollama"}); errExecute != nil {
		t.Fatalf("ollama error = %v", errExecute)
	}
}

func TestExaFallsBackToPublicMCP(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.String(), "mcp.exa.ai") {
			t.Fatalf("url = %s", req.URL.String())
		}
		return jsonResponse(200, `{"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"E\",\"url\":\"https://e.example\"}]}"}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "exa"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "keyless" || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
}

// Kagi V1 reports publication time as `time`, not `published`.
func TestKagiReadsTimeField(t *testing.T) {
	cfg := Config{KagiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"meta":{"trace":"t2"},"data":{"search":[
			{"url":"https://s.example","title":"S","time":"2 hours ago"}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "kagi"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Sources[0].Published != "2 hours ago" {
		t.Fatalf("published = %q, want the time field", response.Sources[0].Published)
	}
}

// A 200 carrying a structured error array must not be read as a result set.
func TestKagiRejectsStructuredErrorBody(t *testing.T) {
	cfg := Config{KagiAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"error":[{"code":429,"message":"rate limited"}]}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "kagi"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "rate limited") {
		t.Fatalf("error = %v", errExecute)
	}
}

// A month boundary must not drift: calendar arithmetic, not a 30-day
// subtraction, keeps the window exact.
func TestKagiRecencyUsesCalendarArithmetic(t *testing.T) {
	day, okDay := kagiAfterDate(RecencyDay)
	week, okWeek := kagiAfterDate(RecencyWeek)
	month, okMonth := kagiAfterDate(RecencyMonth)
	year, okYear := kagiAfterDate(RecencyYear)
	if !okDay || !okWeek || !okMonth || !okYear {
		t.Fatal("every recency value must map to a date")
	}
	now := time.Now().UTC()
	parse := func(value string) time.Time {
		parsed, errParse := time.Parse("2006-01-02", value)
		if errParse != nil {
			t.Fatalf("bad date %q", value)
		}
		return parsed
	}
	// The bound is a calendar date, so the elapsed window varies with the
	// current time of day: "1 day ago" lands between 24h and 48h ago.
	if gap := now.Sub(parse(day)); gap < 24*time.Hour || gap > 48*time.Hour {
		t.Fatalf("day window = %v, want a date one day back", gap)
	}
	if gap := now.Sub(parse(week)); gap < 7*24*time.Hour || gap > 8*24*time.Hour {
		t.Fatalf("week window = %v, want a date seven days back", gap)
	}
	// Calendar month arithmetic always lands inside the previous month.
	monthAgo := parse(month)
	if monthAgo.Month() == now.Month() && monthAgo.Year() == now.Year() {
		t.Fatalf("month window landed in the current month: %s", month)
	}
	if parse(year).Year() != now.Year()-1 {
		t.Fatalf("year window = %s, want last year", year)
	}
}
