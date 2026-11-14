package claude

import (
	"net/http"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestAutoModeFollowsOnlySameClientMainSession(t *testing.T) {
	var state autoModeSessionModels
	now := time.Now()
	headers := func(session, key, class string) http.Header {
		h := http.Header{}
		h.Set("X-Claude-Code-Session-Id", session)
		h.Set("X-Api-Key", key)
		h.Set("X-Claude-Code-Request-Class", class)
		return h
	}
	main := []byte(`{"model":"gpt-6-astra","output_config":{"effort":"medium"},"messages":[{"role":"user","content":"hello"}]}`)
	classifier := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\nhello"}]}]}`)
	state.route(headers("a", "key1", "main"), main, now)
	for _, tc := range []struct {
		name, session, key, class, want string
		at                              time.Time
	}{
		{"same session", "a", "key1", "auxiliary", "gpt-6-astra", now},
		{"other session", "b", "key1", "auxiliary", "claude-sonnet-5", now},
		{"other client", "a", "key2", "auxiliary", "claude-sonnet-5", now},
		{"subagent cannot follow", "a", "key1", "subagent", "claude-sonnet-5", now},
		{"no classification header", "a", "key1", "", "claude-sonnet-5", now},
		{"expired", "a", "key1", "auxiliary", "claude-sonnet-5", now.Add(25 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := state.route(headers(tc.session, tc.key, tc.class), classifier, tc.at)
			if gjson.GetBytes(got, "model").String() != tc.want {
				t.Fatalf("unexpected route: %s", got)
			}
			if gjson.GetBytes(got, "messages").Raw != gjson.GetBytes(classifier, "messages").Raw {
				t.Fatal("changed classifier evidence")
			}
		})
	}
	// Session selection changes replace the previous selection; subagents cannot overwrite it.
	state.route(headers("a", "key1", "main"), []byte(`{"model":"gpt-6.1-sol"}`), now)
	state.route(headers("a", "key1", "subagent"), main, now)
	got := state.route(headers("a", "key1", "auxiliary"), classifier, now)
	if gjson.GetBytes(got, "model").String() != "gpt-6.1-sol" {
		t.Fatalf("model switch lost: %s", got)
	}
	for _, body := range []string{
		`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"summarize"}]}`,
		`{"model":"claude-sonnet-5","tools":[{"name":"Bash"}],"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"}]}]}`,
	} {
		if string(state.route(headers("a", "key1", "auxiliary"), []byte(body), now)) != body {
			t.Fatal("changed non-classifier request")
		}
	}
}

func TestAutoModeMetadataSessionFallback(t *testing.T) {
	var state autoModeSessionModels
	h := http.Header{}
	h.Set("X-Claude-Code-Request-Class", "main")
	state.route(h, []byte(`{"model":"gpt-6-astra","metadata":{"user_id":"{\"session_id\":\"a\"}"}}`), time.Now())
	h.Set("X-Claude-Code-Request-Class", "auxiliary")
	got := state.route(h, []byte(`{"model":"claude-sonnet-5","metadata":{"user_id":"{\"session_id\":\"a\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"}]}]}`), time.Now())
	if gjson.GetBytes(got, "model").String() != "gpt-6-astra" {
		t.Fatalf("metadata route failed: %s", got)
	}
}
