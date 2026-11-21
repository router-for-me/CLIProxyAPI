package claude

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

func TestAutoModeFollowsOnlySameClientMainSession(t *testing.T) {
	var state autoModeSessionModels
	now := time.Now()
	headers := func(session, class string) http.Header {
		h := http.Header{}
		h.Set("X-Claude-Code-Session-Id", session)
		h.Set("X-Claude-Code-Request-Class", class)
		return h
	}
	main := []byte(`{"model":"gpt-6-astra","output_config":{"effort":"medium"},"messages":[{"role":"user","content":"hello"}]}`)
	classifier := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\nhello"}]}]}`)
	state.route(headers("a", "main"), "config-inline", "key1", main, now)
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
			got := state.route(headers(tc.session, tc.class), "config-inline", tc.key, classifier, tc.at)
			if ojson.GetBytesNoCopy(got, "model").String() != tc.want {
				t.Fatalf("unexpected route: %s", got)
			}
			if ojson.GetBytesNoCopy(got, "messages").Raw != ojson.GetBytesNoCopy(classifier, "messages").Raw {
				t.Fatal("changed classifier evidence")
			}
		})
	}
	// Session selection changes replace the previous selection; subagents cannot overwrite it.
	state.route(headers("a", "main"), "config-inline", "key1", []byte(`{"model":"gpt-6.1-sol"}`), now)
	state.route(headers("a", "subagent"), "config-inline", "key1", main, now)
	got := state.route(headers("a", "auxiliary"), "config-inline", "key1", classifier, now)
	if ojson.GetBytesNoCopy(got, "model").String() != "gpt-6.1-sol" {
		t.Fatalf("model switch lost: %s", got)
	}
	for _, body := range []string{
		`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"summarize"}]}`,
		`{"model":"claude-sonnet-5","tools":[{"name":"Bash"}],"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"}]}]}`,
	} {
		if string(state.route(headers("a", "auxiliary"), "config-inline", "key1", []byte(body), now)) != body {
			t.Fatal("changed non-classifier request")
		}
	}
}

func TestAutoModeMetadataSessionFallback(t *testing.T) {
	var state autoModeSessionModels
	h := http.Header{}
	h.Set("X-Claude-Code-Request-Class", "main")
	state.route(h, "config-inline", "key1", []byte(`{"model":"gpt-6-astra","metadata":{"user_id":"{\"session_id\":\"a\"}"}}`), time.Now())
	h.Set("X-Claude-Code-Request-Class", "auxiliary")
	got := state.route(h, "config-inline", "key1", []byte(`{"model":"claude-sonnet-5","metadata":{"user_id":"{\"session_id\":\"a\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"}]}]}`), time.Now())
	if ojson.GetBytesNoCopy(got, "model").String() != "gpt-6-astra" {
		t.Fatalf("metadata route failed: %s", got)
	}
}

func TestAutoModeCacheIsolationAndLimits(t *testing.T) {
	var state autoModeSessionModels
	now := time.Unix(1000, 0)
	main := http.Header{}
	main.Set("X-Claude-Code-Request-Class", "main")
	main.Set("X-Claude-Code-Session-Id", "shared")
	state.route(main, "config-inline", "", []byte(`{"model":"unauthenticated"}`), now)
	state.route(main, "config-inline", "client-one", []byte(`{"model":123}`), now)
	state.route(main, "config-inline", "client-one", []byte(fmt.Sprintf(`{"model":%q}`, strings.Repeat("m", autoModeMaxModelBytes+1))), now)
	if len(state.entries) != 0 {
		t.Fatalf("cached an ineligible model or session: %d entries", len(state.entries))
	}

	for i := range autoModeSessionCapacity {
		main.Set("X-Claude-Code-Session-Id", fmt.Sprintf("session-%d", i))
		state.route(main, "config-inline", "client-one", []byte(`{"model":"initial"}`), now.Add(time.Duration(i)*time.Second))
	}
	last := fmt.Sprintf("session-%d", autoModeSessionCapacity-1)
	main.Set("X-Claude-Code-Session-Id", last)
	state.route(main, "config-inline", "client-one", []byte(`{"model":"updated"}`), now.Add(time.Hour))
	if len(state.entries) != autoModeSessionCapacity {
		t.Fatalf("refresh evicted a session: %d entries", len(state.entries))
	}

	classifier := []byte(`{"model":"native","messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\nhello"}]}]}`)
	auxiliary := main.Clone()
	auxiliary.Set("X-Claude-Code-Request-Class", "auxiliary")
	for _, tt := range []struct {
		provider, principal, want string
	}{
		{"config-inline", "client-one", "updated"},
		{"config-inline", "client-two", "native"},
		{"another-provider", "client-one", "native"},
		{"", "", "native"},
	} {
		got := state.route(auxiliary, tt.provider, tt.principal, classifier, now.Add(time.Hour))
		if model := ojson.GetBytesNoCopy(got, "model").String(); model != tt.want {
			t.Errorf("route for %s/%s = %q, want %q", tt.provider, tt.principal, model, tt.want)
		}
	}

	main.Set("X-Claude-Code-Session-Id", "new")
	state.route(main, "config-inline", "client-one", []byte(`{"model":"new"}`), now.Add(time.Hour))
	auxiliary.Set("X-Claude-Code-Session-Id", "session-0")
	got := state.route(auxiliary, "config-inline", "client-one", classifier, now.Add(time.Hour))
	if len(state.entries) != autoModeSessionCapacity || string(got) != string(classifier) {
		t.Fatalf("new entry should evict the oldest session; got %d entries and %s", len(state.entries), got)
	}
}
