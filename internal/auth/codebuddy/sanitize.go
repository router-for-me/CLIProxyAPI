package codebuddy

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SystemPromptBlacklistRule is one system prompt rewrite rule.
//
// The CodeBuddy upstream content filter rejects system prompts containing
// certain vendor identity phrases (finish_reason=content_filter). Rules were
// derived empirically from rejected/accepted prompt pairs; a match is rewritten
// with Replace (an empty string deletes the match).
//
// Find matches either a literal substring (IsRegex=false) or a regular
// expression (IsRegex=true). Replace is always a literal replacement.
type SystemPromptBlacklistRule struct {
	Find    string
	Replace string
	IsRegex bool
}

// DefaultSystemPromptBlacklist returns the empirically derived default rule
// set. Only the specific trigger sentences are rewritten; other occurrences of
// the vendor names are left untouched to avoid over-rewriting.
func DefaultSystemPromptBlacklist() []SystemPromptBlacklistRule {
	return []SystemPromptBlacklistRule{
		{Find: `You are Claude Code, Anthropic's official CLI for Claude\.`, Replace: "", IsRegex: true},
		{Find: ` Codex CLI is an open source project led by OpenAI.`, Replace: "", IsRegex: false},
		{Find: "Codex CLI", Replace: "workbuddy", IsRegex: false},
		{Find: "PRs", Replace: "PR", IsRegex: false},
	}
}

// SanitizeForContentFilter rewrites system messages in an OpenAI chat
// completions payload according to the blacklist rules and returns the updated
// body plus whether any change was made. String contents and the OpenAI
// multi-part content array format are both supported. Nil rules fall back to
// DefaultSystemPromptBlacklist.
func SanitizeForContentFilter(body []byte, rules []SystemPromptBlacklistRule) ([]byte, bool, error) {
	if len(rules) == 0 {
		rules = DefaultSystemPromptBlacklist()
	}
	compiled, err := compileBlacklistRules(rules)
	if err != nil {
		return body, false, err
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, false, nil
	}

	changed := false
	for i, msg := range messages.Array() {
		if !strings.EqualFold(msg.Get("role").String(), "system") {
			continue
		}
		content := msg.Get("content")
		path := "messages." + strconv.Itoa(i) + ".content"

		if content.Type == gjson.String {
			cleaned := applyBlacklistRules(compiled, content.String())
			if cleaned != content.String() {
				updated, errSet := sjson.SetBytes(body, path, cleaned)
				if errSet != nil {
					return body, changed, errSet
				}
				body = updated
				changed = true
			}
			continue
		}

		if content.IsArray() {
			raw, errDelete := sjson.DeleteBytes(body, path)
			if errDelete != nil {
				return body, changed, errDelete
			}
			parts := make([]map[string]any, 0, len(content.Array()))
			localChanged := false
			for _, part := range content.Array() {
				p := map[string]any{}
				for k, v := range part.Map() {
					if k == "text" && v.Type == gjson.String {
						cleaned := applyBlacklistRules(compiled, v.String())
						if cleaned != v.String() {
							localChanged = true
						}
						p["text"] = cleaned
					} else {
						p[k] = v.Value()
					}
				}
				parts = append(parts, p)
			}
			if localChanged {
				updated, errSet := sjson.SetBytes(raw, path, parts)
				if errSet != nil {
					return body, changed, errSet
				}
				body = updated
				changed = true
			}
		}
	}

	return body, changed, nil
}

type compiledRule struct {
	rule  SystemPromptBlacklistRule
	regex *regexp.Regexp
}

func compileBlacklistRules(rules []SystemPromptBlacklistRule) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		cr := compiledRule{rule: r}
		if r.IsRegex {
			re, err := regexp.Compile(r.Find)
			if err != nil {
				return nil, err
			}
			cr.regex = re
		}
		out = append(out, cr)
	}
	return out, nil
}

func applyBlacklistRules(rules []compiledRule, text string) string {
	out := text
	for _, r := range rules {
		if r.rule.IsRegex {
			out = r.regex.ReplaceAllString(out, r.rule.Replace)
			continue
		}
		if strings.Contains(out, r.rule.Find) {
			out = strings.ReplaceAll(out, r.rule.Find, r.rule.Replace)
		}
	}
	// Trim leading/trailing whitespace only; internal structure is preserved.
	return strings.TrimSpace(out)
}
