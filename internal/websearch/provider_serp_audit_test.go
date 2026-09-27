package websearch

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Google's snippet ladder is ordered most-specific-first and ends at the
// bare `[data-sncf='1']` wrapper. The old ladder led with the wrapper, so a
// page carrying both a VwiC3b snippet and a sncf wrapper returned the whole
// result block — title included — as the snippet.
func TestGooglePrefersDedicatedSnippetOverSncfWrapper(t *testing.T) {
	page := `<html><body><div class="MjjYud">
		<a href="https://go.dev/doc"><h3>Go Docs</h3></a>
		<div data-sncf="1">
			<h3>Go Docs</h3>
			<div class="VwiC3b">the real snippet</div>
		</div>
	</div></body></html>`
	results := parseGoogleResults(page)
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one hit", results)
	}
	if results[0].source.Snippet != "the real snippet" {
		t.Fatalf("snippet = %q, want the VwiC3b text, not the sncf wrapper", results[0].source.Snippet)
	}
}

// `.BNeawe` is a bare class upstream; `s3v9rd` is the modifier that marks
// the snippet element inside it. Matching the bare class latched onto the
// wrong container.
func TestGoogleMatchesBNeaweSnippetModifier(t *testing.T) {
	page := `<html><body><div class="tF2Cxc">
		<a href="https://example.com"><h3>Title</h3></a>
		<div class="BNeawe"><span>sibling text</span></div>
		<div class="BNeawe s3v9rd">modifier snippet</div>
	</div></body></html>`
	results := parseGoogleResults(page)
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].source.Snippet != "modifier snippet" {
		t.Fatalf("snippet = %q, want the s3v9rd element", results[0].source.Snippet)
	}
}

// Google appends a "Read more" affordance to some snippets; it is
// navigation, not content, and is stripped upstream.
func TestGoogleStripsReadMoreSuffix(t *testing.T) {
	page := `<html><body><div class="MjjYud">
		<a href="https://example.com/a"><h3>A</h3></a>
		<div class="VwiC3b">snippet body Read more</div>
	</div></body></html>`
	results := parseGoogleResults(page)
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].source.Snippet != "snippet body" {
		t.Fatalf("snippet = %q, want the Read more affordance stripped", results[0].source.Snippet)
	}
}

// A real results page routinely mentions captcha/recaptcha in its markup.
// Only the actual interstitial — the retry-enablejs bootstrap with no
// organic heading — may be called a challenge.
func TestGoogleDoesNotFalsePositiveOnChallengeWordsInResults(t *testing.T) {
	page := `<html><body><div class="MjjYud">
		<a href="https://example.com/captcha"><h3>How to bypass a captcha</h3></a>
		<div class="VwiC3b">g-recaptcha troubleshooting guide</div>
	</div></body></html>`
	if reason := googleBlockReason(page, 200); reason != "" {
		t.Fatalf("blockReason = %q, want a captcha-themed result page to pass", reason)
	}
	if reason := googleBlockReason(`<html><body><script src="/httpservice/retry/enablejs"></script></body></html>`, 200); reason == "" {
		t.Fatal("the JavaScript-only interstitial must be reported as a challenge")
	}
}

// A SERP repeats a URL across its organic row and its sitelink/cluster
// rows; the source list must not carry the duplicate.
func TestSerpSourcesDropsDuplicateURLs(t *testing.T) {
	results := []serpResult{
		{source: Source{Title: "First", URL: "https://a.example"}},
		{source: Source{Title: "Sitelink", URL: "https://a.example"}},
		{source: Source{Title: "Second", URL: "https://b.example"}},
	}
	sources := serpSources(results, 10)
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want the duplicate target dropped", sources)
	}
	if sources[1].URL != "https://b.example" {
		t.Fatalf("sources = %+v", sources)
	}
}

// Mojeek internal navigation (verticals, paging) is spread across four
// owned domains. Rejecting only mojeek.de let mojeek.com rows become
// results, exactly as upstream does not.
func TestMojeekRejectsEveryOwnedDomain(t *testing.T) {
	for _, href := range []string{
		"https://www.mojeek.com/search?q=x",
		"https://www.mojeek.co.uk/search",
		"https://www.mojeek.fr/search",
		"https://www.mojeek.de/images",
		"https://help.mojeek.com/faq",
	} {
		if got := mojeekResultURL(href); got != "" {
			t.Fatalf("mojeekResultURL(%q) = %q, want it rejected", href, got)
		}
	}
	if got := mojeekResultURL("https://example.com/result"); got != "https://example.com/result" {
		t.Fatalf("mojeekResultURL = %q, want the external target kept", got)
	}
}

// The ALTCHA wall arrives as HTTP 200, so a status-only check let the
// wall through and returned zero results instead of advancing the chain.
// `results-standard` vetoes the verdict so a real page is never misread.
func TestMojeekDetectsProofOfWorkWallOnHTTP200(t *testing.T) {
	if !isMojeekRobotPage(`<html><body><altcha-widget></altcha-widget></body></html>`, 200) {
		t.Fatal("the ALTCHA proof-of-work wall must be detected on a 200 response")
	}
	if !isMojeekRobotPage(`<html><body>Sorry, we are not sending automated queries</body></html>`, 200) {
		t.Fatal("the automated-queries refusal must be detected")
	}
	real := `<html><body><ul class="results-standard"><li><a class="title" href="https://a.example">A</a></li></ul>
		<altcha-widget></altcha-widget></body></html>`
	if isMojeekRobotPage(real, 200) {
		t.Fatal("a page that actually carries results must not be called a robot wall")
	}
}

// The wall must reach the caller as an error, not as an empty result set.
func TestMojeekProofOfWorkWallAdvancesChain(t *testing.T) {
	cfg := Config{}.WithDefaults().WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return htmlResponse(200, `<html><body><altcha-widget></altcha-widget></body></html>`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "mojeek"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "automated-queries") {
		t.Fatalf("error = %v, want the robot wall reported", errExecute)
	}
}
