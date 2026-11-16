package helps

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// replacePayloadUserTextDoc edits only existing protocol-defined user text fields.
// Changed values are collected before writes so traversal views remain valid.
func replacePayloadUserTextDoc(doc *ojson.Doc, protocol, root string, rule *config.PayloadReplaceRule, noteWrite func(string)) bool {
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
	type textChange struct{ path, value string }
	var changes []textChange
	collectChange := func(text string, messageIndex, blockIndex int) {
		updated := text
		for _, replacement := range rule.Replacements {
			for from, to := range replacement {
				updated = strings.ReplaceAll(updated, from, to)
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
