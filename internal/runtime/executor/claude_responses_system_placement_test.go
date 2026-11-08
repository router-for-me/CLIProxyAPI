package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// responsesMidSessionPayload is a Responses request whose developer item
// arrives after the conversation started, the shape Codex sends on later turns.
func responsesMidSessionPayload(model string) []byte {
	return []byte(`{"model":"` + model + `","instructions":"Top rule","input":[` +
		`{"role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"output_text","text":"ok"}]},` +
		`{"role":"developer","content":[{"type":"input_text","text":"Mid rule"}]},` +
		`{"role":"user","content":[{"type":"input_text","text":"go"}]}]}`)
}

func responsesPlacementAuth(attributes map[string]string) *cliproxyauth.Auth {
	auth := midSystemAuth()
	for key, value := range attributes {
		auth.Attributes[key] = value
	}
	return auth
}

type responsesPlacementCall func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload []byte) error

var responsesPlacementCalls = map[string]responsesPlacementCall{
	"execute": func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload []byte) error {
		t.Helper()
		_, err := ex.Execute(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
		return err
	},
	"stream": func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload []byte) error {
		t.Helper()
		result, err := ex.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
		if err != nil {
			return err
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	},
	"count_tokens": func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload []byte) error {
		t.Helper()
		_, err := ex.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
		return err
	},
}

func claudeSystemTurnTexts(body []byte) []string {
	var texts []string
	ojson.GetBytes(body, "messages").ForEach(func(_, message ojson.Result) bool {
		if message.Get("role").String() == "system" {
			texts = append(texts, claudeMessageContentText(message.Get("content")))
		}
		return true
	})
	return texts
}

// With the cloaked wire and a model that accepts mid-conversation system turns,
// the developer item reaches the upstream right after the user message it
// preceded, so the messages of earlier turns keep their cached prefix.
func TestResponsesMidSessionSystemItemStaysInPlace(t *testing.T) {
	const model = "claude-sonnet-5"
	for name, call := range responsesPlacementCalls {
		t.Run(name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())
			if err := call(t, ex, upstream.context(t, nil), midSystemAuth(), model, responsesMidSessionPayload(model)); err != nil {
				t.Fatalf("call error = %v", err)
			}
			if !upstream.called {
				t.Fatal("expected the request to reach the upstream")
			}
			messages := ojson.GetBytes(upstream.body, "messages").Array()
			last := len(messages) - 1
			if last < 1 || messages[last].Get("role").String() != "system" || messages[last-1].Get("role").String() != "user" {
				t.Fatalf("want the developer turn right after the last user message; body=%s", upstream.body)
			}
			if got := claudeMessageContentText(messages[last].Get("content")); got != "Mid rule" {
				t.Fatalf("in-place system turn = %q, want %q; body=%s", got, "Mid rule", upstream.body)
			}
		})
	}
}

// A model that rejects mid-conversation system turns keeps the top-level path,
// so the executor sees the same body as before in-place placement: both
// instructions in source order, placed together by the existing cloaking.
func TestResponsesMidSessionSystemItemKeepsTopLevelPathForLegacyModel(t *testing.T) {
	const model = "claude-sonnet-4-5"
	upstream := &midSystemUpstream{}
	ex := NewClaudeExecutor(midSystemConfig())
	if err := responsesPlacementCalls["execute"](t, ex, upstream.context(t, nil), midSystemAuth(), model, responsesMidSessionPayload(model)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if texts := claudeSystemTurnTexts(upstream.body); len(texts) != 0 {
		t.Fatalf("legacy body carries system turns %q; body=%s", texts, upstream.body)
	}
	body := string(upstream.body)
	top, mid := strings.Index(body, "Top rule"), strings.Index(body, "Mid rule")
	if top < 0 || mid < 0 || mid < top {
		t.Fatalf("want both instructions in source order; body=%s", upstream.body)
	}
}

// Strict cloaking drops every caller system instruction. A translated
// mid-session developer item must not survive it as an in-place turn.
func TestResponsesMidSessionSystemItemDroppedByStrictCloaking(t *testing.T) {
	const model = "claude-sonnet-5"
	for name, call := range responsesPlacementCalls {
		t.Run(name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())
			auth := responsesPlacementAuth(map[string]string{"cloak_strict_mode": "true"})
			if err := call(t, ex, upstream.context(t, nil), auth, model, responsesMidSessionPayload(model)); err != nil {
				t.Fatalf("call error = %v", err)
			}
			if !upstream.called {
				t.Fatal("expected the request to reach the upstream")
			}
			body := string(upstream.body)
			if strings.Contains(body, "Top rule") || strings.Contains(body, "Mid rule") {
				t.Fatalf("strict cloaking must drop every caller instruction; body=%s", upstream.body)
			}
		})
	}
}

// An uncloaked request forwards the caller's betas, so it never gains the
// mid-conversation system beta and keeps both instructions top-level.
func TestResponsesMidSessionSystemItemUncloakedStaysTopLevel(t *testing.T) {
	const model = "claude-sonnet-5"
	upstream := &midSystemUpstream{}
	ex := NewClaudeExecutor(midSystemConfig())
	auth := responsesPlacementAuth(map[string]string{"cloak_mode": "never"})
	if err := responsesPlacementCalls["execute"](t, ex, upstream.context(t, nil), auth, model, responsesMidSessionPayload(model)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if texts := claudeSystemTurnTexts(upstream.body); len(texts) != 0 {
		t.Fatalf("uncloaked body carries system turns %q; body=%s", texts, upstream.body)
	}
	system := ojson.GetBytes(upstream.body, "system.#.text").Raw
	if system != `["Top rule","Mid rule"]` {
		t.Fatalf("system = %s, want both instructions in source order; body=%s", system, upstream.body)
	}
}

// An unsupported system part keeps failing explicitly on the top-level path,
// even when a text-only developer item would otherwise stay in place. With the
// rebuild opt-in, an in-place turn would be folded through a text-only filter
// that drops the unsupported part, so the opt-in must keep the top-level path.
func TestResponsesUnsupportedSystemPartStillRejected(t *testing.T) {
	for _, test := range []struct {
		name       string
		model      string
		attributes map[string]string
	}{
		{name: "legacy model", model: "claude-sonnet-4-5"},
		{name: "supported model", model: "claude-sonnet-5"},
		{name: "rebuild opt-in", model: "claude-sonnet-5", attributes: map[string]string{"rebuild_mid_system_message": "true"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := test.model
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())
			payload := []byte(`{"model":"` + model + `","input":[` +
				`{"role":"user","content":[{"type":"input_text","text":"hi"}]},` +
				`{"role":"developer","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]},` +
				`{"role":"developer","content":[{"type":"input_text","text":"Mid rule"}]},` +
				`{"role":"user","content":[{"type":"input_text","text":"go"}]}]}`)
			err := responsesPlacementCalls["execute"](t, ex, upstream.context(t, nil), responsesPlacementAuth(test.attributes), model, payload)
			if err == nil {
				t.Fatalf("Execute() error = nil, want the unsupported part rejected; body=%s", upstream.body)
			}
			if upstream.called {
				t.Fatalf("upstream must not be called; body=%s", upstream.body)
			}
			var blockErr claudeCallerSystemBlockError
			if !errors.As(err, &blockErr) || blockErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("error = %v, want the unsupported system block rejected with a 400", err)
			}
		})
	}
}

// Local estimation sends nothing upstream, so it keeps every system item at the
// top level, where its request validation accepts them, even for a credential
// whose Messages requests would carry an in-place turn.
func TestResponsesMidSessionSystemItemLocalCountTokensKeepsTopLevel(t *testing.T) {
	const model = "claude-sonnet-5"
	for _, attributes := range []map[string]string{
		{"base_url": "https://example.invalid"},
		{"base_url": "https://example.invalid", "cloak_strict_mode": "true"},
	} {
		upstream := &midSystemUpstream{}
		ex := NewClaudeExecutor(midSystemConfig())
		auth := responsesPlacementAuth(attributes)
		resp, err := ex.CountTokens(upstream.context(t, nil), auth, cliproxyexecutor.Request{Model: model, Payload: responsesMidSessionPayload(model)},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
		if err != nil {
			t.Fatalf("CountTokens(%v) error = %v, want a local estimate", attributes, err)
		}
		if upstream.called {
			t.Fatalf("CountTokens(%v) must estimate locally for a custom base URL", attributes)
		}
		if len(resp.Payload) == 0 {
			t.Fatalf("CountTokens(%v) returned an empty payload", attributes)
		}
	}
}

// A payload rule that switches the model after translation is outside the
// placement decision. The existing validation answers it explicitly instead of
// sending a turn the model rejects.
func TestResponsesMidSessionSystemItemLateLegacyOverrideRejected(t *testing.T) {
	upstream := &midSystemUpstream{}
	cfg := midSystemConfig()
	cfg.Payload.Override = []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"model": "claude-haiku-4-5-20251001"},
	}}
	ex := NewClaudeExecutor(cfg)
	err := responsesPlacementCalls["execute"](t, ex, upstream.context(t, nil), midSystemAuth(), "claude-sonnet-5", responsesMidSessionPayload("claude-sonnet-5"))
	assertMidSystemRejected(t, err, upstream)
}
