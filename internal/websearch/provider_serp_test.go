package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func htmlResponse(status int, page string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(page)),
	}
}

func TestStartpageSeedsTokenThenPosts(t *testing.T) {
	sawSeed, sawPost := false, false
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/":
			sawSeed = true
			// A real homepage form carries several hidden inputs; the `sc`
			// token must be replayed alongside its siblings.
			return htmlResponse(200, `<html><form action="/sp/search">
				<input type="hidden" name="sc" value="tok">
				<input type="hidden" name="lui" value="english">
				<input type="text" name="query">
			</form></html>`), nil
		case req.URL.Path == "/sp/search" && req.Method == http.MethodPost:
			sawPost = true
			body, _ := io.ReadAll(req.Body)
			form := string(body)
			for _, want := range []string{"sc=tok", "lui=english", "query=go", "with_date=w"} {
				if !strings.Contains(form, want) {
					t.Fatalf("form missing %q: %s", want, form)
				}
			}
			// The endpoint parameter is `query`, never `q`.
			if strings.Contains(form, "q=") {
				t.Fatalf("form must not send a bare q parameter: %s", form)
			}
			return htmlResponse(200, `<html><body>
				<div class="result">
					<a class="result-link" href="https://go.dev/doc">
						<h2 class="wgl-title">Go Docs</h2>
					</a>
					<p class="description">the go documentation</p>
				</div>
			</body></html>`), nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyWeek, Provider: "startpage"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if !sawSeed || !sawPost {
		t.Fatalf("seed=%v post=%v", sawSeed, sawPost)
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://go.dev/doc" {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if response.Sources[0].Title != "Go Docs" || response.Sources[0].Snippet != "the go documentation" {
		t.Fatalf("source = %+v", response.Sources[0])
	}
}

// Startpage posts the form only when the homepage exposes a `/sp/search`
// form carrying the `sc` anti-bot token; otherwise the request degrades
// straight to a tokenless GET.
func TestStartpageWithoutFormFallsBackToTokenlessGet(t *testing.T) {
	sawPost, sawGet := false, false
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/":
			// A homepage with no usable form: no /sp/search action, or a
			// form missing its sc token.
			return htmlResponse(200, `<html><body><form action="/other"><input type="hidden" name="sc" value="tok"></form></body></html>`), nil
		case req.Method == http.MethodPost:
			sawPost = true
			return htmlResponse(200, `<html><body>should not happen</body></html>`), nil
		default:
			sawGet = true
			if got := req.URL.Query().Get("query"); got != "go" {
				t.Fatalf("tokenless GET query = %q", got)
			}
			if _, present := req.URL.Query()["sc"]; present {
				t.Fatal("tokenless GET must not carry an sc token")
			}
			return htmlResponse(200, `<html><body>
				<div class="result">
					<a class="result-link" href="https://fallback.example"><h2>Fallback</h2></a>
					<p class="description">tokenless hit</p>
				</div>
			</body></html>`), nil
		}
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "startpage"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if sawPost {
		t.Fatal("must not post a form when the homepage yields no token")
	}
	if !sawGet {
		t.Fatal("tokenless GET fallback was not attempted")
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://fallback.example" {
		t.Fatalf("sources = %+v", response.Sources)
	}
}

func TestStartpageChallengeAdvancesChain(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return htmlResponse(200, `<html><body><div class="component---src-pages-captcha">challenge</div></body></html>`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "startpage"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "CAPTCHA") {
		t.Fatalf("error = %v", errExecute)
	}
}

// A captcha-related *query* must not be mistaken for a captcha *page*;
// Startpage snippet text is quoted verbatim in results.
func TestStartpageChallengeDoesNotFalsePositiveOnQueryText(t *testing.T) {
	page := `<html><body><div class="result">
		<a class="result-link" href="https://example.com/how"><h2>How to bypass a captcha</h2></a>
		<p class="description">Enable javascript and cookies to continue reading.</p>
	</div></body></html>`
	if isStartPageChallenge(page) {
		t.Fatal("result text mentioning captcha must not trigger the challenge detector")
	}
}

func TestGoogleUnwrapsRedirectAndMapsRecency(t *testing.T) {
	sawSearch := false
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/search" {
			sawSearch = true
			if got := req.URL.Query().Get("tbs"); got != "qdr:m" {
				t.Fatalf("tbs = %q", got)
			}
			// udm=14 pins the plain web-results layout; without it Google
			// serves a page with no per-result containers.
			if got := req.URL.Query().Get("udm"); got != "14" {
				t.Fatalf("udm = %q, want 14", got)
			}
			return htmlResponse(200, `<html><body>
				<div class="MjjYud">
					<a href="/url?q=https://go.dev/doc&amp;sa=U">
						<h3>Go Docs</h3>
					</a>
					<div class="VwiC3b">documentation snippet</div>
				</div>
			</body></html>`), nil
		}
		return htmlResponse(200, `<html><body>home</body></html>`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyMonth, Provider: "google"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if !sawSearch {
		t.Fatal("google did not load a SERP")
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://go.dev/doc" {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if response.Sources[0].Snippet != "documentation snippet" {
		t.Fatalf("snippet = %+v", response.Sources[0])
	}
}

func TestGoogleChallengeAdvancesChain(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return htmlResponse(200, `<html><body>Our systems have detected unusual traffic from your computer network</body></html>`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "google"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "challenge") {
		t.Fatalf("error = %v", errExecute)
	}
}

func TestEcosiaIgnoresRecency(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("since") != "" || req.URL.Query().Get("tbs") != "" {
			t.Fatalf("ecosia must not send a recency filter: %s", req.URL.String())
		}
		// Ecosia selects results with data-test-id attributes, not classes.
		return htmlResponse(200, `<html><body>
			<article data-test-id="organic-result">
				<h2 data-test-id="result-title"><a href="https://ecosia.example">Ecosia Hit</a></h2>
				<p data-test-id="web-result-description">ecosia snippet</p>
			</article>
		</body></html>`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyDay, Provider: "ecosia"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://ecosia.example" {
		t.Fatalf("sources = %+v", response.Sources)
	}
}

func TestMojeekMapsSinceAndFlagsRobot(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		if got := query.Get("since"); got != "week" {
			t.Fatalf("since = %q, want the verbatim recency token", got)
		}
		if got := query.Get("t"); got == "" {
			t.Fatalf("missing result-count param t: %s", req.URL.String())
		}
		if got := query.Get("arc"); got != "none" {
			t.Fatalf("arc = %q", got)
		}
		return htmlResponse(200, `<html><body><ul class="results-standard">
			<li class="r"><h2><a class="title" href="https://mojeek.example">Mojeek Hit</a></h2><p class="s">mojeek snippet</p></li>
		</ul></body></html>`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyWeek, Provider: "mojeek"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://mojeek.example" {
		t.Fatalf("sources = %+v", response.Sources)
	}

	blocked := Config{}.WithDefaults().WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusForbidden, `<html><body>blocked</body></html>`), nil
	}})
	if _, errExecute := Execute(context.Background(), blocked, SearchRequest{Query: "go", Provider: "mojeek"}); errExecute == nil {
		t.Fatal("a robot wall must advance the chain")
	}
}

func TestSerpRecencyMappings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping func(Recency) (string, bool)
		recency Recency
		want    string
	}{
		{"google-day", googleRecency, RecencyDay, "qdr:d"},
		{"google-year", googleRecency, RecencyYear, "qdr:y"},
		{"startpage-day", startPageRecency, RecencyDay, "d"},
		{"startpage-year", startPageRecency, RecencyYear, "y"},
		{"google-month", googleRecency, RecencyMonth, "qdr:m"},
	} {
		value, ok := tc.mapping(tc.recency)
		if !ok || value != tc.want {
			t.Fatalf("%s = %q/%v, want %q", tc.name, value, ok, tc.want)
		}
		if value, ok := tc.mapping(""); ok {
			t.Fatalf("%s must ignore an unset recency, got %q", tc.name, value)
		}
	}
}
