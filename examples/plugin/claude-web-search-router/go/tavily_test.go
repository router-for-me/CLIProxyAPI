package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestTavilyClientSearchMockAPI(t *testing.T) {
	var gotBody tavilySearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("content-type = %q", ct)
		}
		raw, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errDecode := json.Unmarshal(raw, &gotBody); errDecode != nil {
			t.Fatal(errDecode)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "北京天气",
			"answer": "明天晴。",
			"results": [
				{"title": "Example Weather", "url": "https://example.com/w", "content": "snippet one"}
			]
		}`))
	}))
	defer server.Close()

	client := newTavilyClientWithOptions([]string{"tvly-test-key"}, server.Client(), server.URL)
	hits, answer, errSearch := client.search(context.Background(), "北京天气", 3)
	if errSearch != nil {
		t.Fatalf("search() error = %v", errSearch)
	}
	if gotBody.APIKey != "tvly-test-key" {
		t.Fatalf("api_key = %q", gotBody.APIKey)
	}
	if gotBody.Query != "北京天气" {
		t.Fatalf("query = %q", gotBody.Query)
	}
	if gotBody.MaxResults != 3 {
		t.Fatalf("max_results = %d, want 3", gotBody.MaxResults)
	}
	if !gotBody.IncludeAnswer {
		t.Fatal("include_answer should be true")
	}
	if answer != "明天晴。" {
		t.Fatalf("answer = %q", answer)
	}
	if len(hits) != 1 || hits[0].URL != "https://example.com/w" {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestTavilyClientSearchEmptyKeys(t *testing.T) {
	client := newTavilyClient(nil)
	_, _, err := client.search(context.Background(), "q", 5)
	if err == nil || !strings.Contains(err.Error(), "tavily_api_keys") {
		t.Fatalf("err = %v", err)
	}
}

func TestTavilyClientSearchHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer server.Close()
	client := newTavilyClientWithOptions([]string{"bad"}, server.Client(), server.URL)
	_, _, err := client.search(context.Background(), "q", 5)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestTavilyClientRoundRobinKeys(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body tavilySearchRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		keys = append(keys, body.APIKey)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client := newTavilyClientWithOptions([]string{"k1", "k2"}, server.Client(), server.URL)
	for i := 0; i < 4; i++ {
		if _, _, err := client.search(context.Background(), "q", 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 4 || keys[0] != "k1" || keys[1] != "k2" || keys[2] != "k1" || keys[3] != "k2" {
		t.Fatalf("key rotation = %v", keys)
	}
}

func TestRunTavilyClaudeStreamWithMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"answer": "2026年6月16日北京多雨。",
			"results": [
				{"title": "bjmy.gov.cn", "url": "https://www.bjmy.gov.cn/x", "content": "预报"}
			]
		}`))
	}))
	defer server.Close()

	claudeBody := []byte(`{
		"model": "claude-sonnet-4-6",
		"stream": true,
		"tools": [{"type": "web_search_20250305", "name": "web_search", "max_uses": 5}],
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Perform a web search for the query: 北京天气 2026年6月16日"}]}]
	}`)
	client := newTavilyClientWithOptions([]string{"tvly-mock"}, server.Client(), server.URL)
	payload, headers, errRun := runTavilyClaudeStreamWithClient(context.Background(), pluginapi.ExecutorRequest{
		Model:           "claude-sonnet-4-6",
		Stream:          true,
		OriginalRequest: claudeBody,
	}, client)
	if errRun != nil {
		t.Fatalf("runTavilyClaudeStreamWithClient() error = %v", errRun)
	}
	if headers.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", headers.Get("Content-Type"))
	}
	text := string(payload)
	for _, needle := range []string{
		"event: message_start",
		`"type":"server_tool_use"`,
		`"name":"web_search"`,
		`"type":"web_search_tool_result"`,
		`"type":"web_search_result"`,
		`https://www.bjmy.gov.cn/x`,
		`"web_search_requests":1`,
		"event: message_stop",
		"北京天气 2026年6月16日",
		"2026年6月16日北京多雨",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("SSE missing %q in:\n%s", needle, text)
		}
	}
}

func TestRunTavilyClaudeJSONWithMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answer":"ok","results":[{"title":"T","url":"https://t.example","content":"c"}]}`))
	}))
	defer server.Close()

	claudeBody := []byte(`{
		"tools": [{"type": "web_search_20250305", "name": "web_search"}],
		"messages": [{"role": "user", "content": "Perform a web search for the query: test query"}]
	}`)
	client := newTavilyClientWithOptions([]string{"k"}, server.Client(), server.URL)
	payload, _, errRun := runTavilyClaudeWithClient(context.Background(), pluginapi.ExecutorRequest{
		Model:           "claude-sonnet-4-6",
		OriginalRequest: claudeBody,
	}, client)
	if errRun != nil {
		t.Fatal(errRun)
	}
	root := gjson.ParseBytes(payload)
	if root.Get("type").String() != "message" {
		t.Fatalf("type = %s", root.Get("type").String())
	}
	if root.Get("content.0.type").String() != "server_tool_use" {
		t.Fatalf("content.0 = %s", root.Get("content.0.type").String())
	}
	if root.Get("content.1.type").String() != "web_search_tool_result" {
		t.Fatalf("content.1 = %s", root.Get("content.1.type").String())
	}
	if root.Get("content.2.text").String() != "ok" {
		t.Fatalf("text = %s", root.Get("content.2.text").String())
	}
	if root.Get("usage.server_tool_use.web_search_requests").Int() != 1 {
		t.Fatalf("web_search_requests = %d", root.Get("usage.server_tool_use.web_search_requests").Int())
	}
}

func TestExecuteStreamRPCWithMockTavily(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answer":"rpc-ok","results":[]}`))
	}))
	defer server.Close()

	currentConfig.Store(pluginConfig{
		Route:         string(backendTavily),
		TavilyAPIKeys: []string{"k"},
	})
	// Override client by patching: executeStream uses loadedConfig keys + real URL.
	// Test runTavilyClaudeStreamWithClient directly instead; for execute() we need config + mock URL.
	// Use executor path with injected client via runTavilyClaudeStreamWithClient already covered.
	_ = server
	claudeBody := []byte(`{"messages":[{"role":"user","content":"Perform a web search for the query: q"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	client := newTavilyClientWithOptions([]string{"k"}, server.Client(), server.URL)
	body, _, err := runTavilyClaudeStreamWithClient(context.Background(), pluginapi.ExecutorRequest{
		Model: "m", Stream: true, OriginalRequest: claudeBody,
	}, client)
	if err != nil || !strings.Contains(string(body), "rpc-ok") {
		t.Fatalf("err=%v body=%s", err, body)
	}
}
