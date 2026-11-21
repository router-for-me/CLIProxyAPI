package helps

import (
	"iter"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// If a matching selector can write a protected field, omit optional server-private
// state before payload evaluation. Body predicates can depend on earlier rules;
// do not speculate about their outcome or evaluate the rules twice.
func CopilotPayloadMayWrite(cfg *config.Config, model, requested, from, to string, headers http.Header, fields ...string) bool {
	if cfg == nil {
		return false
	}
	var models []string
	matches := func(entries []config.PayloadModelRule, paths iter.Seq[string]) bool {
		for path := range paths {
			if !slices.ContainsFunc(fields, func(field string) bool { return payloadRuleTargetsPath(strings.TrimSpace(path), field) }) {
				continue
			}
			if models == nil {
				models = payloadModelCandidates(model, requested)
			}
			for _, m := range models {
				for _, entry := range entries {
					if payloadModelSelectorMatches(entry, m, to, from, headers) {
						return true
					}
				}
			}
		}
		return false
	}
	p := &cfg.Payload
	for _, rules := range [][]config.PayloadRule{p.Default, p.DefaultRaw, p.Override, p.OverrideRaw} {
		for _, rule := range rules {
			if matches(rule.Models, maps.Keys(rule.Params)) {
				return true
			}
		}
	}
	for _, rule := range p.Replace {
		if matches(rule.Models, slices.Values(rule.Target.Paths)) {
			return true
		}
	}
	for _, rule := range p.Filter {
		if matches(rule.Models, slices.Values(rule.Params)) {
			return true
		}
	}
	return false
}

// Only move a leading prompt known to have been converted into developer input.
// Explicit Responses instructions/developer input and mid-turn messages stay put.
func NormalizeCopilotSystemPrompt(doc *ojson.Doc, route copilot.Route, from translator.Format, source ojson.Result) {
	if from != translator.FormatOpenAI && from != translator.FormatClaude {
		return
	}
	explicit := source.Get("instructions")
	if explicit.Exists() {
		_ = doc.SetRaw("instructions", explicit.Raw)
	} else if doc.Get("instructions").String() == "" {
		_ = doc.Delete("instructions")
	}
	if route.SystemPrompt == "" {
		return
	}
	count := 0
	if from == translator.FormatClaude {
		system := source.Get("system")
		hasText := func(s string) bool { return s != "" && !util.IsClaudeCodeAttributionSystemText(s) }
		if system.Type == ojson.String && hasText(system.String()) {
			count = 1
		}
		for _, part := range system.Array() {
			if part.Get("type").String() == "text" && hasText(part.Get("text").String()) {
				count = 1
				break
			}
		}
	} else {
		for _, msg := range source.Get("messages").Array() {
			if msg.Get("role").String() != "system" {
				break
			}
			count++
		}
	}
	if count == 0 {
		return
	}
	var texts []string
	for i := range count {
		msg := doc.Get("input." + strconv.Itoa(i))
		if msg.Get("role").String() != "developer" || msg.Get("type").String() != "message" {
			return
		}
		for _, part := range msg.Get("content").Array() {
			if part.Get("type").String() != "input_text" {
				return
			}
			texts = append(texts, part.Get("text").String())
		}
	}
	if route.SystemPrompt == "instructions" && !explicit.Exists() && len(texts) > 0 {
		_ = doc.Set("instructions", strings.Join(texts, "\n\n"))
		for range count {
			_ = doc.Delete("input.0")
		}
		return
	}
	for i := range count {
		_ = doc.Set("input."+strconv.Itoa(i)+".role", "system")
	}
}
