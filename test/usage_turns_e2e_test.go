package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
)

// usageE2EMetrics is the part of a /stats group the turns test checks.
type usageE2EMetrics struct {
	Requests           int64 `json:"requests"`
	Failed             int64 `json:"failed"`
	Turns              int64 `json:"turns"`
	Sessions           int64 `json:"sessions"`
	CacheWriteTokens   int64 `json:"cache_write_tokens"`
	CacheWrite5mTokens int64 `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`
}

type usageE2EGroup struct {
	Dimensions map[string]string `json:"dimensions"`
	usageE2EMetrics
}

// metricsOfReplies is what /stats should report for replies of the fake,
// sessions aside: a turn is a complete reply that ended in end_turn, max_tokens,
// or refusal. A truncated reply is a failed attempt even though it carried a
// stop reason.
func metricsOfReplies(replies []fakeReply) usageE2EMetrics {
	metrics := usageE2EMetrics{Requests: int64(len(replies))}
	for _, reply := range replies {
		if reply.truncated {
			metrics.Failed++
		}
		switch reply.stop {
		case "end_turn", "max_tokens", "refusal":
			if !reply.truncated {
				metrics.Turns++
			}
		}
		metrics.CacheWrite5mTokens += reply.cacheWrite5m
		metrics.CacheWrite1hTokens += reply.cacheWrite1h
		metrics.CacheWriteTokens += reply.cacheWrite5m + reply.cacheWrite1h + reply.cacheWriteNoLifetime
	}
	return metrics
}

func (s *usageE2EServer) stats(t *testing.T, query url.Values) []usageE2EGroup {
	t.Helper()
	_, body := s.management(t, "/stats", query, true)
	var stats struct {
		Groups []usageE2EGroup `json:"groups"`
	}
	if errDecode := json.Unmarshal(body, &stats); errDecode != nil {
		t.Fatalf("decode stats: %v", errDecode)
	}
	return stats.Groups
}

// sendWebsocket sends prompts as consecutive response.create requests on one
// client websocket, as Codex does within a session, and reads each reply to
// its terminal event.
func (s *usageE2EServer) sendWebsocket(t *testing.T, model string, headers http.Header, prompts ...string) {
	t.Helper()
	headers = headers.Clone()
	headers.Set("Authorization", "Bearer "+usageE2EClientKey)
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.baseURL, "http")+"/v1/responses", headers)
	if errDial != nil {
		t.Errorf("dial responses websocket: %v", errDial)
		return
	}
	defer func() { _ = conn.Close() }()
	for _, prompt := range prompts {
		request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]}`, model, prompt)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
			t.Errorf("send %q: %v", prompt, errWrite)
			return
		}
	reply:
		for {
			_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, message, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read reply to %q: %v", prompt, errRead)
				return
			}
			switch ojson.GetBytes(message, "type").String() {
			case "response.completed", "response.incomplete", "response.done":
				break reply
			case "error", "response.failed":
				t.Errorf("reply to %q: %s", prompt, message)
				return
			}
		}
	}
}

func TestUsageTurnsEndToEnd(t *testing.T) {
	if os.Getenv(usageE2EEnv) != "1" {
		t.Skipf("set %s=1 to build the server binary and run it against a fake upstream", usageE2EEnv)
	}
	start := time.Now()
	fake := newFakeQuotaUpstream(start)
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	proxy := startUsageE2EServer(t, upstream.URL)

	claudeSession := func(sessionID, agentID string) http.Header {
		headers := http.Header{}
		headers.Set("X-Claude-Code-Session-Id", sessionID)
		if agentID != "" {
			headers.Set("X-Claude-Code-Agent-Id", agentID)
		}
		return headers
	}
	codexSession := func(sessionID string) http.Header {
		headers := http.Header{}
		headers.Set("Session_id", sessionID)
		return headers
	}
	const (
		mainSession    = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"
		otherSession   = "7a2b3c4d-5e6f-4a70-8b9c-0d1e2f3a4b5c"
		codexWork      = "8b3c4d5e-6f70-4b81-9cad-1e2f3a4b5c6d"
		codexOther     = "9c4d5e6f-7081-4c92-adbe-2f3a4b5c6d7e"
		codexWebsocket = "ad5e6f70-8192-4da3-becf-3a4b5c6d7e8f"
	)
	plan := []struct {
		provider string
		stream   bool
		headers  http.Header
		reply    string
	}{
		{"claude", true, claudeSession(mainSession, ""), "stop=tool_use cw5m=1000"},
		{"claude", false, claudeSession(mainSession, ""), "stop=tool_use cw1h=2000"},
		{"claude", true, claudeSession(mainSession, "explore-1"), "stop=tool_use cw5m=50"},
		{"claude", false, claudeSession(mainSession, "explore-1"), "stop=end_turn"},
		{"claude", true, claudeSession(mainSession, ""), "stop=end_turn cw5m=300 cw1h=400"},
		{"claude", false, claudeSession(otherSession, ""), "stop=max_tokens"},
		{"claude", true, claudeSession(otherSession, ""), "stop=end_turn truncate-once"},
		{"codex", true, codexSession(codexWork), "stop=tool_use cw=500"},
		{"codex", false, codexSession(codexWork), "stop=tool_use"},
		{"codex", true, codexSession(codexWork), "stop=end_turn cw=700"},
		{"codex", false, codexSession(codexOther), "stop=max_tokens"},
	}
	models := map[string]string{"claude": "claude-opus-5-5", "codex": "gpt-5.5"}
	sessions := map[string]map[string]bool{"claude": {}, "codex": {}}
	for _, request := range plan {
		proxy.sendWithHeaders(t, request.provider, models[request.provider], request.stream, "in=1000 out=200 "+request.reply, request.headers)
		sessions[request.provider][request.headers.Get("X-Claude-Code-Session-Id")+request.headers.Get("X-Claude-Code-Agent-Id")+request.headers.Get("Session_id")] = true
	}
	websocketPrompts := []string{"in=1000 out=200 stop=tool_use cw=300", "in=1000 out=200 stop=end_turn", "in=1000 out=200 stop=max_tokens"}
	proxy.sendWebsocket(t, models["codex"], codexSession(codexWebsocket), websocketPrompts...)
	sessions["codex"][codexWebsocket] = true
	sentRequests := len(plan) + len(websocketPrompts)
	if t.Failed() {
		t.FailNow()
	}

	rangeQuery := func(pairs ...string) url.Values {
		query := url.Values{
			"from": {strconv.FormatInt(start.Add(-time.Minute).UnixMilli(), 10)},
			"to":   {strconv.FormatInt(time.Now().Add(time.Minute).UnixMilli(), 10)},
		}
		for i := 0; i+1 < len(pairs); i += 2 {
			query.Set(pairs[i], pairs[i+1])
		}
		return query
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		groups := proxy.stats(t, rangeQuery())
		if len(groups) == 1 && groups[0].Requests >= int64(sentRequests) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats = %+v, want %d requests recorded", groups, sentRequests)
		}
		time.Sleep(100 * time.Millisecond)
	}

	fake.mu.Lock()
	replies := fake.replies
	fake.mu.Unlock()
	truth := map[string]usageE2EMetrics{}
	stops := map[string]map[string]int64{"claude": {}, "codex": {}}
	for provider, emitted := range replies {
		want := metricsOfReplies(emitted)
		want.Sessions = int64(len(sessions[provider]))
		truth[provider] = want
		for _, reply := range emitted {
			stops[provider][reply.stop]++
		}
	}

	_, body := proxy.management(t, "/stats", rangeQuery("group_by", "provider"), true)
	t.Logf("GET /stats?group_by=provider\n%s", body)
	byProvider := map[string]usageE2EMetrics{}
	for _, group := range proxy.stats(t, rangeQuery("group_by", "provider")) {
		byProvider[group.Dimensions["provider"]] = group.usageE2EMetrics
	}
	var total usageE2EMetrics
	for _, provider := range []string{"claude", "codex"} {
		got, want := byProvider[provider], truth[provider]
		if got != want {
			t.Errorf("%s: /stats = %+v, want what the fake emitted %+v", provider, got, want)
		} else {
			t.Logf("%s: /stats matches the fake: %+v", provider, got)
		}
		total.Requests += want.Requests
		total.Turns += want.Turns
		total.Sessions += want.Sessions
	}

	all := proxy.stats(t, rangeQuery())
	if len(all) != 1 || all[0].Requests != total.Requests || all[0].Turns != total.Turns || all[0].Sessions != total.Sessions {
		t.Errorf("range total = %+v, want %d requests, %d turns, %d sessions", all, total.Requests, total.Turns, total.Sessions)
	}

	var websocketReplies []fakeReply
	for _, reply := range replies["codex"] {
		if reply.overWebsocket {
			websocketReplies = append(websocketReplies, reply)
		}
	}
	overWebsocket := metricsOfReplies(websocketReplies)
	overWebsocket.Sessions = 1
	websocketSession := proxy.stats(t, rangeQuery("filter.session_id", codexWebsocket))
	if overWebsocket.Requests != int64(len(websocketPrompts)) || len(websocketSession) != 1 || websocketSession[0].usageE2EMetrics != overWebsocket {
		t.Errorf("websocket session = %+v, want what the fake sent over its websocket %+v", websocketSession, overWebsocket)
	} else {
		t.Logf("codex websocket: /stats for the session matches the %d replies the fake sent over its websocket: %+v", overWebsocket.Requests, overWebsocket)
	}

	topLevel := proxy.stats(t, rangeQuery("filter.provider", "claude", "filter.parent_session_id", ""))
	if len(topLevel) != 1 || topLevel[0].Sessions != 2 {
		t.Errorf("Claude sessions without a parent = %+v, want 2: the main session and the other one, not the subagent", topLevel)
	}

	for _, provider := range []string{"claude", "codex"} {
		got := map[string]int64{}
		for _, group := range proxy.stats(t, rangeQuery("group_by", "stop_reason", "filter.provider", provider)) {
			got[group.Dimensions["stop_reason"]] = group.Requests
		}
		if len(got) != len(stops[provider]) {
			t.Errorf("%s stop reasons = %v, want %v", provider, got, stops[provider])
		}
		for stop, want := range stops[provider] {
			if got[stop] != want {
				t.Errorf("%s stop_reason %s = %d requests, want %d", provider, stop, got[stop], want)
			}
		}
		t.Logf("%s stop reasons: stored %v, emitted %v", provider, got, stops[provider])
	}
}
