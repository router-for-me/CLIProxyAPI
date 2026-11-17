package helps

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// replacePayloadUserTextDoc edits only existing protocol-defined user text fields.
// Changed values are collected before writes so traversal views remain valid.
func replacePayloadUserTextDoc(doc *ojson.Doc, protocol, root string, rule *config.PayloadReplaceRule, ruleIndex int, noteWrite func(string)) bool {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	var arrayPath string
	var gemini, responses bool
	switch protocol {
	case "openai", "claude":
		arrayPath = buildPayloadPath(root, "messages")
	case "codex", "responses", "response", "openai-response", "openai-responses", "meta":
		arrayPath = buildPayloadPath(root, "input")
		responses = true
	case "gemini", "antigravity":
		arrayPath = buildPayloadPath(root, "contents")
		gemini = true
	default:
		return false
	}
	var selected map[int]bool
	lastPosition := 0
	if !rule.Target.All {
		selected = make(map[int]bool, len(rule.Target.Positions))
		for _, position := range rule.Target.Positions {
			selected[position] = true
			if position > lastPosition {
				lastPosition = position
			}
		}
	}
	if len(rule.Replacements) == 0 {
		return false
	}
	type compiledReplacement struct {
		from, to string
		re       *regexp.Regexp
	}
	var steps []compiledReplacement
	compileSteps := func() ([]compiledReplacement, bool) {
		if steps != nil {
			return steps, true
		}
		built := make([]compiledReplacement, 0, len(rule.Replacements))
		for i, replacement := range rule.Replacements {
			source, target, isRegex, errResolve := replacement.Resolve()
			if errResolve != nil {
				return nil, false
			}
			step := compiledReplacement{from: source, to: target}
			if isRegex {
				re, errCompile := regexp.Compile(source)
				if source == "" || errCompile != nil {
					log.Warnf("payload.replace rule %d replacement %d skipped: invalid regex", ruleIndex+1, i+1)
					continue
				}
				step.re = re
			}
			built = append(built, step)
		}
		steps = built
		return steps, true
	}
	type textChange struct{ path, value string }
	var changes []textChange
	collectChange := func(text string, messageIndex, blockIndex int) {
		compiled, ok := compileSteps()
		if !ok {
			return
		}
		updated := text
		for _, step := range compiled {
			if step.re != nil {
				updated = step.re.ReplaceAllString(updated, step.to)
			} else {
				updated = strings.ReplaceAll(updated, step.from, step.to)
			}
		}
		if updated == text {
			return
		}
		// Build paths only for changed text; no second document lookup is needed.
		path := arrayPath
		if messageIndex >= 0 {
			if gemini {
				path = arrayPath + "." + strconv.Itoa(messageIndex) + ".parts." + strconv.Itoa(blockIndex) + ".text"
			} else if blockIndex >= 0 {
				path = arrayPath + "." + strconv.Itoa(messageIndex) + ".content." + strconv.Itoa(blockIndex) + ".text"
			} else {
				path = arrayPath + "." + strconv.Itoa(messageIndex) + ".content"
			}
		}
		changes = append(changes, textChange{path: path, value: updated})
	}
	array := doc.Get(arrayPath)
	if responses && array.Type == ojson.String {
		if rule.Target.All || selected[1] {
			collectChange(array.String(), -1, -1)
		}
	} else if array.IsArray() {
		position := 0
		array.ForEach(func(index, message ojson.Result) bool {
			if !message.IsObject() || message.Get("role").String() != "user" {
				return true
			}
			if responses {
				if typ := message.Get("type"); typ.Exists() && (typ.Type != ojson.String || typ.String() != "message") {
					return true
				}
			}
			position++
			keepScanning := rule.Target.All || position < lastPosition
			if !rule.Target.All && !selected[position] {
				return keepScanning
			}
			messageIndex := int(index.Int())
			if gemini {
				if parts := message.Get("parts"); parts.IsArray() {
					parts.ForEach(func(partIndex, part ojson.Result) bool {
						text := part.Get("text")
						if part.IsObject() && text.Type == ojson.String && !part.Get("thought").Bool() &&
							!part.Get("functionCall").Exists() && !part.Get("functionResponse").Exists() {
							collectChange(text.String(), messageIndex, int(partIndex.Int()))
						}
						return true
					})
				}
				return keepScanning
			}
			content := message.Get("content")
			if content.Type == ojson.String {
				collectChange(content.String(), messageIndex, -1)
			} else if content.IsArray() {
				content.ForEach(func(blockIndex, block ojson.Result) bool {
					text := block.Get("text")
					if !block.IsObject() || text.Type != ojson.String || block.Get("thought").Bool() {
						return true
					}
					typ := block.Get("type").String()
					if typ == "text" || (typ == "input_text" && responses) {
						collectChange(text.String(), messageIndex, int(blockIndex.Int()))
					}
					return true
				})
			}
			return keepScanning
		})
	}
	mutated := false
	for _, change := range changes {
		if errSet := doc.Set(change.path, change.value); errSet != nil {
			continue
		}
		mutated = true
		noteWrite(change.path)
	}
	return mutated
}
