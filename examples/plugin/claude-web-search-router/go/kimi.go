package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/sirupsen/logrus"
)

const (
	kimiDefaultBaseURL = "https://api.moonshot.ai"
	kimiDefaultModel   = "kimi-k3"

	kimiSearchBasicPath = "/v1/tools/search"
	kimiSearchProPath   = "/v1/tools/search_pro"
	kimiFetchPath       = "/v1/tools/fetch"
	kimiResponsesPath   = "/v1/responses"

	// kimiFetchSnippetCap bounds the Markdown body surfaced as a search snippet.
	kimiFetchSnippetCap = 8000
)

type kimiClient struct {
	keys    []string
	idx     atomic.Uint64
	http    *http.Client
	baseURL string // empty → https://api.moonshot.ai
}

func newKimiClient(keys []string, baseURL string) *kimiClient {
	return newKimiClientWithOptions(keys, baseURL, nil)
}

func newKimiClientWithOptions(keys []string, baseURL string, httpClient *http.Client) *kimiClient {
	trimmed := make([]string, 0, len(keys))
	for _, key := range keys {
		if k := strings.TrimSpace(key); k != "" {
			trimmed = append(trimmed, k)
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &kimiClient{
		keys:    trimmed,
		http:    httpClient,
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
	}
}

// sharedKimiClient returns the process-wide client for the given config,
// creating it on first use and refreshing it when the config changes. Reusing
// the client keeps the round-robin key counter advancing across requests; a
// fresh client per request restarts the counter at zero and pins every
// one-call request onto the first configured key.
var (
	sharedKimiClientMu   sync.Mutex
	sharedKimiClientPtr  *kimiClient
	sharedKimiClientKeys []string
	sharedKimiClientBase string
)

func sharedKimiClient(keys []string, baseURL string) *kimiClient {
	normalizedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	sharedKimiClientMu.Lock()
	defer sharedKimiClientMu.Unlock()
	if sharedKimiClientPtr != nil && sharedKimiClientBase == normalizedBase && slices.Equal(sharedKimiClientKeys, keys) {
		return sharedKimiClientPtr
	}
	client := newKimiClient(keys, baseURL)
	sharedKimiClientPtr = client
	sharedKimiClientKeys = append([]string(nil), keys...)
	sharedKimiClientBase = client.baseURL
	return client
}

func (c *kimiClient) endpoint(path string) string {
	base := c.baseURL
	if base == "" {
		base = kimiDefaultBaseURL
	}
	return base + path
}

func (c *kimiClient) available() bool {
	return c != nil && len(c.keys) > 0
}

func (c *kimiClient) nextKey() string {
	if len(c.keys) == 0 {
		return ""
	}
	n := c.idx.Add(1)
	return c.keys[int(n-1)%len(c.keys)]
}

// hostHTTPContextKey carries the host-injected HTTPClient through the call
// chain so Kimi upstream requests follow host transport policy (proxy-url)
// and request-log capture instead of opening direct connections.
type hostHTTPContextKey struct{}

func withHostHTTP(ctx context.Context, hc pluginapi.HostHTTPClient) context.Context {
	if hc == nil {
		return ctx
	}
	return context.WithValue(ctx, hostHTTPContextKey{}, hc)
}

func hostHTTPFromContext(ctx context.Context) pluginapi.HostHTTPClient {
	hc, _ := ctx.Value(hostHTTPContextKey{}).(pluginapi.HostHTTPClient)
	return hc
}

// hostHTTPBridge implements pluginapi.HostHTTPClient on top of the
// host.http.do callback, so upstream requests follow host transport policy
// (proxy-url) and request-log capture. ExecutorRequest.HTTPClient cannot be
// used for this: it is tagged json:"-" and never crosses the RPC boundary to
// the plugin, so the bridge keys off the executor's host callback ID instead.
type hostHTTPBridge struct {
	hostCallbackID string
}

// newHostHTTPBridge returns a bridge for the given host callback ID, or nil
// when no callback context is available (e.g. unit tests).
func newHostHTTPBridge(hostCallbackID string) pluginapi.HostHTTPClient {
	if strings.TrimSpace(hostCallbackID) == "" {
		return nil
	}
	return &hostHTTPBridge{hostCallbackID: hostCallbackID}
}

// hostHTTPDoWireRequest mirrors the host's rpcHostHTTPRequest JSON schema.
type hostHTTPDoWireRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method,omitempty"`
	URL            string      `json:"url,omitempty"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

func (b *hostHTTPBridge) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	raw, errCall := callHost(pluginabi.MethodHostHTTPDo, hostHTTPDoWireRequest{
		HostCallbackID: b.hostCallbackID,
		Method:         req.Method,
		URL:            req.URL,
		Headers:        req.Headers,
		Body:           req.Body,
	})
	if errCall != nil {
		return pluginapi.HTTPResponse{}, errCall
	}
	var resp pluginapi.HTTPResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return pluginapi.HTTPResponse{}, errDecode
	}
	return resp, nil
}

func (b *hostHTTPBridge) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("host http bridge: streaming not supported")
}

// post sends a JSON body to a Kimi tools endpoint with Bearer auth. Errors embed
// the HTTP status ("kimi http 429: ...") so hostHTTPStatusFromError picks them up.
func (c *kimiClient) post(ctx context.Context, path string, payload any) ([]byte, error) {
	if !c.available() {
		return nil, fmt.Errorf("kimi_api_keys is empty")
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, errMarshal
	}
	if hc := hostHTTPFromContext(ctx); hc != nil {
		return c.postViaHost(ctx, hc, path, raw)
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path), bytes.NewReader(raw))
	if errNew != nil {
		return nil, errNew
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.nextKey())
	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logrus.Errorf("kimi: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kimi http %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	return body, nil
}

// postViaHost sends the payload through the host-injected HTTPClient so the
// call honors host transport policy (proxy-url) and appears in request logs.
func (c *kimiClient) postViaHost(ctx context.Context, hc pluginapi.HostHTTPClient, path string, raw []byte) ([]byte, error) {
	resp, errDo := hc.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodPost,
		URL:    c.endpoint(path),
		Headers: http.Header{
			"Content-Type":  []string{"application/json"},
			"Authorization": []string{"Bearer " + c.nextKey()},
		},
		Body: raw,
	})
	if errDo != nil {
		return nil, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kimi http %d: %s", resp.StatusCode, truncate(string(resp.Body), 512))
	}
	return resp.Body, nil
}

type kimiTimeWindow struct {
	Start string `json:"start,omitempty" yaml:"start"`
	End   string `json:"end,omitempty" yaml:"end"`
}

type kimiSearchRequest struct {
	TextQuery      string          `json:"text_query"`
	Limit          int             `json:"limit,omitempty"`
	IncludeContent bool            `json:"include_content,omitempty"`
	Sites          []string        `json:"sites,omitempty"`
	TimeWindow     *kimiTimeWindow `json:"time_window,omitempty"`
}

type kimiSearchChunk struct {
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

type kimiSearchResult struct {
	Title   string            `json:"title"`
	URL     string            `json:"url"`
	Snippet string            `json:"snippet"`
	Text    string            `json:"text"`
	Date    string            `json:"date"`
	Chunks  []kimiSearchChunk `json:"chunks"`
}

type kimiSearchResponse struct {
	SearchResults []kimiSearchResult `json:"search_results"`
}

func (c *kimiClient) search(ctx context.Context, pro bool, req kimiSearchRequest) (*kimiSearchResponse, error) {
	path := kimiSearchBasicPath
	if pro {
		path = kimiSearchProPath
	} else {
		// sites and time_window are Pro-only parameters.
		req.Sites = nil
		req.TimeWindow = nil
	}
	if strings.TrimSpace(req.TextQuery) == "" {
		return nil, fmt.Errorf("web search query is empty")
	}
	body, errPost := c.post(ctx, path, req)
	if errPost != nil {
		return nil, errPost
	}
	var parsed kimiSearchResponse
	if errDecode := json.Unmarshal(body, &parsed); errDecode != nil {
		return nil, errDecode
	}
	return &parsed, nil
}

type kimiFetchRequest struct {
	URL string `json:"url"`
}

type kimiFetchResponse struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

func (c *kimiClient) fetch(ctx context.Context, rawURL string) (*kimiFetchResponse, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("kimi fetch url is empty")
	}
	body, errPost := c.post(ctx, kimiFetchPath, kimiFetchRequest{URL: rawURL})
	if errPost != nil {
		return nil, errPost
	}
	var parsed kimiFetchResponse
	if errDecode := json.Unmarshal(body, &parsed); errDecode != nil {
		return nil, errDecode
	}
	return &parsed, nil
}

type kimiResponsesTool struct {
	Type string `json:"type"`
}

type kimiResponsesRequest struct {
	Model   string              `json:"model"`
	Input   string              `json:"input"`
	Tools   []kimiResponsesTool `json:"tools"`
	Include []string            `json:"include,omitempty"`
}

type kimiResponsesSource struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type kimiResponsesAction struct {
	Type    string                `json:"type"`
	Query   string                `json:"query"`
	Sources []kimiResponsesSource `json:"sources"`
}

type kimiResponsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type kimiResponsesOutputItem struct {
	Type    string                 `json:"type"`
	Action  kimiResponsesAction    `json:"action"`
	Content []kimiResponsesContent `json:"content"`
}

type kimiResponsesResult struct {
	Output []kimiResponsesOutputItem `json:"output"`
}

// normalizeKimiUpstreamModel converts a configured or registry model name to
// the upstream model ID the Kimi Responses endpoint expects, mirroring the
// host executor (internal/runtime/executor/kimi_executor.go): CLIProxyAPI
// "kimi-" prefixes, "[1m]" context suffixes, and thinking suffixes such as
// "(1024)" are handled, and coding aliases remap to their official upstream
// IDs. Without this, a registry alias such as kimi-k3 reaches the upstream
// unchanged and can be rejected as an invalid model.
func normalizeKimiUpstreamModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	suffix := ""
	if i := strings.Index(m, "("); i >= 0 && strings.HasSuffix(m, ")") {
		suffix = m[i:]
		m = strings.TrimSpace(m[:i])
	}
	m = strings.TrimSuffix(m, "[1m]")
	var normalized string
	switch m {
	case "kimi-k2.8", "k2.8",
		"kimi-k2.8-code", "k2.8-code",
		"kimi-k2.8-preview", "k2.8-preview",
		"kimi-k2.7-code", "k2.7-code",
		"kimi-for-coding", "for-coding":
		normalized = "kimi-for-coding"
	case "kimi-k2.7-code-highspeed", "k2.7-code-highspeed",
		"kimi-for-coding-highspeed", "for-coding-highspeed":
		normalized = "kimi-for-coding-highspeed"
	default:
		normalized = strings.TrimPrefix(m, "kimi-")
	}
	return normalized + suffix
}

// searchModel runs kimi_search_level=model: a Responses API call where Kimi's
// server-side web_search tool searches and the model writes the answer.
func (c *kimiClient) searchModel(ctx context.Context, model, query string) ([]claudeWebSearchHit, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = kimiDefaultModel
	}
	model = normalizeKimiUpstreamModel(model)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, "", fmt.Errorf("web search query is empty")
	}
	body, errPost := c.post(ctx, kimiResponsesPath, kimiResponsesRequest{
		Model:   model,
		Input:   query,
		Tools:   []kimiResponsesTool{{Type: "web_search"}},
		Include: []string{"web_search_call.action.sources"},
	})
	if errPost != nil {
		return nil, "", errPost
	}
	var parsed kimiResponsesResult
	if errDecode := json.Unmarshal(body, &parsed); errDecode != nil {
		return nil, "", errDecode
	}
	var hits []claudeWebSearchHit
	var answerParts []string
	for _, item := range parsed.Output {
		switch item.Type {
		case "web_search_call":
			for _, src := range item.Action.Sources {
				hits = append(hits, claudeWebSearchHit{
					Title: strings.TrimSpace(src.Title),
					URL:   strings.TrimSpace(src.URL),
				})
			}
		case "message":
			for _, part := range item.Content {
				if t := strings.TrimSpace(part.Text); t != "" {
					answerParts = append(answerParts, t)
				}
			}
		}
	}
	return hits, strings.Join(answerParts, "\n\n"), nil
}

// executePlan runs the resolved search plan and maps results to Claude hits.
// The answer is non-empty only for the model level (Kimi writes it) — local
// levels rely on composeAnswerText over the hits.
func (c *kimiClient) executePlan(ctx context.Context, plan kimiSearchPlan, cfg pluginConfig, query string) ([]claudeWebSearchHit, string, error) {
	var hits []claudeWebSearchHit
	var answer string
	var err error
	switch plan.level {
	case kimiLevelFetch:
		var fr *kimiFetchResponse
		fr, err = c.fetch(ctx, plan.fetchURL)
		if err == nil {
			finalURL := strings.TrimSpace(fr.URL)
			if finalURL == "" {
				finalURL = plan.fetchURL
			}
			if !fetchURLPermittedBySites(finalURL, plan.sites) {
				// The upstream fetch landed on a host outside the allowed
				// domains (redirect or canonical URL); returning its body
				// would bypass the request's site restriction.
				return nil, "", fmt.Errorf("kimi fetch landed outside allowed domains: %s", finalURL)
			}
			snippet := truncate(fr.Markdown, kimiFetchSnippetCap)
			hits = []claudeWebSearchHit{{
				Title:   strings.TrimSpace(fr.Title),
				URL:     finalURL,
				Snippet: snippet,
			}}
		}
	case kimiLevelModel:
		hits, answer, err = c.searchModel(ctx, cfg.KimiModel, query)
	case kimiLevelPro:
		hits, err = c.runToolSearch(ctx, true, plan, cfg, query)
	default:
		hits, err = c.runToolSearch(ctx, false, plan, cfg, query)
		// Empty Basic results are free, so escalate to Pro at no extra cost.
		if err == nil && plan.escalate && len(hits) == 0 {
			hits, err = c.runToolSearch(ctx, true, plan, cfg, query)
		}
	}
	if err != nil {
		return nil, "", err
	}
	filtered := filterBlockedDomains(hits, plan.blocked)
	if plan.level == kimiLevelModel && answer != "" && len(filtered) != len(hits) {
		// The model wrote its answer from an unfiltered server-side search;
		// once a source is removed the answer may still derive from that
		// page, so drop it and let the caller compose from allowed hits.
		answer = ""
	}
	return filtered, answer, nil
}

func (c *kimiClient) runToolSearch(ctx context.Context, pro bool, plan kimiSearchPlan, cfg pluginConfig, query string) ([]claudeWebSearchHit, error) {
	req := kimiSearchRequest{
		TextQuery: query,
		Limit:     plan.limit,
	}
	if pro {
		req.Sites = plan.sites
		req.TimeWindow = cfg.KimiTimeWindow
	}
	resp, errSearch := c.search(ctx, pro, req)
	if errSearch != nil {
		return nil, errSearch
	}
	return kimiResultsToHits(resp.SearchResults), nil
}

// kimiResultsToHits prefers ranked Pro chunks, then snippet, then full text.
func kimiResultsToHits(results []kimiSearchResult) []claudeWebSearchHit {
	hits := make([]claudeWebSearchHit, 0, len(results))
	for _, r := range results {
		snippet := ""
		for _, chunk := range r.Chunks {
			if t := strings.TrimSpace(chunk.Text); t != "" {
				snippet = t
				break
			}
		}
		if snippet == "" {
			snippet = strings.TrimSpace(r.Snippet)
		}
		if snippet == "" {
			snippet = strings.TrimSpace(r.Text)
		}
		hits = append(hits, claudeWebSearchHit{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Snippet: snippet,
			PageAge: strings.TrimSpace(r.Date),
		})
	}
	return hits
}

func filterBlockedDomains(hits []claudeWebSearchHit, blocked []string) []claudeWebSearchHit {
	if len(blocked) == 0 || len(hits) == 0 {
		return hits
	}
	out := make([]claudeWebSearchHit, 0, len(hits))
	for _, hit := range hits {
		if domainMatches(hostFromURL(hit.URL), blocked) {
			continue
		}
		out = append(out, hit)
	}
	return out
}

func domainMatches(host string, domains []string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	for _, d := range domains {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func runKimiClaudeWithClient(ctx context.Context, req pluginapi.ExecutorRequest, client *kimiClient) ([]byte, http.Header, error) {
	cfg := loadedConfig()
	hits, answer, query, errSearch := runKimiSearch(ctx, req, client, cfg)
	if errSearch != nil {
		return nil, nil, errSearch
	}
	builder := newClaudeStreamBuilder(strings.TrimSpace(req.Model))
	payload := builder.buildMessageJSON(query, hits, answer)
	headers := http.Header{"Content-Type": []string{"application/json"}}
	return payload, headers, nil
}

func runKimiClaudeStreamWithClient(ctx context.Context, req pluginapi.ExecutorRequest, client *kimiClient) ([]byte, http.Header, error) {
	cfg := loadedConfig()
	hits, answer, query, errSearch := runKimiSearch(ctx, req, client, cfg)
	if errSearch != nil {
		return nil, nil, errSearch
	}
	builder := newClaudeStreamBuilder(strings.TrimSpace(req.Model))
	payload := builder.buildStreamWithQuery(query, hits, answer)
	headers := http.Header{"Content-Type": []string{"text/event-stream"}}
	return payload, headers, nil
}

func runKimiSearch(ctx context.Context, req pluginapi.ExecutorRequest, client *kimiClient, cfg pluginConfig) ([]claudeWebSearchHit, string, string, error) {
	query := extractClaudeWebSearchQuery(req.OriginalRequest)
	if query == "" {
		query = extractClaudeWebSearchQuery(req.Payload)
	}
	if strings.TrimSpace(query) == "" {
		return nil, "", "", fmt.Errorf("web search query is empty")
	}
	toolCfg := extractClaudeWebSearchToolConfig(claudeRequestBody(req))
	plan := resolveKimiSearchPlan(cfg, toolCfg, query)
	ctx = withHostHTTP(ctx, req.HTTPClient)
	hits, answer, errSearch := client.executePlan(ctx, plan, cfg, query)
	if errSearch != nil {
		return nil, "", "", errSearch
	}
	return hits, answer, query, nil
}
