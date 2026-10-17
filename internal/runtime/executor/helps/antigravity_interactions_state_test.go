package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestIssue5190ContinuationIsolation(t *testing.T) {
	previous := antigravityInteractionsSessions
	t.Cleanup(func() { antigravityInteractionsSessions = previous })
	const initial = `{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`
	const continued = `{"input":[{"content":[{"text":"hi","type":"text"}],"type":"user_input"},{"type":"function_call","id":"call_1"},{"type":"function_result","call_id":"call_1","result":"ok"}]}`
	const model = "antigravity-preview-05-2026"
	for _, name := range []string{"match", "caller", "credential", "key", "model", "call", "prefix", "explicit", "environment", "missing", "partial", "failed"} {
		t.Run(name, func(t *testing.T) {
			antigravityInteractionsSessions = cache.NewInteractionsSessionCache()
			auth := &cliproxyauth.Auth{ID: "credential", Provider: "gemini-interactions", Attributes: map[string]string{"api_key": "secret"}}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller"}}
			req := cliproxyexecutor.Request{}
			state, _ := PrepareAntigravityInteractions(auth, req, opts, model, []byte(initial))
			response := `{"id":"interaction_1","environment_id":"env_1","status":"requires_action","steps":[{"type":"function_call","id":"call_1"}]}`
			if name == "partial" {
				response = `{"id":"interaction_1","status":"requires_action","steps":[{"type":"function_call","id":"call_1"},{"type":"function_call","id":"call_2"}]}`
			}
			if name == "failed" {
				response = `{"id":"interaction_1","status":"failed","steps":[{"type":"function_call","id":"call_1"}]}`
			}
			state.Observe([]byte(response))
			body := []byte(continued)
			targetModel := model
			want := ""
			switch name {
			case "match":
				want = "interaction_1"
			case "caller":
				opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "other"
			case "credential":
				auth.ID = "other"
			case "key":
				auth.Attributes["api_key"] = "rotated"
			case "model":
				targetModel = "antigravity-other"
			case "call":
				body = []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_result","call_id":"other","result":"ok"}]}`)
			case "prefix":
				body = []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"different"}]},{"type":"function_result","call_id":"call_1","result":"ok"}]}`)
			case "missing":
				body = []byte(initial)
			case "explicit":
				body = append([]byte(`{"previous_interaction_id":"explicit",`), body[1:]...)
				want = "explicit"
			case "environment":
				body = append([]byte(`{"environment_id":"explicit-env",`), body[1:]...)
				want = "interaction_1"
			}
			_, rewritten := PrepareAntigravityInteractions(auth, req, opts, targetModel, body)
			if got := gjson.GetBytes(rewritten, "previous_interaction_id").String(); got != want {
				t.Fatalf("continuation=%q want=%q body=%s", got, want, rewritten)
			}
			if want == "" || name == "explicit" {
				if string(body) != string(rewritten) {
					t.Fatalf("unexpected rewrite: %s", rewritten)
				}
			}
			if name == "environment" && gjson.GetBytes(rewritten, "environment_id").String() != "explicit-env" {
				t.Fatalf("explicit environment overwritten: %s", rewritten)
			}
		})
	}
}
