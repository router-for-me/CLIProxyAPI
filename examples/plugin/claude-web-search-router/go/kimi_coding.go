package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	kimiCodingDefaultBaseURL = "https://api.kimi.ai/coding"
	kimiCodingDefaultModel   = "kimi-for-coding"
)

// kimiCodingClient calls the Kimi for Coding Responses endpoint directly with a
// coding-plan API key. It covers deployments that store kimi access as an
// OpenAI-compat key group rather than a kimi OAuth auth (kimi_responses) or a
// Moonshot platform key (kimi_web_search). The coding endpoint runs web_search
// server-side, so the model writes the answer itself.
type kimiCodingClient struct {
	keys    []string
	baseURL string
	model   string
	idx     atomic.Int64
	http    *http.Client
}

func newKimiCodingClient(cfg pluginConfig) *kimiCodingClient {
	return newKimiCodingClientWithOptions(cfg.KimiCodingAPIKeys, cfg.KimiCodingBaseURL, cfg.KimiCodingModel, nil)
}

func newKimiCodingClientWithOptions(keys []string, baseURL, model string, httpClient *http.Client) *kimiCodingClient {
	trimmed := make([]string, 0, len(keys))
	for _, key := range keys {
		if k := strings.TrimSpace(key); k != "" {
			trimmed = append(trimmed, k)
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &kimiCodingClient{
		keys:    trimmed,
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		model:   strings.TrimSpace(model),
		http:    httpClient,
	}
}

// sharedKimiCodingClient returns the process-wide coding client for the given
// config, creating it on first use and refreshing it when the config changes,
// so the round-robin key counter advances across requests (see sharedKimiClient).
var (
	sharedKimiCodingMu  sync.Mutex
	sharedKimiCodingPtr *kimiCodingClient
	sharedKimiCodingSig string
)

func sharedKimiCodingClient(cfg pluginConfig) *kimiCodingClient {
	sig := strings.Join(cfg.KimiCodingAPIKeys, "\x00") + "\x01" + cfg.KimiCodingBaseURL + "\x01" + cfg.KimiCodingModel
	sharedKimiCodingMu.Lock()
	defer sharedKimiCodingMu.Unlock()
	if sharedKimiCodingPtr != nil && sharedKimiCodingSig == sig {
		return sharedKimiCodingPtr
	}
	client := newKimiCodingClient(cfg)
	sharedKimiCodingPtr = client
	sharedKimiCodingSig = sig
	return client
}

func (c *kimiCodingClient) available() bool {
	return c != nil && len(c.keys) > 0
}

func (c *kimiCodingClient) endpoint() string {
	base := c.baseURL
	if base == "" {
		base = kimiCodingDefaultBaseURL
	}
	// Accept bases with or without a trailing /v1 so a coding endpoint copied
	// from an openai-compat group (e.g. .../coding/v1) resolves identically.
	return strings.TrimSuffix(base, "/v1") + "/v1/responses"
}

func (c *kimiCodingClient) resolveModel() string {
	if c.model != "" {
		return c.model
	}
	return kimiCodingDefaultModel
}

func (c *kimiCodingClient) nextKey() string {
	if len(c.keys) == 0 {
		return ""
	}
	n := c.idx.Add(1)
	return c.keys[int(n-1)%len(c.keys)]
}

// search posts the converted Claude body to {base}/v1/responses with a
// server-side web_search tool and parses the answer text plus source hits.
func (c *kimiCodingClient) search(ctx context.Context, claudeBody []byte) (string, []claudeWebSearchHit, error) {
	if !c.available() {
		return "", nil, fmt.Errorf("kimi_coding_api_keys is empty")
	}
	reqBody, errBody := kimiResponsesBody(claudeBody, c.resolveModel(), false)
	if errBody != nil {
		return "", nil, errBody
	}
	if hc := hostHTTPFromContext(ctx); hc != nil {
		return c.searchViaHost(ctx, hc, reqBody)
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), strings.NewReader(string(reqBody)))
	if errNew != nil {
		return "", nil, errNew
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.nextKey())
	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return "", nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logrus.Errorf("kimi coding: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", nil, errRead
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("kimi coding http %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	return parseCodingResponses(body)
}

// searchViaHost sends the Responses payload through the host-injected
// HTTPClient so the call honors host transport policy (proxy-url) and appears
// in request logs.
func (c *kimiCodingClient) searchViaHost(ctx context.Context, hc pluginapi.HostHTTPClient, reqBody []byte) (string, []claudeWebSearchHit, error) {
	resp, errDo := hc.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodPost,
		URL:    c.endpoint(),
		Headers: http.Header{
			"Content-Type":  []string{"application/json"},
			"Authorization": []string{"Bearer " + c.nextKey()},
		},
		Body: reqBody,
	})
	if errDo != nil {
		return "", nil, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("kimi coding http %d: %s", resp.StatusCode, truncate(string(resp.Body), 512))
	}
	return parseCodingResponses(resp.Body)
}

func parseCodingResponses(body []byte) (string, []claudeWebSearchHit, error) {
	if !gjson.GetBytes(body, "output").IsArray() {
		return "", nil, fmt.Errorf("kimi coding: unexpected responses body")
	}
	var answer string
	var hits []claudeWebSearchHit
	for _, item := range gjson.GetBytes(body, "output").Array() {
		switch item.Get("type").String() {
		case "message":
			for _, c := range item.Get("content").Array() {
				if c.Get("type").String() == "output_text" {
					if text := strings.TrimSpace(c.Get("text").String()); text != "" {
						answer = strings.TrimSpace(answer + "\n" + text)
					}
				}
			}
		case "web_search_call":
			for _, src := range item.Get("action.sources").Array() {
				hits = append(hits, claudeWebSearchHit{
					Title: strings.TrimSpace(src.Get("title").String()),
					URL:   strings.TrimSpace(src.Get("url").String()),
				})
			}
		}
	}
	if answer == "" && len(hits) == 0 {
		return "", nil, fmt.Errorf("kimi coding: empty responses output")
	}
	return answer, hits, nil
}

func runKimiCodingClaude(ctx context.Context, req pluginapi.ExecutorRequest, cfg pluginConfig) ([]byte, http.Header, error) {
	answer, hits, errSearch := sharedKimiCodingClient(cfg).search(withHostHTTP(ctx, req.HTTPClient), claudeRequestBody(req))
	if errSearch != nil {
		return nil, nil, errSearch
	}
	query := extractClaudeWebSearchQuery(req.OriginalRequest)
	if query == "" {
		query = extractClaudeWebSearchQuery(req.Payload)
	}
	payload := newClaudeStreamBuilder(req.Model).buildMessageJSON(query, hits, answer)
	return payload, http.Header{"Content-Type": []string{"application/json"}}, nil
}

// runKimiCodingClaudeStream buffers a complete Claude SSE stream. The upstream
// call is non-streaming, so the stream is emitted once the search completes.
func runKimiCodingClaudeStream(ctx context.Context, req pluginapi.ExecutorRequest, cfg pluginConfig) ([]byte, http.Header, error) {
	answer, hits, errSearch := sharedKimiCodingClient(cfg).search(withHostHTTP(ctx, req.HTTPClient), claudeRequestBody(req))
	if errSearch != nil {
		return nil, nil, errSearch
	}
	query := extractClaudeWebSearchQuery(req.OriginalRequest)
	if query == "" {
		query = extractClaudeWebSearchQuery(req.Payload)
	}
	payload := newClaudeStreamBuilder(req.Model).buildStreamWithQuery(query, hits, answer)
	return payload, http.Header{"Content-Type": []string{"text/event-stream"}}, nil
}
