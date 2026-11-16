package helps

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// replacePayloadUserTextDoc edits only existing protocol-defined user text fields.
// Paths are collected before writes, so no result views survive a document mutation.
func replacePayloadUserTextDoc(doc *ojson.Doc, protocol, root string, rule *config.PayloadReplaceRule, noteWrite func(string)) bool {
	selected := make(map[int]bool, len(rule.Target.Positions))
	for _, position := range rule.Target.Positions {
		selected[position] = true
	}
	var paths []string
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	var arrayPath string
	switch protocol {
	case "openai", "claude":
		arrayPath = buildPayloadPath(root, "messages")
	case "codex", "responses", "response", "openai-response", "openai-responses", "meta":
		arrayPath = buildPayloadPath(root, "input")
		if doc.Get(arrayPath).Type == ojson.String {
			if rule.Target.All || selected[1] {
				paths = append(paths, arrayPath)
			}
		}
	case "gemini", "antigravity":
		arrayPath = buildPayloadPath(root, "contents")
	default:
		return false
	}
	array := doc.Get(arrayPath)
	if array.IsArray() {
		position := 0
		for index, message := range array.Array() {
			if !message.IsObject() || message.Get("role").String() != "user" {
				continue
			}
			if protocol != "openai" && protocol != "claude" && protocol != "gemini" && protocol != "antigravity" {
				if typ := message.Get("type"); typ.Exists() && (typ.Type != ojson.String || typ.String() != "message") {
					continue
				}
			}
			position++
			if !rule.Target.All && !selected[position] {
				continue
			}
			messagePath := arrayPath + "." + strconv.Itoa(index)
			if protocol == "gemini" || protocol == "antigravity" {
				parts := message.Get("parts")
				if !parts.IsArray() {
					continue
				}
				for partIndex, part := range parts.Array() {
					if part.IsObject() && part.Get("text").Type == ojson.String && !part.Get("thought").Bool() &&
						!part.Get("functionCall").Exists() && !part.Get("functionResponse").Exists() {
						paths = append(paths, messagePath+".parts."+strconv.Itoa(partIndex)+".text")
					}
				}
				continue
			}
			content := message.Get("content")
			if content.Type == ojson.String {
				paths = append(paths, messagePath+".content")
			} else if content.IsArray() {
				for blockIndex, block := range content.Array() {
					if !block.IsObject() || block.Get("text").Type != ojson.String || block.Get("thought").Bool() {
						continue
					}
					typ := block.Get("type").String()
					if typ == "text" || (typ == "input_text" && protocol != "openai" && protocol != "claude") {
						paths = append(paths, messagePath+".content."+strconv.Itoa(blockIndex)+".text")
					}
				}
			}
		}
	}
	mutated := false
	for _, path := range paths {
		text := doc.Get(path).String()
		updated := text
		for _, replacement := range rule.Replacements {
			for from, to := range replacement {
				updated = strings.ReplaceAll(updated, from, to)
			}
		}
		if updated == text {
			continue
		}
		if errSet := doc.Set(path, updated); errSet != nil {
			continue
		}
		mutated = true
		noteWrite(path)
	}
	return mutated
}
