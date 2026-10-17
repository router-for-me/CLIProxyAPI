package executor

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestInsertClaudeMidConversationSystemMessages_EffortDirective(t *testing.T) {
	const directive = `{"role":"system","content":[],"output_config":{"effort":"low"}}`
	const user = `{"role":"user","content":"next"}`
	for _, test := range []struct {
		name     string
		tail     string
		insertAt int
	}{
		{name: "compacted history", tail: directive + "," + user, insertAt: 3},
		{name: "before assistant", tail: directive + "," + user + `,{"role":"assistant","content":"answer"}`, insertAt: 3},
		{name: "multiple directives", tail: directive + "," + directive + "," + user, insertAt: 4},
		{name: "terminal directive", tail: directive, insertAt: 2},
		{name: "content-bearing system", tail: `{"role":"system","content":[{"type":"text","text":"rule"}],"output_config":{"effort":"low"}},` + user, insertAt: 1},
		{name: "empty system without output config", tail: `{"role":"system","content":[]},` + user, insertAt: 1},
		{name: "string content", tail: `{"role":"system","content":"","output_config":{"effort":"low"}},` + user, insertAt: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(`{"messages":[{"role":"user","content":"summary"},` + test.tail + `]}`)
			before := gjson.GetBytes(payload, "messages").Array()
			out := insertClaudeMidConversationSystemMessages(payload, []string{"first guidance", "second guidance"})
			after := gjson.GetBytes(out, "messages").Array()
			if len(after) != len(before)+2 {
				t.Fatalf("message count = %d, want %d: %s", len(after), len(before)+2, out)
			}
			assertClaudeMidConversationSystemMessage(t, out, test.insertAt, "first guidance", "")
			assertClaudeMidConversationSystemMessage(t, out, test.insertAt+1, "second guidance", "")
			for idx, message := range before {
				outIdx := idx
				if idx >= test.insertAt {
					outIdx += 2
				}
				if after[outIdx].Raw != message.Raw {
					t.Fatalf("caller message %d changed or moved incorrectly: %s", idx, out)
				}
			}
			second := insertClaudeMidConversationSystemMessages(out, []string{"first guidance", "second guidance"})
			if !bytes.Equal(out, second) {
				t.Fatalf("insertion is not idempotent:\nfirst: %s\nsecond: %s", out, second)
			}
		})
	}
}

func TestClaudeSystemPlacement_EffortDirectivePolicies(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"caller guidance"}],` +
		`"messages":[{"role":"user","content":"summary"},` +
		`{"role":"system","content":[],"output_config":{"effort":"low"}},` +
		`{"role":"user","content":"next"}]}`)
	for _, keepTopLevel := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep top level %v", keepTopLevel), func(t *testing.T) {
			for _, test := range []struct {
				name string
				out  []byte
			}{
				{name: "messages", out: checkSystemInstructionsWithMode(payload, false, keepTopLevel)},
				{name: "count tokens", out: relocateClaudeSystemPromptForCountTokensWithPolicy(payload, false, false, keepTopLevel)},
			} {
				t.Run(test.name, func(t *testing.T) {
					if got := gjson.GetBytes(test.out, "messages.1").Raw; got != gjson.GetBytes(payload, "messages.1").Raw {
						t.Fatalf("caller effort directive changed or moved: %s", test.out)
					}
					wantMessages := int64(4)
					if keepTopLevel {
						wantMessages = 3
						if !bytes.Contains([]byte(gjson.GetBytes(test.out, "system").Raw), []byte("caller guidance")) {
							t.Fatalf("caller guidance missing from top-level system: %s", test.out)
						}
					} else {
						assertClaudeMidConversationSystemMessage(t, test.out, 3, "caller guidance", "")
						if bytes.Contains([]byte(gjson.GetBytes(test.out, "system").Raw), []byte("caller guidance")) {
							t.Fatalf("relocated caller guidance remains in top-level system: %s", test.out)
						}
					}
					if got := gjson.GetBytes(test.out, "messages.#").Int(); got != wantMessages {
						t.Fatalf("message count = %d, want %d: %s", got, wantMessages, test.out)
					}
				})
			}
		})
	}
}

func TestCaptureClaudeCodeSystemPlacement_EffortDirective(t *testing.T) {
	before := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"guidance"}],` +
		`"messages":[{"role":"user","content":"summary"},` +
		`{"role":"system","content":[],"output_config":{"effort":"low"}},` +
		`{"role":"user","content":"next"}]}`)
	// This literal fixture is independent of the insertion helper's decision.
	after := []byte(`{"messages":[{"role":"user","content":"summary"},` +
		`{"role":"system","content":[],"output_config":{"effort":"low"}},` +
		`{"role":"user","content":"next"},` +
		`{"role":"system","content":[{"type":"text","text":"guidance"}]}]}`)
	state := captureClaudeCodeSystemPlacement(before, after, true)
	if state.insertAt != 3 || len(state.insertedRaw) != 1 || len(state.texts) != 1 {
		t.Fatalf("placement = %+v, want one inserted guidance turn at index 3", state)
	}
	if state.insertedRaw[0] != gjson.GetBytes(after, "messages.3").Raw || state.texts[0] != "guidance" {
		t.Fatalf("placement captured the wrong turn: %+v", state)
	}
	if callerOwned := captureClaudeCodeSystemPlacement(before, before, true); len(callerOwned.insertedRaw) != 0 {
		t.Fatalf("placement must not claim caller-owned turns: %+v", callerOwned)
	}
}

func TestClaudeExecutor_EffortDirectiveAfterCompaction(t *testing.T) {
	const model = "claude-opus-5-5"
	payload := []byte(`{"model":"claude-opus-5-5","max_tokens":64,` +
		`"system":[{"type":"text","text":"caller guidance"}],` +
		`"output_config":{"effort":"medium"},"thinking":{"type":"adaptive"},` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"<conversation-checkpoint>Earlier we said hello.</conversation-checkpoint>"}]},` +
		`{"role":"system","content":[],"output_config":{"effort":"low"}},` +
		`{"role":"user","content":[{"type":"text","text":"Reply with just: ok"}]}]}`)
	for _, path := range []string{"execute", "stream", "count tokens"} {
		t.Run(path, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ctx := upstream.context(t, http.Header{"Anthropic-Beta": []string{"mid-conversation-output-config-2026-07-01"}})
			auth := &cliproxyauth.Auth{
				ID:         "effort-directive-oauth",
				Attributes: map[string]string{"api_key": "sk-ant-oat-effort-directive", "cloak_mode": "always"},
				Metadata: map[string]any{
					"account_uuid": "11111111-2222-4333-8444-555555555555",
					"claude_device_ids": []string{
						"0000000000000000000000000000000000000000000000000000000000000001",
					},
				},
			}
			ex := NewClaudeExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: model, Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			switch path {
			case "execute":
				if _, errExecute := ex.Execute(ctx, auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			case "stream":
				result, errStream := ex.ExecuteStream(ctx, auth, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			case "count tokens":
				if _, errCount := ex.CountTokens(ctx, auth, req, opts); errCount != nil {
					t.Fatal(errCount)
				}
			}
			if !upstream.called {
				t.Fatal("upstream request was not sent")
			}
			if got := gjson.GetBytes(upstream.body, "messages.1").Raw; got != gjson.GetBytes(payload, "messages.1").Raw {
				t.Fatalf("caller effort directive changed or moved on the wire: %s", upstream.body)
			}
			if got := gjson.GetBytes(upstream.body, "messages.#.role").Raw; got != `["user","system","user","system"]` {
				t.Fatalf("wire roles = %s, want [user, directive, user, caller system]: %s", got, upstream.body)
			}
			if got := gjson.GetBytes(upstream.body, "messages.3.content.0.text").String(); got != "caller guidance" {
				t.Fatalf("caller guidance missing from trailing system message: %s", upstream.body)
			}
			if bytes.Contains([]byte(gjson.GetBytes(upstream.body, "system").Raw), []byte("caller guidance")) {
				t.Fatalf("OAuth caller guidance must not remain in top-level system: %s", upstream.body)
			}
		})
	}
}
