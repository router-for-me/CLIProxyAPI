package helps

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

// marshalJSONNoEscape encodes v with HTML escaping disabled so injected text
// containing <, >, or & does not appear in upstream payloads as </>/&.
// JSON parsers handle either form, but unescaped output is more readable in logs.
func marshalJSONNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// promptRulesSnapshot is the immutable runtime view of cfg.PromptRules with each
// strip rule's regex pre-compiled. Concurrent readers obtain the current snapshot
// via an atomic load — never holding a lock on the hot path.
type promptRulesSnapshot struct {
	rules   []config.PromptRule
	regexes []*regexp.Regexp // parallel to rules; nil for non-strip or invalid pattern
}

var promptRulesPtr atomic.Pointer[promptRulesSnapshot]

func init() {
	// Wire SanitizePromptRules → snapshot rebuild so every config load (boot, file
	// watcher, management API persist that re-loads) refreshes the runtime cache.
	config.SetPromptRulesUpdateHook(updatePromptRulesSnapshotInternal)
	// Wire validation predicate so config rejects rules scoped to unknown
	// source formats. The set of valid protocols is owned by this package.
	config.SetPromptRuleProtocolValidator(IsAllowedPromptRuleProtocol)
}

// UpdatePromptRulesSnapshot rebuilds the in-process compiled-regex snapshot from
// the supplied rules. Management handlers call this after mutating cfg.PromptRules
// in place (since those paths bypass LoadConfig).
func UpdatePromptRulesSnapshot(rules []config.PromptRule) {
	updatePromptRulesSnapshotInternal(rules)
}

func updatePromptRulesSnapshotInternal(rules []config.PromptRule) {
	// Deep copy each rule's Models slice so external mutation of the source
	// config (under management's lock) cannot race with readers iterating an
	// older snapshot. Outer slice copy alone is insufficient — the inner
	// Models slice is shared by default.
	deep := make([]config.PromptRule, len(rules))
	for i := range rules {
		r := rules[i]
		if len(r.Models) > 0 {
			r.Models = append([]config.PayloadModelRule(nil), r.Models...)
		}
		deep[i] = r
	}
	snap := &promptRulesSnapshot{
		rules:   deep,
		regexes: make([]*regexp.Regexp, len(deep)),
	}
	for i := range deep {
		if deep[i].Action == config.PromptRuleActionStrip && deep[i].Pattern != "" {
			if re, err := regexp.Compile(deep[i].Pattern); err == nil {
				snap.regexes[i] = re
			}
			// Compile failures are silently skipped at runtime — write paths
			// already validate so this branch is defense-in-depth for SDK
			// programmatic config users (per Codex review §14).
		}
	}
	promptRulesPtr.Store(snap)
}

func loadPromptRulesSnapshot() *promptRulesSnapshot {
	return promptRulesPtr.Load()
}

// ApplyPromptRules mutates a source-format request body according to enabled
// prompt rules. Strip rules run first, then inject. Idempotent via per-rule
// marker substring. Endpoint-scoped: skips /v1/images/* and the responses/compact
// alt path.
//
// sourceFormat is the inbound request's protocol identifier (the value of
// BaseAPIHandler.HandlerType()): "openai", "openai-response", "claude",
// "gemini", or "gemini-cli". Unknown formats are pass-through.
//
// The function reads from a package-level atomic snapshot; callers do not need
// to thread *config.Config through to the handler chokepoint.
func ApplyPromptRules(sourceFormat, model string, rawJSON []byte, requestPath, alt string) []byte {
	if len(rawJSON) == 0 {
		return rawJSON
	}
	if alt == "responses/compact" || isImagesEndpointRequestPath(requestPath) {
		return rawJSON
	}
	snap := loadPromptRulesSnapshot()
	if snap == nil || len(snap.rules) == 0 {
		return rawJSON
	}
	h := promptHandlerForSourceFormat(sourceFormat)
	if h == nil {
		return rawJSON
	}
	candidates := payloadModelCandidates(model, model)
	out := rawJSON

	// Strip pass first so injected content cannot be unintentionally stripped
	// within the same request.
	for i := range snap.rules {
		rule := &snap.rules[i]
		if !rule.Enabled || rule.Action != config.PromptRuleActionStrip {
			continue
		}
		if !promptRuleMatch(rule, sourceFormat, candidates) {
			continue
		}
		re := snap.regexes[i]
		if re == nil {
			continue
		}
		switch rule.Target {
		case config.PromptRuleTargetSystem:
			out = h.StripSystem(out, re)
		case config.PromptRuleTargetUser:
			out = h.StripLastUser(out, re)
		}
	}

	// Inject pass.
	for i := range snap.rules {
		rule := &snap.rules[i]
		if !rule.Enabled || rule.Action != config.PromptRuleActionInject {
			continue
		}
		if !promptRuleMatch(rule, sourceFormat, candidates) {
			continue
		}
		position := rule.Position
		if position == "" {
			position = config.PromptRulePositionAppend
		}
		switch rule.Target {
		case config.PromptRuleTargetSystem:
			out = h.InjectSystem(out, rule.Content, rule.Marker, position)
		case config.PromptRuleTargetUser:
			out = h.InjectLastUser(out, rule.Content, rule.Marker, position)
		}
	}
	return out
}

// promptRuleMatch returns true when the rule's Models scope admits the given
// source format and model. Empty Models slice means match-all — explicitly
// different from payloadModelRulesMatch which returns false on empty.
func promptRuleMatch(rule *config.PromptRule, sourceFormat string, models []string) bool {
	if len(rule.Models) == 0 {
		return true
	}
	if len(models) == 0 {
		return false
	}
	for _, model := range models {
		for j := range rule.Models {
			entry := &rule.Models[j]
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			if ep := strings.TrimSpace(entry.Protocol); ep != "" && sourceFormat != "" && !strings.EqualFold(ep, sourceFormat) {
				continue
			}
			if matchModelPattern(name, model) {
				return true
			}
		}
	}
	return false
}

// promptHandler is the per-source-format dispatch surface. Each implementation
// understands the JSON shape of one inbound request format and applies inject /
// strip operations on system prompt and last natural-language user message.
type promptHandler interface {
	InjectSystem(payload []byte, content, marker, position string) []byte
	StripSystem(payload []byte, re *regexp.Regexp) []byte
	InjectLastUser(payload []byte, content, marker, position string) []byte
	StripLastUser(payload []byte, re *regexp.Regexp) []byte
}

var (
	openaiPromptHandler         promptHandler = &openaiPromptFmt{}
	openaiResponsePromptHandler promptHandler = &openaiResponsePromptFmt{}
	claudePromptHandler         promptHandler = &claudePromptFmt{}
	geminiPromptHandler         promptHandler = newGeminiPromptFmt("")
	geminiCLIPromptHandler      promptHandler = newGeminiPromptFmt("request")
)

// AllowedPromptRuleProtocols is the canonical set of source-format strings that
// PromptRule.Models[].Protocol may scope to. Used by config validation and
// kept in lockstep with the dispatch table below — no aliases.
var AllowedPromptRuleProtocols = []string{
	"openai", "openai-response", "claude", "gemini", "gemini-cli",
}

// IsAllowedPromptRuleProtocol returns true when p is an empty string ("any
// source format") or matches one of the accepted source-format strings.
func IsAllowedPromptRuleProtocol(p string) bool {
	if p == "" {
		return true
	}
	for _, allowed := range AllowedPromptRuleProtocols {
		if allowed == p {
			return true
		}
	}
	return false
}

func promptHandlerForSourceFormat(sourceFormat string) promptHandler {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "openai":
		return openaiPromptHandler
	case "openai-response":
		return openaiResponsePromptHandler
	case "claude":
		return claudePromptHandler
	case "gemini":
		return geminiPromptHandler
	case "gemini-cli":
		return geminiCLIPromptHandler
	default:
		return nil
	}
}

// applyPosition returns the result of inserting add into base at the configured
// position. Used for inject mutations on string-shaped targets.
func applyPosition(base, add, position string) string {
	if position == config.PromptRulePositionPrepend {
		return add + base
	}
	return base + add
}

// containsMarker returns true when marker is non-empty AND appears in text.
// An empty marker is invalid at validation time, so this returns false to err on
// the side of injecting (which a downstream test can flag).
func containsMarker(text, marker string) bool {
	if marker == "" {
		return false
	}
	return strings.Contains(text, marker)
}

// hasNonEmptyText returns true when the given gjson.Result has a string-typed
// child at field whose trimmed value is non-empty. Used by per-format locators
// to decide whether a content block / part counts as natural-language text.
func hasNonEmptyText(r gjson.Result, field string) bool {
	t := r.Get(field)
	if !t.Exists() || t.Type != gjson.String {
		return false
	}
	return strings.TrimSpace(t.String()) != ""
}

// isImagesEndpointRequestPath is provided by payload_helpers.go (in this same
// package) as part of the disable-image-generation tri-state feature. We share
// that helper rather than re-declaring it locally.
