package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func kimiTestBody() []byte {
	return []byte(`{
		"model": "claude-sonnet-4-6",
		"tools": [{"type": "web_search_20250305", "name": "web_search", "max_uses": 5}],
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Perform a web search for the query: kimi test query"}]}]
	}`)
}

func TestKimiClientSearchBasicMockAPI(t *testing.T) {
	var gotBody kimiSearchRequest
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kimiSearchBasicPath {
			t.Errorf("path = %s, want %s", r.URL.Path, kimiSearchBasicPath)
		}
		gotAuth = r.Header.Get("Authorization")
		raw, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errDecode := json.Unmarshal(raw, &gotBody); errDecode != nil {
			t.Fatal(errDecode)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"search_results": [
				{"title": "T1", "url": "https://a.example", "snippet": "s1", "date": "2026-06-01"},
				{"title": "T2", "url": "https://b.example", "text": "full body"}
			]
		}`))
	}))
	defer server.Close()

	client := newKimiClientWithOptions([]string{"sk-test"}, server.URL, server.Client())
	resp, errSearch := client.search(context.Background(), false, kimiSearchRequest{
		TextQuery: "q",
		Limit:     7,
	})
	if errSearch != nil {
		t.Fatalf("search() error = %v", errSearch)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody.TextQuery != "q" || gotBody.Limit != 7 {
		t.Fatalf("request = %+v", gotBody)
	}
	hits := kimiResultsToHits(resp.SearchResults)
	if len(hits) != 2 {
		t.Fatalf("hits = %#v", hits)
	}
	if hits[0].Snippet != "s1" || hits[0].PageAge != "2026-06-01" {
		t.Fatalf("hit[0] = %#v", hits[0])
	}
	if hits[1].Snippet != "full body" {
		t.Fatalf("hit[1] snippet = %q, want text fallback", hits[1].Snippet)
	}
}

func TestKimiClientSearchBasicStripsProParams(t *testing.T) {
	var gotBody kimiSearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"search_results": []}`))
	}))
	defer server.Close()

	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	_, err := client.search(context.Background(), false, kimiSearchRequest{
		TextQuery: "q",
		Sites:     []string{"a.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotBody.Sites) != 0 || gotBody.TimeWindow != nil {
		t.Fatalf("basic request leaked pro params: %+v", gotBody)
	}
}

func TestKimiClientSearchProMockAPI(t *testing.T) {
	var gotBody kimiSearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kimiSearchProPath {
			t.Errorf("path = %s, want %s", r.URL.Path, kimiSearchProPath)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{
			"search_results": [
				{"title": "P", "url": "https://p.example", "snippet": "weak",
				 "chunks": [{"text": "ranked chunk one", "score": 1.5}, {"text": "chunk two", "score": 0.9}]}
			]
		}`))
	}))
	defer server.Close()

	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	resp, err := client.search(context.Background(), true, kimiSearchRequest{
		TextQuery:  "q",
		Sites:      []string{"a.example"},
		TimeWindow: &kimiTimeWindow{Start: "2026-01", End: "2026-09"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotBody.Sites) != 1 || gotBody.Sites[0] != "a.example" {
		t.Fatalf("sites = %v", gotBody.Sites)
	}
	if gotBody.TimeWindow == nil || gotBody.TimeWindow.Start != "2026-01" {
		t.Fatalf("time_window = %+v", gotBody.TimeWindow)
	}
	hits := kimiResultsToHits(resp.SearchResults)
	if len(hits) != 1 || hits[0].Snippet != "ranked chunk one" {
		t.Fatalf("hits = %#v, want top chunk as snippet", hits)
	}
}

func TestKimiClientFetchMockAPI(t *testing.T) {
	var gotBody kimiFetchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kimiFetchPath {
			t.Errorf("path = %s, want %s", r.URL.Path, kimiFetchPath)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"url": "https://x.example/page", "title": "Page", "markdown": "# Body"}`))
	}))
	defer server.Close()

	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	resp, err := client.fetch(context.Background(), "https://x.example/page")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.URL != "https://x.example/page" {
		t.Fatalf("url = %q", gotBody.URL)
	}
	if resp.Title != "Page" || resp.Markdown != "# Body" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestKimiClientSearchModelMockAPI(t *testing.T) {
	var gotBody kimiResponsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kimiResponsesPath {
			t.Errorf("path = %s, want %s", r.URL.Path, kimiResponsesPath)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{
			"output": [
				{"type": "web_search_call", "id": "ws_1", "status": "completed",
				 "action": {"type": "search", "query": "kimi k3", "sources": [
					{"type": "url", "url": "https://s1.example", "title": "S1"},
					{"type": "url", "url": "https://s2.example", "title": "S2"}
				 ]}},
				{"type": "message", "content": [{"type": "output_text", "text": "Kimi K3 is the latest model."}]}
			]
		}`))
	}))
	defer server.Close()

	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	hits, answer, err := client.searchModel(context.Background(), "", "tell me about kimi k3")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.Model != normalizeKimiUpstreamModel(kimiDefaultModel) {
		t.Fatalf("model = %q, want upstream form %q", gotBody.Model, normalizeKimiUpstreamModel(kimiDefaultModel))
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Type != "web_search" {
		t.Fatalf("tools = %+v", gotBody.Tools)
	}
	if len(hits) != 2 || hits[0].URL != "https://s1.example" {
		t.Fatalf("hits = %#v", hits)
	}
	if answer != "Kimi K3 is the latest model." {
		t.Fatalf("answer = %q", answer)
	}
}

func TestKimiClientEmptyKeys(t *testing.T) {
	client := newKimiClient(nil, "")
	_, err := client.search(context.Background(), false, kimiSearchRequest{TextQuery: "q"})
	if err == nil || !strings.Contains(err.Error(), "kimi_api_keys") {
		t.Fatalf("err = %v", err)
	}
}

func TestKimiClientHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": {"type": "rate_limited"}}`))
	}))
	defer server.Close()
	client := newKimiClientWithOptions([]string{"bad"}, server.URL, server.Client())
	_, err := client.search(context.Background(), false, kimiSearchRequest{TextQuery: "q"})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want status embedded", err)
	}
	if !isRetryableHTTPStatus(hostHTTPStatusFromError(err)) {
		t.Fatalf("err = %v, want retryable status", err)
	}
}

func TestKimiClientRoundRobinKeys(t *testing.T) {
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"search_results": []}`))
	}))
	defer server.Close()
	client := newKimiClientWithOptions([]string{"k1", "k2"}, server.URL, server.Client())
	for i := 0; i < 4; i++ {
		if _, err := client.search(context.Background(), false, kimiSearchRequest{TextQuery: "q"}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Bearer k1", "Bearer k2", "Bearer k1", "Bearer k2"}
	if len(auths) != 4 {
		t.Fatalf("auths = %v", auths)
	}
	for i := range want {
		if auths[i] != want[i] {
			t.Fatalf("auths = %v, want %v", auths, want)
		}
	}
}

func TestKimiExecutePlanEscalatesEmptyBasicToPro(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == kimiSearchBasicPath {
			_, _ = w.Write([]byte(`{"search_results": []}`))
			return
		}
		_, _ = w.Write([]byte(`{"search_results": [{"title": "P", "url": "https://p.example", "snippet": "pro hit"}]}`))
	}))
	defer server.Close()

	currentConfig.Store(pluginConfig{
		KimiSearchLevel:   string(kimiLevelAuto),
		KimiProEscalation: true,
	})
	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	plan := resolveKimiSearchPlan(loadedConfig(), claudeWebSearchToolConfig{}, "a simple question")
	hits, _, err := client.executePlan(context.Background(), plan, loadedConfig(), "a simple question")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != kimiSearchBasicPath || paths[1] != kimiSearchProPath {
		t.Fatalf("paths = %v, want basic then pro", paths)
	}
	if len(hits) != 1 || hits[0].Snippet != "pro hit" {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestKimiExecutePlanNoEscalationWhenDisabled(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"search_results": []}`))
	}))
	defer server.Close()

	currentConfig.Store(pluginConfig{
		KimiSearchLevel:   string(kimiLevelAuto),
		KimiProEscalation: false,
	})
	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	plan := resolveKimiSearchPlan(loadedConfig(), claudeWebSearchToolConfig{}, "a simple question")
	hits, _, err := client.executePlan(context.Background(), plan, loadedConfig(), "a simple question")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(hits) != 0 {
		t.Fatalf("calls = %d hits = %v, want single basic call", calls, hits)
	}
}

func TestRunKimiClaudeJSONWithMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"search_results": [{"title": "K", "url": "https://k.example", "snippet": "kimi snippet", "date": "2026-10-01"}]}`))
	}))
	defer server.Close()

	currentConfig.Store(pluginConfig{
		Enabled:         true,
		Route:           string(backendKimiWebSearch),
		KimiSearchLevel: string(kimiLevelBasic),
	})
	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	payload, _, err := runKimiClaudeWithClient(context.Background(), pluginapi.ExecutorRequest{
		Model:           "claude-sonnet-4-6",
		OriginalRequest: kimiTestBody(),
	}, client)
	if err != nil {
		t.Fatal(err)
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
	if root.Get("content.1.content.0.url").String() != "https://k.example" {
		t.Fatalf("result url = %s", root.Get("content.1.content.0.url").String())
	}
	if root.Get("content.1.content.0.page_age").String() != "2026-10-01" {
		t.Fatalf("page_age = %s", root.Get("content.1.content.0.page_age").String())
	}
	if root.Get("usage.server_tool_use.web_search_requests").Int() != 1 {
		t.Fatalf("web_search_requests = %d", root.Get("usage.server_tool_use.web_search_requests").Int())
	}
}

func TestRunKimiClaudeStreamWithMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"search_results": [{"title": "K", "url": "https://k.example", "snippet": "kimi snippet"}]}`))
	}))
	defer server.Close()

	currentConfig.Store(pluginConfig{
		Enabled:         true,
		Route:           string(backendKimiWebSearch),
		KimiSearchLevel: string(kimiLevelBasic),
	})
	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	payload, headers, err := runKimiClaudeStreamWithClient(context.Background(), pluginapi.ExecutorRequest{
		Model:           "claude-sonnet-4-6",
		Stream:          true,
		OriginalRequest: kimiTestBody(),
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", headers.Get("Content-Type"))
	}
	text := string(payload)
	for _, needle := range []string{
		"event: message_start",
		`"type":"server_tool_use"`,
		`"type":"web_search_tool_result"`,
		`https://k.example`,
		`"web_search_requests":1`,
		"event: message_stop",
		"kimi test query",
		"kimi snippet",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("SSE missing %q in:\n%s", needle, text)
		}
	}
}

func TestRunKimiClaudeModelLevelWithMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kimiResponsesPath {
			t.Errorf("path = %s, want %s", r.URL.Path, kimiResponsesPath)
		}
		_, _ = w.Write([]byte(`{
			"output": [
				{"type": "web_search_call", "action": {"type": "search", "query": "kimi test query",
					"sources": [{"type": "url", "url": "https://src.example", "title": "SRC"}]}},
				{"type": "message", "content": [{"type": "output_text", "text": "model-written answer"}]}
			]
		}`))
	}))
	defer server.Close()

	currentConfig.Store(pluginConfig{
		Enabled:         true,
		Route:           string(backendKimiWebSearch),
		KimiSearchLevel: string(kimiLevelModel),
	})
	client := newKimiClientWithOptions([]string{"k"}, server.URL, server.Client())
	payload, _, err := runKimiClaudeWithClient(context.Background(), pluginapi.ExecutorRequest{
		Model:           "claude-sonnet-4-6",
		OriginalRequest: kimiTestBody(),
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	root := gjson.ParseBytes(payload)
	if root.Get("content.2.text").String() != "model-written answer" {
		t.Fatalf("text = %s", root.Get("content.2.text").String())
	}
	if root.Get("content.1.content.0.url").String() != "https://src.example" {
		t.Fatalf("source url = %s", root.Get("content.1.content.0.url").String())
	}
}
