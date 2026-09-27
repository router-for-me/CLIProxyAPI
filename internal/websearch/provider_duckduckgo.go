package websearch

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/html"
)

// duckDuckGoProvider scrapes the credential-free DuckDuckGo HTML frontend.
// Challenged or bot-gated responses surface as provider errors so the chain
// advances instead of hanging on a browser escalation.
type duckDuckGoProvider struct{}

func (duckDuckGoProvider) ID() string    { return ProviderDuckDuckGo }
func (duckDuckGoProvider) Label() string { return "DuckDuckGo" }

func (duckDuckGoProvider) Available(_ Config, _ bool) bool { return true }

func (duckDuckGoProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	count := clampCount(req.ResultCount(), 1, 20, 10)
	form := duckDuckGoForm(req, parsed)
	var sources []Source
	seen := map[string]bool{}
	// The frontend exposes a next-page form; keep following it until the
	// requested count is met, the form disappears, or a page adds nothing.
	for len(sources) < count && len(form) > 0 {
		page, errPage := duckDuckGoFetch(ctx, cfg, form)
		if errPage != nil {
			return SearchResponse{}, errPage
		}
		before := len(sources)
		for _, result := range parseDuckDuckGoResults(page) {
			if seen[result.URL] {
				continue
			}
			seen[result.URL] = true
			sources = append(sources, result)
			if len(sources) >= count {
				break
			}
		}
		if len(sources) == before {
			// A page that adds nothing means pagination has stalled.
			break
		}
		form = parseDuckDuckGoContinuation(page)
	}
	return SearchResponse{Sources: capSources(sources, count)}, nil
}

// ddgQuerySyntax is the operator set the HTML frontend parses. Date bounds
// are deliberately excluded: DuckDuckGo does not parse them, so they are
// stripped from the query and enforced by the lenient post-filter instead.
var ddgQuerySyntax = QuerySyntax{
	Phrases:   true,
	Negation:  true,
	Or:        true,
	Site:      true,
	InURL:     true,
	InTitle:   true,
	InText:    true,
	FileType:  true,
	DateRange: false,
}

// ddgLocaleCodes are the documented `kl` values. They resemble
// region-language locales but contain provider-specific identifiers that
// cannot be derived mechanically.
var ddgLocaleCodes = map[string]bool{
	"xa-ar": true, "xa-en": true, "ar-es": true, "au-en": true, "at-de": true,
	"be-fr": true, "be-nl": true, "br-pt": true, "bg-bg": true, "ca-en": true,
	"ca-fr": true, "ct-ca": true, "cl-es": true, "cn-zh": true, "co-es": true,
	"hr-hr": true, "cz-cs": true, "dk-da": true, "ee-et": true, "fi-fi": true,
	"fr-fr": true, "de-de": true, "gr-el": true, "hk-tzh": true, "hu-hu": true,
	"in-en": true, "id-id": true, "id-en": true, "ie-en": true, "il-he": true,
	"it-it": true, "jp-jp": true, "kr-kr": true, "lv-lv": true, "lt-lt": true,
	"xl-es": true, "my-ms": true, "my-en": true, "mx-es": true, "nl-nl": true,
	"nz-en": true, "no-no": true, "pe-es": true, "ph-en": true, "ph-tl": true,
	"pl-pl": true, "pt-pt": true, "ro-ro": true, "ru-ru": true, "sg-en": true,
	"sk-sk": true, "sl-sl": true, "za-en": true, "es-es": true, "se-sv": true,
	"ch-de": true, "ch-fr": true, "ch-it": true, "tw-tzh": true, "th-th": true,
	"tr-tr": true, "ua-uk": true, "uk-en": true, "us-en": true, "ue-es": true,
	"ve-es": true, "vn-vi": true, "wt-wt": true,
}

// ddgLocaleAliases are BCP 47 locales whose DuckDuckGo code does not
// follow a component swap.
var ddgLocaleAliases = map[string]string{
	"ca-es":  "ct-ca",
	"en-gb":  "uk-en",
	"es-419": "xl-es",
	"es-us":  "ue-es",
	"ja-jp":  "jp-jp",
	"ko-kr":  "kr-kr",
	"zh-hk":  "hk-tzh",
	"zh-tw":  "tw-tzh",
}

var ddgLocalePattern = regexp.MustCompile(`^([a-z]{2})-([a-z]{2})$`)

// duckDuckGoKl maps a `lang:` locale onto DuckDuckGo's documented `kl`
// values. Shared queries use language-region order while DDG generally uses
// region-language, and provider-specific exceptions resolve through the
// alias table. Anything outside the allowlist keeps the default region.
func duckDuckGoKl(lang string) string {
	if lang == "" {
		return ""
	}
	locale := strings.ReplaceAll(strings.ToLower(lang), "_", "-")
	if alias, ok := ddgLocaleAliases[locale]; ok {
		return alias
	}
	match := ddgLocalePattern.FindStringSubmatch(locale)
	if match == nil {
		return ""
	}
	candidate := match[2] + "-" + match[1]
	if ddgLocaleCodes[candidate] {
		return candidate
	}
	return ""
}

func duckDuckGoForm(req SearchRequest, parsed ParsedQuery) url.Values {
	form := url.Values{}
	form.Set("q", FormatScraperQuery(req.Query, parsed, ddgQuerySyntax))
	form.Set("kl", firstNonEmpty(duckDuckGoKl(parsed.Lang), "us-en"))
	if df, ok := duckDuckGoRecency(req.Recency); ok {
		form.Set("df", df)
	}
	// Real browser submissions include an empty b parameter; omitting it is
	// part of the fingerprint the anomaly challenge keys on.
	form.Set("b", "")
	return form
}

func duckDuckGoRecency(recency Recency) (string, bool) {
	switch recency {
	case RecencyDay:
		return "d", true
	case RecencyWeek:
		return "w", true
	case RecencyMonth:
		return "m", true
	case RecencyYear:
		return "y", true
	default:
		return "", false
	}
}

func duckDuckGoFetch(ctx context.Context, cfg Config, form url.Values) (string, error) {
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if errRequest != nil {
		return "", &ProviderError{Provider: "DuckDuckGo", Message: errRequest.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Referer", "https://html.duckduckgo.com/")
	for key, value := range browserHeaders() {
		httpReq.Header.Set(key, value)
	}
	resp, errDo := doerFor(cfg).Do(httpReq)
	if errDo != nil {
		return "", &ProviderError{Provider: "DuckDuckGo", Message: errDo.Error()}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("websearch: close duckduckgo response body")
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, MaxSearchBodySize+1))
	if errRead != nil {
		return "", &ProviderError{Provider: "DuckDuckGo", Message: errRead.Error(), Status: resp.StatusCode}
	}
	if int64(len(body)) > MaxSearchBodySize {
		return "", &ProviderError{Provider: "DuckDuckGo", Message: "response too large", Status: resp.StatusCode}
	}
	// DuckDuckGo mixes 200 and 202 on bot challenges, so the body is the
	// reliable signal rather than the status code.
	if isDuckDuckGoChallenge(body) {
		return "", &ProviderError{
			Provider: "DuckDuckGo",
			Message:  "blocked with a bot-detection challenge; it throttles automated searches from datacenter or shared-egress IPs, so prefer a credentialed provider such as Brave, Tavily, Exa, or Kagi.",
			Status:   http.StatusTooManyRequests,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &ProviderError{
			Provider: "DuckDuckGo",
			Message:  "HTML error (" + strconv.Itoa(resp.StatusCode) + ")",
			Status:   resp.StatusCode,
		}
	}
	return string(body), nil
}

func isDuckDuckGoChallenge(body []byte) bool {
	text := string(body)
	return strings.Contains(text, "anomaly-modal") || strings.Contains(text, "anomaly.js")
}

// parseDuckDuckGoResults pulls result rows in document order. Each result
// lives in a `div.result` container holding the title anchor
// `a.result__a` and an optional `result__snippet` sibling; scoping the
// snippet to the same block keeps a sponsored or instant-answer row from
// overwriting an organic result's preview.
func parseDuckDuckGoResults(page string) []Source {
	doc, errParse := html.Parse(strings.NewReader(page))
	if errParse != nil {
		return nil
	}
	var sources []Source
	for _, block := range findElements(doc, isDuckDuckGoResultBlock) {
		anchor := findElement(block, func(node *html.Node) bool {
			return node.Data == "a" && hasHTMLClass(node, "result__a")
		})
		if anchor == nil {
			continue
		}
		target := unwrapDuckDuckGoURL(htmlAttr(anchor, "href"))
		title := strings.TrimSpace(normalizeSearchText(htmlText(anchor)))
		if target == "" || title == "" {
			continue
		}
		source := Source{Title: title, URL: target, Published: duckDuckGoPublishedDate(block)}
		if snippet := findElement(block, func(node *html.Node) bool {
			return hasHTMLClass(node, "result__snippet")
		}); snippet != nil {
			source.Snippet = strings.TrimSpace(normalizeSearchText(htmlText(snippet)))
		}
		sources = append(sources, source)
	}
	return sources
}

func isDuckDuckGoResultBlock(node *html.Node) bool {
	return node.Type == html.ElementNode && node.Data == "div" && hasHTMLClass(node, "result")
}

var duckDuckGoTimestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2})?`)

// duckDuckGoPublishedDate lifts the publication timestamp from the row's
// extras container. The scan is restricted to that container so a
// date-shaped value inside a snippet is not mistaken for metadata.
func duckDuckGoPublishedDate(block *html.Node) string {
	extras := findElement(block, func(node *html.Node) bool {
		return hasHTMLClass(node, "result__extras__url")
	})
	if extras == nil {
		return ""
	}
	for _, span := range findElements(extras, func(node *html.Node) bool { return node.Data == "span" }) {
		text := strings.TrimSpace(normalizeSearchText(htmlText(span)))
		if duckDuckGoTimestampPattern.MatchString(text) {
			return text
		}
	}
	return ""
}

// parseDuckDuckGoContinuation extracts the hidden fields of the next-page
// form. Attribute order varies, so each input is read independently.
func parseDuckDuckGoContinuation(page string) url.Values {
	doc, errParse := html.Parse(strings.NewReader(page))
	if errParse != nil {
		return nil
	}
	for _, form := range findElements(doc, func(node *html.Node) bool { return node.Data == "form" }) {
		fields := url.Values{}
		for _, input := range findElements(form, func(node *html.Node) bool { return node.Data == "input" }) {
			if inputType, _ := lookupHTMLAttr(input, "type"); inputType != "hidden" {
				continue
			}
			name, ok := lookupHTMLAttr(input, "name")
			if !ok || name == "" {
				continue
			}
			fields.Set(name, htmlAttr(input, "value"))
		}
		if fields.Get("s") != "" && fields.Get("vqd") != "" {
			return fields
		}
	}
	return nil
}

// unwrapDuckDuckGoURL resolves a result href back to its target. DuckDuckGo
// routes outbound clicks through //duckduckgo.com/l/?uddg=<encoded> for
// analytics; the page also mixes in protocol-relative and absolute links.
func unwrapDuckDuckGoURL(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	href = strings.ReplaceAll(href, "&amp;", "&")
	if match := uddgPattern.FindStringSubmatch(href); match != nil {
		decoded, errDecode := url.QueryUnescape(match[1])
		if errDecode != nil {
			return ""
		}
		return decoded
	}
	switch {
	case strings.HasPrefix(href, "//"):
		return "https:" + href
	case strings.HasPrefix(href, "http://"), strings.HasPrefix(href, "https://"):
		return href
	default:
		return ""
	}
}

var uddgPattern = regexp.MustCompile(`[?&]uddg=([^&]+)`)

var whitespaceRunPattern = regexp.MustCompile(`\s+`)

// normalizeSearchText folds whitespace runs and trims, so a title or snippet
// containing a newline cannot be misread as a new line in the rendered
// Sources block.
func normalizeSearchText(value string) string {
	return strings.TrimSpace(whitespaceRunPattern.ReplaceAllString(value, " "))
}

func hasHTMLClass(node *html.Node, class string) bool {
	for _, attr := range node.Attr {
		if attr.Key != "class" {
			continue
		}
		for _, token := range strings.Fields(attr.Val) {
			if token == class {
				return true
			}
		}
	}
	return false
}

func htmlAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func htmlText(node *html.Node) string {
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return out.String()
}
