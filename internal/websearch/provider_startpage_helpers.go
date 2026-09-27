package websearch

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Startpage proxies Google's index. Its search form expects a short-lived
// anti-bot token, so the adapter seeds the homepage, submits the form, and
// falls back to a tokenless GET when the token is rejected.
const startPageHost = "https://www.startpage.com"

// startPageForm builds the form fields the search endpoint expects. The
// query parameter is `query`, not `q`; `hidden` carries every hidden input
// lifted from the homepage form (including the `sc` anti-bot token) so the
// POST mirrors a real browser submission.
func startPageForm(query string, recency Recency, hidden map[string]string) url.Values {
	form := url.Values{}
	for key, value := range hidden {
		form.Set(key, value)
	}
	form.Set("query", query)
	if withDate, ok := startPageRecency(recency); ok {
		form.Set("with_date", withDate)
	}
	return form
}

func startPageRecency(recency Recency) (string, bool) {
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

// seedStartPage loads the homepage to obtain the anti-bot form token, the
// sibling hidden inputs that must accompany it, and the cookies the
// follow-up request must carry. Any failure is non-fatal: the caller falls
// back to a tokenless GET.
func seedStartPage(ctx context.Context, cfg Config) (cookie string, hidden map[string]string, err error) {
	httpReq, errRequest := newJSONRequest(ctx, http.MethodGet, startPageHost+"/", nil, browserHeaders())
	if errRequest != nil {
		return "", nil, &ProviderError{Provider: "Startpage", Message: errRequest.Error()}
	}
	resp, errDo := doerFor(cfg).Do(httpReq)
	if errDo != nil {
		return "", nil, &ProviderError{Provider: "Startpage", Message: errDo.Error()}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, MaxSearchBodySize))
	if errRead != nil {
		return "", nil, &ProviderError{Provider: "Startpage", Message: errRead.Error(), Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, &ProviderError{Provider: "Startpage", Message: summarizeErrorBody(body), Status: resp.StatusCode}
	}
	return cookieHeader(resp), parseStartPageFormInputs(string(body)), nil
}

// parseStartPageFormInputs lifts every hidden input from the homepage's
// /sp/search form. It returns nil unless the form exists and carries the
// `sc` anti-bot token, so a markup change degrades to the tokenless GET
// rather than posting a doomed form.
func parseStartPageFormInputs(page string) map[string]string {
	doc, errParse := html.Parse(strings.NewReader(page))
	if errParse != nil {
		return nil
	}
	var form *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if form != nil || node == nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "form" && strings.Contains(htmlAttr(node, "action"), "/sp/search") {
			form = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if form == nil {
		return nil
	}
	inputs := map[string]string{}
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "input" {
			if inputType, _ := lookupHTMLAttr(node, "type"); inputType == "hidden" {
				if name, ok := lookupHTMLAttr(node, "name"); ok && name != "" {
					inputs[name] = htmlAttr(node, "value")
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(form)
	if inputs["sc"] == "" {
		return nil
	}
	return inputs
}

func cookieHeader(resp *http.Response) string {
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func startPageFetch(ctx context.Context, cfg Config, cookie string, form url.Values) ([]byte, int, error) {
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, startPageHost+"/sp/search", []byte(form.Encode()), map[string]string{
		"Content-Type":    "application/x-www-form-urlencoded",
		"Accept":          "text/html,application/xhtml+xml",
		"Accept-Language": "en-US,en;q=0.9",
		"User-Agent":      browserHeaders()["User-Agent"],
		"Referer":         startPageHost + "/",
		"Cookie":          cookie,
	})
	if errRequest != nil {
		return nil, 0, &ProviderError{Provider: "Startpage", Message: errRequest.Error()}
	}
	resp, errDo := doerFor(cfg).Do(httpReq)
	if errDo != nil {
		return nil, 0, &ProviderError{Provider: "Startpage", Message: errDo.Error()}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, MaxSearchBodySize))
	if errRead != nil {
		return nil, resp.StatusCode, &ProviderError{Provider: "Startpage", Message: errRead.Error(), Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.StatusCode, &ProviderError{Provider: "Startpage", Message: summarizeErrorBody(body), Status: resp.StatusCode}
	}
	return body, resp.StatusCode, nil
}

func startPageTokenlessGet(ctx context.Context, cfg Config, cookie string, form url.Values) ([]byte, int, error) {
	httpReq, errRequest := newJSONRequest(ctx, http.MethodGet, startPageHost+"/sp/search?"+form.Encode(), nil, map[string]string{
		"Accept":          "text/html,application/xhtml+xml",
		"Accept-Language": "en-US,en;q=0.9",
		"User-Agent":      browserHeaders()["User-Agent"],
		"Referer":         startPageHost + "/",
		"Cookie":          cookie,
	})
	if errRequest != nil {
		return nil, 0, &ProviderError{Provider: "Startpage", Message: errRequest.Error()}
	}
	resp, errDo := doerFor(cfg).Do(httpReq)
	if errDo != nil {
		return nil, 0, &ProviderError{Provider: "Startpage", Message: errDo.Error()}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, MaxSearchBodySize))
	if errRead != nil {
		return nil, resp.StatusCode, &ProviderError{Provider: "Startpage", Message: errRead.Error(), Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.StatusCode, &ProviderError{Provider: "Startpage", Message: summarizeErrorBody(body), Status: resp.StatusCode}
	}
	return body, resp.StatusCode, nil
}

// isStartPageChallenge detects Startpage's CAPTCHA interstitial by the
// markers it actually renders. A bare "captcha" substring is deliberately
// NOT used: result snippets for captcha-related queries would false-positive.
func isStartPageChallenge(page string) bool {
	return strings.Contains(page, "component---src-pages-captcha") || strings.Contains(page, "/sp/captcha")
}

// parseStartPageResults walks the server-rendered results page in document
// order. Each organic hit lives in a `div.result` container holding the
// title anchor `a.result-link` (with an `h2.wgl-title` heading) and an
// optional `p.description` snippet. Hrefs are direct target URLs — Startpage
// does not wrap outbound clicks. The offscreen adblock honeypot uses the
// class token `a-bg-result`, which an exact class match ignores, and
// sponsored placements render outside `div.result` containers.
func parseStartPageResults(page string, count int) []Source {
	doc, errParse := html.Parse(strings.NewReader(page))
	if errParse != nil {
		return nil
	}
	var sources []Source
	seen := make(map[string]bool, count)
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if len(sources) >= count {
			return
		}
		if !isStartPageResultBlock(node) {
			return
		}
		anchor := startPageFindElement(node, func(a *html.Node) bool {
			return a.Data == "a" && hasHTMLClass(a, "result-link")
		})
		if anchor == nil {
			return
		}
		target := startPageSanitizeURL(htmlAttr(anchor, "href"))
		if target == "" || seen[target] {
			return
		}
		title := strings.TrimSpace(startPageHeadingText(anchor))
		if title == "" {
			return
		}
		seen[target] = true
		sources = append(sources, Source{Title: title, URL: target, Snippet: startPageFindSnippet(node)})
	}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil || len(sources) >= count {
			return
		}
		collect(node)
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return sources
}

func isStartPageResultBlock(node *html.Node) bool {
	return node.Type == html.ElementNode && node.Data == "div" && hasHTMLClass(node, "result")
}

// startPageSanitizeURL accepts only http(s) targets that point away from
// Startpage itself.
func startPageSanitizeURL(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	base, errParse := url.Parse(startPageHost)
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
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	if host == "startpage.com" || strings.HasSuffix(host, ".startpage.com") {
		return ""
	}
	return parsed.String()
}

// startPageFindElement returns the first descendant element matching pred.
func startPageFindElement(container *html.Node, pred func(*html.Node) bool) *html.Node {
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
	walk(container)
	return found
}

// startPageHeadingText prefers the anchor's first h2/h3 heading, in document
// order, over the anchor's raw text. The raw text also contains the
// displayed URL, which must not become part of the title.
func startPageHeadingText(anchor *html.Node) string {
	heading := startPageFindElement(anchor, func(node *html.Node) bool {
		return node.Data == "h2" || node.Data == "h3"
	})
	if heading != nil {
		if text := normalizeSearchText(htmlText(heading)); text != "" {
			return text
		}
	}
	return normalizeSearchText(htmlText(anchor))
}

// startPageFindSnippet returns the block's p.description text.
func startPageFindSnippet(container *html.Node) string {
	paragraph := startPageFindElement(container, func(node *html.Node) bool {
		return node.Data == "p" && hasHTMLClass(node, "description")
	})
	if paragraph == nil {
		return ""
	}
	return normalizeSearchText(htmlText(paragraph))
}

func lookupHTMLAttr(node *html.Node, key string) (string, bool) {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val, true
		}
	}
	return "", false
}
