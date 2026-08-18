// Package executor provides per-provider runtime executors.
package executor

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// CodeBuddyCNBaseURL is the Tencent CodeBuddy CN OpenAI-compatible gateway.
const CodeBuddyCNBaseURL = "https://copilot.tencent.com/v2/chat/completions"

// CodeBuddyCNExecutor talks to the CodeBuddy CN (Tencent) OpenAI-compatible gateway.
//
// CodeBuddy CN rejects non-stream chat requests (HTTP 400, code 11101). To keep the
// OpenAI-compatible executor path intact we force streaming at the executor boundary
// and let the HTTP handler aggregate the SSE back into a JSON response for
// non-streaming clients.
//
// Reasoning is opt-in: reasoning_summary:"auto" is only added when the client
// explicitly sets reasoning_effort. "none"/"off" drops the field entirely (the
// gateway has no "none" value). Plain requests are left untouched — forcing
// reasoning on plain requests trips CodeBuddy's content filter.
//
// Agent system prompts are replaced with a neutral prompt because Tencent's
// content filter flags CLI agent system prompts (e.g. "You are Claude Code…") as
// sensitive content and rejects the whole request.
type CodeBuddyCNExecutor struct {
	*OpenAICompatExecutor
}

// NewCodeBuddyCNExecutor constructs a CodeBuddy CN executor.
func NewCodeBuddyCNExecutor(cfg *config.Config) *CodeBuddyCNExecutor {
	return &CodeBuddyCNExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor(constant.CodeBuddyCN, cfg),
	}
}

// Identifier returns the executor identifier.
func (e *CodeBuddyCNExecutor) Identifier() string { return constant.CodeBuddyCN }

// Execute forces streaming and delegates to the OpenAI-compatible executor.
func (e *CodeBuddyCNExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	opts.Stream = true
	return e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream forces streaming and delegates to the OpenAI-compatible executor.
func (e *CodeBuddyCNExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	opts.Stream = true
	return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
}

// applyOutgoingTransforms mutates the final OpenAI upstream body: forcing
// stream, mapping reasoning_effort to reasoning_summary, and neutralizing
// agent system prompts.
func (e *CodeBuddyCNExecutor) applyOutgoingTransforms(ctx context.Context, auth *cliproxyauth.Auth, baseModel string, opts cliproxyexecutor.Options, translated []byte) []byte {
	body := translated
	if len(body) == 0 {
		return body
	}

	body, _ = sjson.SetBytes(body, "stream", true)

	body = applyCodeBuddyCNReasoning(body)
	body = applyCodeBuddyCNAgentSystemPrompt(body)
	return body
}

// applyCodeBuddyCNReasoning maps reasoning_effort to reasoning_summary.
// "none"/"off" drops the field; any other explicit value enables
// reasoning_summary:"auto"; absent reasoning_effort leaves the request untouched.
func applyCodeBuddyCNReasoning(body []byte) []byte {
	if !gjson.GetBytes(body, "reasoning_effort").Exists() {
		return body
	}
	eff := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String()))
	switch eff {
	case "none", "off", "":
		body, _ = sjson.DeleteBytes(body, "reasoning_effort")
		body, _ = sjson.DeleteBytes(body, "reasoning_summary")
	default:
		body, _ = sjson.DeleteBytes(body, "reasoning_effort")
		body, _ = sjson.SetBytes(body, "reasoning_summary", "auto")
	}
	return body
}

// agentSystemPromptPattern matches CLI agent system prompts that CodeBuddy's
// content filter rejects as sensitive content.
var agentSystemPromptPattern = regexp.MustCompile(`(?i)you are claude code|claude\.?code.+official.+cli|anthropic.+official.+cli|you are (?:cursor|windsurf|cline|aider|continue|copilot|cody)|you are an? (?:ai )?(?:coding |code )?agent|cc_entrypoint\s*=\s*(?:cli|vscode|jetbrains|gui)|claude\.?code.+issues|give feedback.+claude\.?code|you are .{0,30}(?:powerful )?ai agent|orchestration capabilities|OhMyOpenCode|<agent-identity>|<Role>|<Behavior_Instructions>`)

// neutralSystemPrompt replaces detected agent system prompts.
const neutralSystemPrompt = "You are a helpful AI assistant that helps with software engineering tasks."

// applyCodeBuddyCNAgentSystemPrompt replaces agent system prompts in either the
// top-level Anthropic `system` field or the openai `messages` array system role
// with a neutral prompt, leaving legitimate user system prompts untouched.
func applyCodeBuddyCNAgentSystemPrompt(body []byte) []byte {
	if agentSystemPromptPattern.MatchString(string(body)) {
		body = replaceSystemField(body)
		body = replaceSystemMessages(body)
	}
	return body
}

// replaceSystemField handles the Anthropic-style top-level `system` field.
func replaceSystemField(body []byte) []byte {
	sysPath := gjson.GetBytes(body, "system")
	if !sysPath.Exists() {
		return body
	}
	text := systemContentText(sysPath)
	if text == "" {
		return body
	}
	if !agentSystemPromptPattern.MatchString(text) {
		return body
	}
	updated, err := sjson.SetBytes(body, "system", neutralSystemPrompt)
	if err != nil {
		return body
	}
	return updated
}

// replaceSystemMessages handles OpenAI-style system messages in the `messages` array.
func replaceSystemMessages(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || messages.Type != gjson.JSON {
		return body
	}
	changed := false
	var out []any
	messages.ForEach(func(_, item gjson.Result) bool {
		if item.Get("role").String() == "system" {
			text := systemContentText(item.Get("content"))
			if text != "" && agentSystemPromptPattern.MatchString(text) {
				entry := map[string]any{
					"role":    "system",
					"content": neutralSystemPrompt,
				}
				out = append(out, entry)
				changed = true
				return true
			}
		}
		out = append(out, json.RawMessage(item.Raw))
		return true
	})
	if !changed {
		return body
	}
	marshaled, err := json.Marshal(out)
	if err != nil {
		return body
	}
	updated, err := sjson.SetBytes(body, "messages", json.RawMessage(marshaled))
	if err != nil {
		return body
	}
	return updated
}

// systemContentText flattens a system content (string or typed blocks) to text.
func systemContentText(res gjson.Result) string {
	if res.Type == gjson.String {
		return res.String()
	}
	if res.Type == gjson.JSON {
		var parts []string
		res.ForEach(func(_, block gjson.Result) bool {
			if t := strings.TrimSpace(block.Get("text").String()); t != "" {
				parts = append(parts, t)
			}
			return true
		})
		return strings.Join(parts, "\n")
	}
	return ""
}
