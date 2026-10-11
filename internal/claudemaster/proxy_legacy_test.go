package claudemaster

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProxyLegacyRemoteControlBoundary(t *testing.T) {
	const creation = `{"source":"remote-control","environment_id":"env_native","events":[],"session_context":{"sources":[],"outcomes":[],"model":"claude-sonnet-4-6","cwd":"/scratch","reuse_outcome_branches":true}}`
	headers := http.Header{"Anthropic-Beta": {"ccr-byoc-2025-07-29"}, "X-Organization-Uuid": {"org_native"}, "Authorization": {"Bearer master-control-only"}, "Content-Type": {"application/json"}}
	calls := 0
	sent := map[string]string{} // method and path -> the body the client sent, which Anthropic must receive unchanged
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("legacy control reached inference") }), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer master-control-only" {
			t.Error("legacy master authorization changed")
		}
		if r.Header.Get("X-Environment-Runner-Version") == "" && r.Header.Get("X-Organization-Uuid") != "org_native" {
			t.Error("legacy CLI control identity changed")
		}
		var body []byte
		var err error
		if r.Body != nil {
			body, err = io.ReadAll(r.Body)
		}
		if err != nil || string(body) != sent[r.Method+" "+r.URL.Path] {
			t.Errorf("%s %s body changed on the way: %q", r.Method, r.URL.Path, body)
		}
		return proxyTestResponse(204, ""), nil
	}))
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/sessions", creation},
		{"POST", "/v1/sessions", strings.Replace(creation, `"environment_id":"env_native"`, `"self_hosted_runner_pool_id":"ccpool_native"`, 1)},
		{"GET", "/v1/sessions/session_native", ""}, {"PATCH", "/v1/sessions/session_native", `{"title":"native"}`},
		{"POST", "/v1/sessions/session_native/events", `{"events":[]}`},
		{"POST", "/v1/sessions/session_native/archive", `{}`}, {"POST", "/v1/sessions/session_native/unarchive", `{}`},
	} {
		sent[tc.method+" "+tc.path] = tc.body
		resp, _ := proxyTestRequest(t, client, tc.method, tc.path, tc.body, headers.Clone())
		if resp.StatusCode != 204 {
			t.Errorf("native %s %s was blocked: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
	if calls != 7 {
		t.Fatalf("expected seven native control dispatches, got %d", calls)
	}
	for _, action := range []string{"events", "archive"} {
		bridge := headers.Clone()
		bridge.Del("X-Organization-Uuid")
		bridge.Set("Anthropic-Beta", "environments-2025-11-01")
		bridge.Set("X-Environment-Runner-Version", "2.1.285")
		sent["POST /v1/sessions/session_native/"+action] = `{}`
		resp, _ := proxyTestRequest(t, client, "POST", "/v1/sessions/session_native/"+action, `{}`, bridge)
		if resp.StatusCode != 204 {
			t.Errorf("native bridge %s was blocked: %d", action, resp.StatusCode)
		}
	}
	for _, tc := range []struct{ method, path, body, beta string }{
		{"POST", "/v1/sessions", creation, ""},
		{"POST", "/v1/sessions", creation, "ccr-byoc-2025-07-29,managed-agents-2026-04-01"},
		{"POST", "/v1/sessions", `{"agent":"agent_managed","environment_id":"env_native","events":[]}`, "ccr-byoc-2025-07-29"},
		{"POST", "/v1/sessions", strings.Replace(creation, `"source":"remote-control"`, `"source":"remote-control","agent":"agent_managed"`, 1), "ccr-byoc-2025-07-29"},
		{"POST", "/v1/sessions", strings.Replace(creation, "remote-control", "web", 1), "ccr-byoc-2025-07-29"},
		{"POST", "/v1/sessions", strings.Replace(creation, `"environment_id":"env_native",`, "", 1), "ccr-byoc-2025-07-29"},
		{"POST", "/v1/sessions/session_native/messages", `{}`, "ccr-byoc-2025-07-29"},
		{"GET", "/v1/sessions/session_native/events", `{}`, "ccr-byoc-2025-07-29"},
		{"DELETE", "/v1/sessions/session_native", `{}`, "ccr-byoc-2025-07-29"},
		{"POST", "/v1/sessions/session_native/events", `{}`, "environments-2025-11-01"},
	} {
		h := headers.Clone()
		h.Set("Anthropic-Beta", tc.beta)
		sent[tc.method+" "+tc.path] = tc.body
		resp, _ := proxyTestRequest(t, client, tc.method, tc.path, tc.body, h)
		// Not Remote Control's own shape (managed agents, another source, another beta): relayed as sent, on the
		// session's own login, as Claude Code would send it without claude-master.
		if resp.StatusCode != 204 {
			t.Errorf("%s %s was not relayed: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
	bridge := headers.Clone()
	bridge.Del("X-Organization-Uuid")
	bridge.Set("Anthropic-Beta", "environments-2025-11-01")
	bridge.Set("X-Environment-Runner-Version", "2.1.285")
	for _, path := range []string{"/v1/sessions", "/v1/sessions/session_native/unarchive"} {
		sent["POST "+path] = creation
		resp, _ := proxyTestRequest(t, client, "POST", path, creation, bridge.Clone())
		if resp.StatusCode != 204 {
			t.Errorf("bridge request to %s was not relayed: %d", path, resp.StatusCode)
		}
	}
	if calls != 21 {
		t.Fatalf("expected every request relayed to Anthropic once, got %d", calls)
	}
}
