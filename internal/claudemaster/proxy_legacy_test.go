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
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("legacy control reached inference") }), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer master-control-only" {
			t.Error("legacy master authorization changed")
		}
		if r.Header.Get("X-Environment-Runner-Version") == "" && r.Header.Get("X-Organization-Uuid") != "org_native" {
			t.Error("legacy CLI control identity changed")
		}
		if r.URL.Path == "/v1/sessions" {
			body, err := io.ReadAll(r.Body)
			if err != nil || (string(body) != creation && string(body) != strings.Replace(creation, `"environment_id":"env_native"`, `"self_hosted_runner_pool_id":"ccpool_native"`, 1)) {
				t.Error("validated native creation body changed")
			}
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
		bridge.Set("X-Environment-Runner-Version", "2.1.269")
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
		resp, _ := proxyTestRequest(t, client, tc.method, tc.path, tc.body, h)
		if resp.StatusCode != 403 {
			t.Errorf("non-native %s %s escaped legacy boundary: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
	bridge := headers.Clone()
	bridge.Del("X-Organization-Uuid")
	bridge.Set("Anthropic-Beta", "environments-2025-11-01")
	bridge.Set("X-Environment-Runner-Version", "2.1.269")
	for _, path := range []string{"/v1/sessions", "/v1/sessions/session_native/unarchive"} {
		resp, _ := proxyTestRequest(t, client, "POST", path, creation, bridge.Clone())
		if resp.StatusCode != 403 {
			t.Errorf("bridge profile escaped its callback paths: %s", path)
		}
	}
	if calls != 9 {
		t.Fatal("rejected legacy request reached master control")
	}
}
