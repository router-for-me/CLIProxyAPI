package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

var claude55Model = regexp.MustCompile(`^claude-(opus|sonnet)-5-5(?:-(high|reasoning))?(?:-[0-9]{8})?(?:\[[^\[\]]+\])?$`)

// claudeFableForcedToolChoiceModel matches Claude Fable 5.1 and 5.2 (dashed or
// dotted version), which reject forced tool use like Claude 5.5. Fable 5
// accepts it.
var claudeFableForcedToolChoiceModel = regexp.MustCompile(`^claude-fable-5[-.][12](?:-[0-9a-z]+)*(?:\[[^\[\]]+\])?$`)

// NormalizeClaudeToolChoice downgrades forced tool use on Claude 5.5 and
// Fable 5.1 and 5.2 to auto.
// Use the routed upstream model, not the payload model, and run before thinking
// preparation so forced-mode cleanup cannot discard thinking or signed history.
// Delegated providers keep their own tool-choice contract. User payload rules
// must run afterwards and remain authoritative.
func NormalizeClaudeToolChoice(provider, model string, payload []byte) []byte {
	if len(payload) == 0 || provider != "claude" {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !NormalizeClaudeToolChoiceDoc(provider, model, doc) {
		return payload
	}
	return doc.Bytes()
}

// NormalizeClaudeToolChoiceDoc downgrades forced tool use on Claude 5.5 and
// Fable 5.1 and 5.2 to auto in place on doc.
func NormalizeClaudeToolChoiceDoc(provider, model string, doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 || provider != "claude" {
		return false
	}
	model = strings.ToLower(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	// Provider-qualified routes (anthropic/claude-fable-5-1) name the same model;
	// strip the prefix the way the executor's model classifiers do.
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	if !claude55Model.MatchString(model) && !claudeFableForcedToolChoiceModel.MatchString(model) {
		return false
	}
	switch doc.Get("tool_choice.type").String() {
	case "any", "tool":
		_ = doc.Set("tool_choice.type", "auto")
		_ = util.DeleteJSONDocIfExists(doc, "tool_choice.name")
		return true
	}
	return false
}
