package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeAPIKeyCloakedSystemPrefixAcrossTurns(t *testing.T) {
	const model = "claude-opus-5-5"
	const firstUser = `{"role":"user","content":"Investigate the build failure."}`
	const reply = `{"role":"assistant","content":"I will inspect the build."}`
	const nextUser = `{"role":"user","content":"Continue the investigation."}`
	const directive = `{"role":"system","content":[],"output_config":{"effort":"low"}}`
	const tool = `{"name":"inspect_build","description":"Inspect a build","input_schema":{"type":"object"}}`
	for _, path := range []string{"execute", "stream", "count_tokens"} {
		t.Run(path, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ctx := upstream.context(t, nil)
			ex := NewClaudeExecutor(midSystemConfig())
			auth := midSystemAuth()
			var initialUser, initialSystem string
			for _, turn := range []struct {
				name, messages, tools, effort string
				compacted                     bool
			}{
				{name: "first turn", messages: firstUser, tools: tool, effort: "medium"},
				{name: "growing history", messages: firstUser + "," + reply + "," + nextUser, tools: tool, effort: "medium"},
				{name: "effort update", messages: firstUser + "," + reply + "," + directive + "," + nextUser, tools: tool, effort: "high"},
				{name: "tool addition", messages: firstUser + "," + reply + "," + nextUser, tools: tool + `,{"name":"read_log","input_schema":{"type":"object"}}`, effort: "high"},
				{name: "tool removal", messages: firstUser + "," + reply + "," + nextUser, effort: "high"},
				{name: "compacted history", messages: `{"role":"user","content":"<conversation-checkpoint>The build is failing.</conversation-checkpoint>"},` + directive + "," + nextUser, tools: tool, effort: "low", compacted: true},
			} {
				t.Run(turn.name, func(t *testing.T) {
					payload := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"system":[{"type":"text","text":"Keep the investigation scoped to the build."}],"thinking":{"type":"adaptive"},"output_config":{"effort":%q},"tools":[%s],"messages":[%s]}`, model, turn.effort, turn.tools, turn.messages))
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
					switch path {
					case "execute":
						if _, err := ex.Execute(ctx, auth, req, opts); err != nil {
							t.Fatal(err)
						}
					case "stream":
						result, err := ex.ExecuteStream(ctx, auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count_tokens":
						if _, err := ex.CountTokens(ctx, auth, req, opts); err != nil {
							t.Fatal(err)
						}
					}
					if !upstream.called {
						t.Fatal("request did not reach the in-memory transport")
					}
					if got := ojson.GetBytes(upstream.body, "output_config.effort").String(); got != turn.effort {
						t.Errorf("effort = %q, want current request's %q", got, turn.effort)
					}
					if got, want := ojson.GetBytes(upstream.body, "tools.#").Int(), ojson.GetBytes(payload, "tools.#").Int(); got != want {
						t.Errorf("tool count = %d, want current request's %d", got, want)
					}
					systemTexts := ojson.GetBytes(upstream.body, "system.#.text").Raw
					if initialSystem == "" {
						initialSystem = systemTexts
					} else if !turn.compacted && systemTexts != initialSystem {
						t.Errorf("system text prefix changed:\nfirst: %s\nnow: %s", initialSystem, systemTexts)
					}
					systemIndex := 2 // Messages adds billing and CLI identity; count_tokens does not.
					if path == "count_tokens" {
						systemIndex = 0
					}
					if got := ojson.GetBytes(upstream.body, fmt.Sprintf("system.%d.text", systemIndex)).String(); got != "Keep the investigation scoped to the build." {
						t.Errorf("caller system prefix moved as history changed: %s", upstream.body)
					}
					for _, message := range ojson.GetBytes(upstream.body, "messages").Array() {
						if claudeMessageContentText(message.Get("content")) == "Keep the investigation scoped to the build." {
							t.Error("top-level guidance unexpectedly appeared in history")
						}
					}
					if initialUser == "" {
						initialUser = ojson.GetBytes(upstream.body, "messages.0").Raw
					} else if !turn.compacted && ojson.GetBytes(upstream.body, "messages.0").Raw != initialUser {
						t.Error("unchanged first user message was rewritten")
					}
					if turn.compacted && !strings.Contains(claudeMessageContentText(ojson.GetBytes(upstream.body, "messages.0.content")), "<conversation-checkpoint>The build is failing.</conversation-checkpoint>") {
						t.Error("compaction must forward the replacement anchor, not reuse stale history")
					}
					if turn.name == "effort update" || turn.compacted {
						found := false
						for _, message := range ojson.GetBytes(upstream.body, "messages").Array() {
							found = found || message.Raw == directive
						}
						if !found {
							t.Error("caller effort directive was changed or lost")
						}
					}
				})
			}
		})
	}
}
