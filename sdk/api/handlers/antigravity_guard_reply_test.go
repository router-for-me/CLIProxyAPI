package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	guardTestContentFilter = "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n"
	guardTestQuota         = `{"error":{"code":429,"message":"You have exhausted your capacity on this model.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`
)

func TestAntigravityGuardReplyReplacesContentFilter429(t *testing.T) {
	for protocol, want := range map[string]string{
		"openai":          `"code":"google_flagged_prompt"`,
		"openai-response": `"code":"google_flagged_prompt"`,
		"claude":          `"type":"permission_error"`,
		"gemini":          `"status":"PERMISSION_DENIED"`,
	} {
		original := &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errors.New(guardTestContentFilter)}
		got := antigravityGuardReplyFor(protocol, "antigravity", original)
		body := string(got.Body)
		if got.StatusCode != http.StatusForbidden || !got.DirectResponse || !strings.Contains(body, antigravityGuardMessage) || !strings.Contains(body, want) {
			t.Fatalf("%s: reply = %+v body=%s", protocol, got, body)
		}
		if got.Error != original.Error {
			t.Fatalf("%s: original error not kept", protocol)
		}
	}
}

func TestTrackSelectedAuthChainsExistingCallback(t *testing.T) {
	var previous string
	meta := map[string]any{coreexecutor.SelectedAuthCallbackMetadataKey: func(id string) { previous = id }}
	recorder := trackSelectedAuth(meta)
	meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string))("auth-1")
	if recorder.get() != "auth-1" || previous != "auth-1" {
		t.Fatalf("recorder=%q previous=%q, want auth-1 for both", recorder.get(), previous)
	}
	if trackSelectedAuth(nil).get() != "" {
		t.Fatal("nil metadata recorder is not empty")
	}
}

func TestAntigravityGuardReplyKeepsOtherErrors(t *testing.T) {
	cases := map[string]struct {
		provider string
		msg      *interfaces.ErrorMessage
	}{
		"quota 429":      {"antigravity", &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errors.New(guardTestQuota)}},
		"other provider": {"vertex", &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errors.New(guardTestContentFilter)}},
		"other status":   {"antigravity", &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New(guardTestContentFilter)}},
		"direct":         {"antigravity", &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errors.New(guardTestContentFilter), DirectResponse: true}},
	}
	for name, tc := range cases {
		if got := antigravityGuardReplyFor("openai", tc.provider, tc.msg); got != tc.msg {
			t.Fatalf("%s: message was replaced: %+v", name, got)
		}
	}
}
