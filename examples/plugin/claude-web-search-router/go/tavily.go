package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

const tavilySearchURL = "https://api.tavily.com/search"

type tavilyClient struct {
	keys    []string
	idx     atomic.Uint64
	http    *http.Client
	baseURL string // empty → https://api.tavily.com/search
}

func newTavilyClient(keys []string) *tavilyClient {
	return newTavilyClientWithOptions(keys, nil, "")
}

func newTavilyClientWithOptions(keys []string, httpClient *http.Client, baseURL string) *tavilyClient {
	trimmed := make([]string, 0, len(keys))
	for _, key := range keys {
		if k := strings.TrimSpace(key); k != "" {
			trimmed = append(trimmed, k)
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &tavilyClient{
		keys:    trimmed,
		http:    httpClient,
		baseURL: strings.TrimSpace(baseURL),
	}
}

func (c *tavilyClient) searchEndpoint() string {
	if c != nil && c.baseURL != "" {
		return c.baseURL
	}
	return tavilySearchURL
}

func (c *tavilyClient) available() bool {
	return c != nil && len(c.keys) > 0
}

func (c *tavilyClient) nextKey() string {
	if len(c.keys) == 0 {
		return ""
	}
	n := c.idx.Add(1)
	return c.keys[int(n-1)%len(c.keys)]
}

type tavilySearchRequest struct {
	APIKey        string `json:"api_key"`
	Query         string `json:"query"`
	SearchDepth   string `json:"search_depth,omitempty"`
	MaxResults    int    `json:"max_results,omitempty"`
	IncludeAnswer bool   `json:"include_answer,omitempty"`
}

type tavilySearchResponse struct {
	Answer  string `json:"answer"`
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

type claudeWebSearchHit struct {
	Title   string
	URL     string
	Snippet string
}

func (c *tavilyClient) search(ctx context.Context, query string, maxResults int) ([]claudeWebSearchHit, string, error) {
	if !c.available() {
		return nil, "", fmt.Errorf("tavily_api_keys is empty")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, "", fmt.Errorf("web search query is empty")
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	payload, errMarshal := json.Marshal(tavilySearchRequest{
		APIKey:        c.nextKey(),
		Query:         query,
		SearchDepth:   "basic",
		MaxResults:    maxResults,
		IncludeAnswer: true,
	})
	if errMarshal != nil {
		return nil, "", errMarshal
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, c.searchEndpoint(), bytes.NewReader(payload))
	if errNew != nil {
		return nil, "", errNew
	}
	req.Header.Set("Content-Type", "application/json")
	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return nil, "", errDo
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, "", errRead
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("tavily http %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	var parsed tavilySearchResponse
	if errDecode := json.Unmarshal(body, &parsed); errDecode != nil {
		return nil, "", errDecode
	}
	hits := make([]claudeWebSearchHit, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		hits = append(hits, claudeWebSearchHit{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Snippet: strings.TrimSpace(r.Content),
		})
	}
	return hits, strings.TrimSpace(parsed.Answer), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
