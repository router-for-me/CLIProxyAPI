package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/html"
)

func logSERPCleanup(err error) {
	log.WithError(err).Debug("websearch: close scraper response body")
}

// readBodyLimited reads at most limit bytes, reporting an oversize body
// rather than silently truncating a results page.
func readBodyLimited(body io.Reader, limit int64) ([]byte, error) {
	data, errRead := io.ReadAll(io.LimitReader(body, limit+1))
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

// SERP scrapers. These engines are credential-free but bot-defended, so
// each adapter issues a plain browser-profiled request and reports a
// challenge as a tagged 429 so the chain advances. There is no headless
// browser: a challenge therefore ends the provider rather than escalating.

// serpResult is one organic hit collected from a scraped results page.
type serpResult struct {
	source Source
}

// serpFetch performs a browser-profiled GET and returns the body and status.
func serpFetch(ctx context.Context, cfg Config, label, requestURL string, headers map[string]string) ([]byte, int, error) {
	merged := map[string]string{}
	for key, value := range browserHeaders() {
		merged[key] = value
	}
	for key, value := range headers {
		if value != "" {
			merged[key] = value
		}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodGet, requestURL, nil, merged)
	if errRequest != nil {
		return nil, 0, &ProviderError{Provider: label, Message: errRequest.Error()}
	}
	resp, errDo := doerFor(cfg).Do(httpReq)
	if errDo != nil {
		return nil, 0, &ProviderError{Provider: label, Message: errDo.Error()}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logSERPCleanup(errClose)
		}
	}()
	body, errRead := readBodyLimited(resp.Body, MaxSearchBodySize)
	if errRead != nil {
		return nil, resp.StatusCode, &ProviderError{Provider: label, Message: errRead.Error(), Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.StatusCode, &ProviderError{Provider: label, Message: summarizeErrorBody(body), Status: resp.StatusCode}
	}
	return body, resp.StatusCode, nil
}

// parseSerpDocument parses a results page once so several extractors can
// walk the same tree.
func parseSerpDocument(page string) *html.Node {
	doc, errParse := html.Parse(strings.NewReader(page))
	if errParse != nil {
		return nil
	}
	return doc
}

// findElement returns the first descendant element matching pred.
func findElement(root *html.Node, pred func(*html.Node) bool) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if found != nil || node == nil {
			return
		}
		if node.Type == html.ElementNode && pred(node) {
			found = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return found
}

// findElements collects every descendant element matching pred, in
// document order.
func findElements(root *html.Node, pred func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode && pred(node) {
			found = append(found, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return found
}

// hasAttr reports whether an element carries the attribute value.
func hasAttr(node *html.Node, key, value string) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}
	attr, ok := lookupHTMLAttr(node, key)
	return ok && attr == value
}

// closestAncestor walks up to the nearest ancestor matching pred.
func closestAncestor(node *html.Node, pred func(*html.Node) bool) *html.Node {
	for current := node; current != nil; current = current.Parent {
		if current.Type == html.ElementNode && pred(current) {
			return current
		}
	}
	return nil
}

// resolveSerpURL resolves a possibly relative href and rejects any target
// owned by the engine itself, so navigation chrome never becomes a result.
func resolveSerpURL(base, href, ownerHost string) string {
	href = strings.TrimSpace(href)
	if href == "" || ownerHost == "" {
		return ""
	}
	baseURL, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	parsed, errParse := url.Parse(href)
	if errParse != nil {
		return ""
	}
	parsed = baseURL.ResolveReference(parsed)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	if host == ownerHost || strings.HasSuffix(host, "."+ownerHost) {
		return ""
	}
	return parsed.String()
}

// serpSources converts collected hits into a bounded source list,
// dropping duplicate targets the way the reference adapters do: a SERP
// routinely repeats a URL across its result and its sitelink/cluster rows.
func serpSources(results []serpResult, count int) []Source {
	sources := make([]Source, 0, min(len(results), count))
	seen := make(map[string]bool, min(len(results), count))
	for _, result := range results {
		if len(sources) >= count {
			break
		}
		if result.source.URL == "" || result.source.Title == "" || seen[result.source.URL] {
			continue
		}
		seen[result.source.URL] = true
		sources = append(sources, result.source)
	}
	return sources
}

// --- Google ---------------------------------------------------------------

const googleHost = "google.com"

type googleProvider struct{}

func (googleProvider) ID() string    { return ProviderGoogle }
func (googleProvider) Label() string { return "Google" }

func (googleProvider) Available(_ Config, _ bool) bool { return true }

func (googleProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	count := clampCount(req.ResultCount(), 1, 20, 10)
	endpoint, errBuild := url.Parse("https://www.google.com/search")
	if errBuild != nil {
		return SearchResponse{}, &ProviderError{Provider: "Google", Message: errBuild.Error()}
	}
	params := endpoint.Query()
	params.Set("q", FormatScraperQuery(req.Query, parsed, GoogleQuerySyntax))
	params.Set("num", strconv.Itoa(count))
	params.Set("hl", "en")
	params.Set("gl", "us")
	// udm=14 pins the plain web-results layout. Without it Google serves
	// the AI/Universal layout, which has no per-result containers at all.
	params.Set("udm", "14")
	params.Set("pws", "0")
	if tbs, ok := googleRecency(req.Recency); ok {
		params.Set("tbs", tbs)
	}
	endpoint.RawQuery = params.Encode()
	page, status, errFetch := serpFetch(ctx, cfg, "Google", endpoint.String(), map[string]string{
		"Referer": "https://www.google.com/",
	})
	if errFetch != nil {
		return SearchResponse{}, errFetch
	}
	if reason := googleBlockReason(string(page), status); reason != "" {
		return SearchResponse{}, &ProviderError{Provider: "Google", Message: reason, Status: http.StatusTooManyRequests}
	}
	return SearchResponse{Sources: serpSources(parseGoogleResults(string(page)), count)}, nil
}

// googleBlockPattern matches the traffic interstitial variants Google
// serves. A bare `g-recaptcha` is deliberately NOT a marker: it appears in
// ordinary result markup for captcha-themed queries, so only the
// interstitial boilerplate around it counts.
var googleBlockPattern = regexp.MustCompile(`(?i)unusual traffic|our systems have detected|detected unusual traffic`)

// googleBlockReason reports why a Google SERP is unusable, or "" when it
// is a real results page. A JavaScript-only challenge is only called out
// when no organic `h3` heading is present, so a normal results page that
// merely links a Google script is not misread as blocked.
func googleBlockReason(page string, status int) string {
	lower := strings.ToLower(page)
	if status == http.StatusForbidden || status == http.StatusTooManyRequests ||
		strings.Contains(lower, "/sorry/") || googleBlockPattern.MatchString(page) {
		return "Google blocked the search with an automated-traffic challenge; try another provider or retry later."
	}
	if strings.Contains(page, "/httpservice/retry/enablejs") && !strings.Contains(lower, "<h3") {
		return "Google returned its JavaScript challenge instead of rendered search results."
	}
	return ""
}

func googleRecency(recency Recency) (string, bool) {
	switch recency {
	case RecencyDay:
		return "qdr:d", true
	case RecencyWeek:
		return "qdr:w", true
	case RecencyMonth:
		return "qdr:m", true
	case RecencyYear:
		return "qdr:y", true
	default:
		return "", false
	}
}

// parseGoogleResults anchors on the `h3` title heading and takes the
// nearest ancestor anchor, which is the shape Google emits for organic
// hits. Anchoring on a wrapper class instead silently returns nothing.
func parseGoogleResults(page string) []serpResult {
	doc := parseSerpDocument(page)
	if doc == nil {
		return nil
	}
	var results []serpResult
	seen := map[string]bool{}
	for _, heading := range findElements(doc, func(node *html.Node) bool { return node.Data == "h3" }) {
		anchor := closestAncestor(heading.Parent, func(node *html.Node) bool { return node.Data == "a" })
		if anchor == nil {
			continue
		}
		target := googleResultURL(htmlAttr(anchor, "href"))
		if target == "" || seen[target] {
			continue
		}
		title := strings.TrimSpace(htmlText(heading))
		if title == "" {
			continue
		}
		seen[target] = true
		results = append(results, serpResult{source: Source{
			Title:   title,
			URL:     target,
			Snippet: googleSnippetFor(heading),
		}})
	}
	return results
}

// googleResultURL unwraps Google's /url redirect form and rejects any
// google.com-owned target.
func googleResultURL(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "/") {
		if parsed, errParse := url.Parse("https://www.google.com" + href); errParse == nil {
			host := strings.ToLower(parsed.Hostname())
			if (host == googleHost || host == "www."+googleHost) && parsed.Path == "/url" {
				target := parsed.Query().Get("q")
				if target == "" {
					target = parsed.Query().Get("url")
				}
				if target != "" {
					href = target
				}
			}
		}
	}
	return resolveSerpURL("https://www.google.com/", href, googleHost)
}

// googleSnippetContainers is the ordered ladder of snippet containers
// Google has used, most specific first. The bare `[data-sncf='1']` wrapper
// is the last resort: it also wraps the title block, so it only wins when
// no dedicated snippet class matched.
var googleSnippetContainers = []string{"VwiC3b", "IsZvec", "BNeawe", "data-sncf"}

// googleReadMoreSuffix is the trailing affordance Google appends to some
// snippets; it is navigation, not content.
var googleReadMoreSuffix = regexp.MustCompile(`(?i)\s*Read more$`)

// googleSnippetFor finds the snippet near a title heading, walking out
// through the known result wrappers.
func googleSnippetFor(heading *html.Node) string {
	container := closestAncestor(heading, func(node *html.Node) bool {
		return hasHTMLClass(node, "tF2Cxc") || hasHTMLClass(node, "MjjYud") || hasHTMLClass(node, "Gx5Zad")
	})
	if container == nil {
		if parent := heading.Parent; parent != nil && parent.Parent != nil {
			container = parent.Parent
		}
	}
	if container == nil {
		return ""
	}
	for _, className := range googleSnippetContainers {
		node := findElement(container, func(n *html.Node) bool { return serpClassOrAttr(n, className) })
		if node == nil {
			continue
		}
		if text := normalizeSearchText(googleReadMoreSuffix.ReplaceAllString(htmlText(node), "")); text != "" {
			return text
		}
	}
	return ""
}

// serpClassOrAttr matches either a CSS class token or, for the `data-sncf`
// wrapper, a `data-sncf` attribute value.
func serpClassOrAttr(node *html.Node, key string) bool {
	if node.Type != html.ElementNode {
		return false
	}
	if key == "data-sncf" {
		return hasAttr(node, "data-sncf", "1")
	}
	if !hasHTMLClass(node, key) {
		return false
	}
	// `.BNeawe` is a bare class upstream; the `s3v9rd` modifier marks the
	// actual snippet element inside it.
	return key != "BNeawe" || hasHTMLClass(node, "s3v9rd")
}

// --- Ecosia ---------------------------------------------------------------

const ecosiaHost = "ecosia.org"

type ecosiaProvider struct{}

func (ecosiaProvider) ID() string    { return ProviderEcosia }
func (ecosiaProvider) Label() string { return "Ecosia" }

func (ecosiaProvider) Available(_ Config, _ bool) bool { return true }

func (ecosiaProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	count := clampCount(req.ResultCount(), 1, 20, 10)
	// Ecosia proxies Google's index, so classic operators pass through
	// inline; recency has no server-side knob and is left to the filter.
	endpoint, errBuild := url.Parse("https://www.ecosia.org/search")
	if errBuild != nil {
		return SearchResponse{}, &ProviderError{Provider: "Ecosia", Message: errBuild.Error()}
	}
	params := endpoint.Query()
	params.Set("q", FormatScraperQuery(req.Query, parsed, GoogleQuerySyntax))
	endpoint.RawQuery = params.Encode()
	page, status, errFetch := serpFetch(ctx, cfg, "Ecosia", endpoint.String(), map[string]string{
		"Referer": "https://www.ecosia.org/",
	})
	if errFetch != nil {
		return SearchResponse{}, errFetch
	}
	if isEcosiaBlocked(string(page), status) {
		return SearchResponse{}, &ProviderError{
			Provider: "Ecosia",
			Message:  "Ecosia throttles automated searches from datacenter or shared-egress IPs; try DuckDuckGo, Brave, or Tavily.",
			Status:   http.StatusTooManyRequests,
		}
	}
	return SearchResponse{Sources: serpSources(parseEcosiaResults(string(page)), count)}, nil
}

// isEcosiaBlocked detects the Cloudflare managed challenge, which arrives
// as a 403 titled "Ecosia Firewall" carrying the challenge bootstrap.
func isEcosiaBlocked(page string, status int) bool {
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		return true
	}
	for _, marker := range []string{
		"Ecosia Firewall",
		"_cf_chl_opt",
		"/cdn-cgi/challenge-platform/",
		"confirm you're not a robot",
		"confirm you are not a robot",
	} {
		if strings.Contains(page, marker) {
			return true
		}
	}
	return false
}

// parseEcosiaResults matches Ecosia's attribute-selected markup: an
// organic hit is `article[data-test-id=organic-result]` holding a
// `[data-test-id=result-title]` heading inside a link. Ad slots and
// entity cards use different test-ids and never match.
func parseEcosiaResults(page string) []serpResult {
	doc := parseSerpDocument(page)
	if doc == nil {
		return nil
	}
	var results []serpResult
	for _, article := range findElements(doc, func(node *html.Node) bool {
		return node.Data == "article" && hasAttr(node, "data-test-id", "organic-result")
	}) {
		heading := findElement(article, func(node *html.Node) bool {
			return hasAttr(node, "data-test-id", "result-title")
		})
		if heading == nil {
			continue
		}
		// Ecosia wraps the link either around the title heading or inside
		// it, so check the heading, its subtree, then its ancestors.
		anchor := heading
		if anchor.Data != "a" {
			anchor = findElement(heading, func(node *html.Node) bool { return node.Data == "a" })
		}
		if anchor == nil {
			anchor = closestAncestor(heading.Parent, func(node *html.Node) bool { return node.Data == "a" })
		}
		if anchor == nil {
			continue
		}
		target := resolveSerpURL("https://www.ecosia.org/", htmlAttr(anchor, "href"), ecosiaHost)
		title := strings.TrimSpace(htmlText(heading))
		if target == "" || title == "" {
			continue
		}
		// The paragraph is preferred over the container because the
		// container also holds screen-reader-only thumbnail captions.
		description := findElement(article, func(node *html.Node) bool {
			return hasAttr(node, "data-test-id", "web-result-description")
		})
		if description == nil {
			description = findElement(article, func(node *html.Node) bool {
				return hasAttr(node, "data-test-id", "result-description")
			})
		}
		snippet := ""
		if description != nil {
			snippet = strings.TrimSpace(htmlText(description))
		}
		results = append(results, serpResult{source: Source{Title: title, URL: target, Snippet: snippet}})
	}
	return results
}

// --- Mojeek ---------------------------------------------------------------

type mojeekProvider struct{}

func (mojeekProvider) ID() string    { return ProviderMojeek }
func (mojeekProvider) Label() string { return "Mojeek" }

func (mojeekProvider) Available(_ Config, _ bool) bool { return true }

func (mojeekProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	count := clampCount(req.ResultCount(), 1, 20, 10)
	endpoint, errBuild := url.Parse("https://www.mojeek.de/search")
	if errBuild != nil {
		return SearchResponse{}, &ProviderError{Provider: "Mojeek", Message: errBuild.Error()}
	}
	// Mojeek parses site:, quoted phrases, and -exclusions. Its `in*`
	// operators and YYYYMMDD date syntax differ from the Google form, so
	// those are left to the lenient post-filter.
	mojeekSyntax := QuerySyntax{Phrases: true, Negation: true, Site: true}
	params := endpoint.Query()
	params.Set("q", FormatScraperQuery(req.Query, parsed, mojeekSyntax))
	params.Set("t", strconv.Itoa(count))
	params.Set("arc", "none")
	params.Set("lang", "en")
	params.Set("lb", "en")
	params.Set("theme", "dark")
	// Mojeek's `since` filter accepts the same relative tokens as recency.
	if req.Recency != "" {
		params.Set("since", string(req.Recency))
	}
	endpoint.RawQuery = params.Encode()
	page, status, errFetch := serpFetch(ctx, cfg, "Mojeek", endpoint.String(), map[string]string{
		"Referer": "https://www.mojeek.de/?arc=none&lang=en&lb=en&theme=dark",
	})
	if errFetch != nil {
		return SearchResponse{}, errFetch
	}
	if isMojeekRobotPage(string(page), status) {
		return SearchResponse{}, &ProviderError{
			Provider: "Mojeek",
			Message:  "Mojeek blocked the request with its automated-queries wall; retry later or configure another provider such as Brave, Tavily, Exa, or Kagi.",
			Status:   http.StatusTooManyRequests,
		}
	}
	return SearchResponse{Sources: serpSources(parseMojeekResults(string(page)), count)}, nil
}

// mojeekRobotPattern matches the two robot walls Mojeek serves: the
// ALTCHA proof-of-work widget (HTTP 200) and the "automated queries"
// refusal (HTTP 403).
var mojeekRobotPattern = regexp.MustCompile(`(?i)sending automated queries`)

// isMojeekRobotPage reports whether the body is a robot wall rather than
// a results page. `results-standard` vetoes the verdict, because a real
// results page can legitimately mention the widget in its footer.
func isMojeekRobotPage(page string, status int) bool {
	if strings.Contains(page, "results-standard") {
		return false
	}
	return status == http.StatusForbidden ||
		strings.Contains(page, "altcha-widget") ||
		strings.Contains(page, "captcha-wrap") ||
		mojeekRobotPattern.MatchString(page)
}

// parseMojeekResults reads `ul.results-standard > li` blocks, whose title
// lives in `h2 a.title` and whose preview lives in `p.s`. Clustered
func parseMojeekResults(page string) []serpResult {
	doc := parseSerpDocument(page)
	if doc == nil {
		return nil
	}
	var results []serpResult
	for _, list := range findElements(doc, func(node *html.Node) bool {
		return node.Data == "ul" && hasHTMLClass(node, "results-standard")
	}) {
		for _, item := range findElements(list, func(node *html.Node) bool { return node.Data == "li" }) {
			anchor := findElement(item, func(node *html.Node) bool {
				return node.Data == "a" && hasHTMLClass(node, "title")
			})
			if anchor == nil {
				continue
			}
			target := mojeekResultURL(htmlAttr(anchor, "href"))
			title := strings.TrimSpace(htmlText(anchor))
			if target == "" || title == "" {
				continue
			}
			snippet := ""
			if preview := findElement(item, func(node *html.Node) bool {
				return node.Data == "p" && hasHTMLClass(node, "s")
			}); preview != nil {
				snippet = strings.TrimSpace(htmlText(preview))
			}
			results = append(results, serpResult{source: Source{Title: title, URL: target, Snippet: snippet}})
		}
	}
	return results
}

// mojeekHosts are the Mojeek-owned domains whose internal navigation
// rows (verticals, paging) share the organic result markup.
var mojeekHosts = []string{"mojeek.com", "mojeek.co.uk", "mojeek.fr", "mojeek.de"}

// mojeekResultURL accepts only http(s) targets that point away from every
// Mojeek-owned domain.
func mojeekResultURL(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	base, errParse := url.Parse("https://www.mojeek.de/")
	if errParse != nil {
		return ""
	}
	parsed, errParse := url.Parse(href)
	if errParse != nil {
		return ""
	}
	parsed = base.ResolveReference(parsed)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	for _, owned := range mojeekHosts {
		if host == owned || strings.HasSuffix(host, "."+owned) {
			return ""
		}
	}
	return parsed.String()
}
