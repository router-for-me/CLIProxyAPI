package executor

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// When the credential's sensitive-word rules would rewrite the classifier
// prompt, the prompt would no longer reach Anthropic in the classifier's own
// shape. The request then takes the normal cloaking path, with the CLI
// identity block, in both Execute and ExecuteStream.
func TestClaudeExecutor_ClassifierWithRewrittenPromptTakesNormalCloaking(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "execute stream"
		}
		t.Run(name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())
			ctx := upstream.context(t, nil)
			auth := midSystemAuth()
			auth.Attributes["cloak_sensitive_words"] = "security"
			req := cliproxyexecutor.Request{Model: "claude-sonnet-5", Payload: classifierPayload(classifierSystemPrompt)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			if stream {
				result, err := ex.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else if _, err := ex.Execute(ctx, auth, req, opts); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !upstream.called {
				t.Fatal("expected the request to reach the upstream")
			}

			system := ojson.GetBytes(upstream.body, "system").Raw
			if got := ojson.GetBytes(upstream.body, "system.1.text").String(); got != claudeCodeCLIIdentity {
				t.Fatalf("system.1 = %q, want the CLI identity block; system=%s", got, system)
			}
			if strings.Contains(string(upstream.body), "security monitor") {
				t.Fatalf("sensitive word reached upstream unchanged; body=%s", upstream.body)
			}
		})
	}
}

// claudeSystemTextRewrittenBy ignores the billing header and reports a change
// only when a rule matches real system text.
func TestClaudeSystemTextRewrittenBy(t *testing.T) {
	system := ojson.Parse(`[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.304;"},{"type":"text","text":"` + classifierSystemPrompt + `"}]`)
	if claudeSystemTextRewrittenBy(system, nil) {
		t.Fatal("nil matcher reported a rewrite")
	}
	if !claudeSystemTextRewrittenBy(system, (claudeCloakSettings{sensitiveWords: []string{"security"}}).buildSensitiveWordMatcher()) {
		t.Fatal("a matching rule did not report a rewrite")
	}
	if claudeSystemTextRewrittenBy(system, (claudeCloakSettings{sensitiveWords: []string{"unrelated-word"}}).buildSensitiveWordMatcher()) {
		t.Fatal("a rule that matches nothing reported a rewrite")
	}
}
