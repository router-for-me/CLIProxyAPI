package helps

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// replacePayloadTextDoc edits existing strings selected by paths or protocol-defined role text.
// Changed values are collected before writes so traversal views remain valid.
func replacePayloadTextDoc(doc *ojson.Doc, protocol, root string, rule *config.PayloadReplaceRule, ruleIndex int, noteWrite func(string)) bool {
	pathTarget := len(rule.Target.Paths) > 0
	var arrayPath, systemPath string
	var gemini, responses bool
	if !pathTarget {
		switch rule.Target.Role {
		case "system", "developer", "user":
		default:
			return false
		}
		protocol = strings.ToLower(strings.TrimSpace(protocol))
		switch protocol {
		case "openai":
			arrayPath = buildPayloadPath(root, "messages")
		case "claude":
			arrayPath = buildPayloadPath(root, "messages")
			systemPath = buildPayloadPath(root, "system")
		case "codex", "responses", "response", "openai-response", "openai-responses", "meta":
			arrayPath = buildPayloadPath(root, "input")
			systemPath = buildPayloadPath(root, "instructions")
			responses = true
		case "gemini", "antigravity":
			arrayPath = buildPayloadPath(root, "contents")
			systemPath = buildPayloadPath(root, "systemInstruction")
			if !doc.Get(systemPath).Exists() {
				systemPath = buildPayloadPath(root, "system_instruction")
			}
			gemini = true
		default:
			return false
		}
	}
	var selected map[int]bool
	lastPosition := 0
	if !pathTarget && !rule.Target.All {
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
	replaceText := func(text string) string {
		compiled, ok := compileSteps()
		if !ok {
			return text
		}
		updated := text
		for _, step := range compiled {
			if step.re != nil {
				updated = step.re.ReplaceAllString(updated, step.to)
			} else {
				updated = strings.ReplaceAll(updated, step.from, step.to)
			}
		}
		return updated
	}
	type textChange struct{ path, value string }
	var changes []textChange
	collectChange := func(text, basePath string, messageIndex, blockIndex int) {
		updated := replaceText(text)
		if updated == text {
			return
		}
		// Build paths only after all replacements produce an actual text change.
		path := basePath
		if messageIndex >= 0 {
			if gemini {
				path = basePath + "." + strconv.Itoa(messageIndex) + ".parts." + strconv.Itoa(blockIndex) + ".text"
			} else if blockIndex >= 0 {
				path = basePath + "." + strconv.Itoa(messageIndex) + ".content." + strconv.Itoa(blockIndex) + ".text"
			} else {
				path = basePath + "." + strconv.Itoa(messageIndex) + ".content"
			}
		} else if blockIndex >= 0 {
			if gemini {
				path = basePath + ".parts." + strconv.Itoa(blockIndex) + ".text"
			} else {
				path = basePath + "." + strconv.Itoa(blockIndex) + ".text"
			}
		}
		changes = append(changes, textChange{path: path, value: updated})
	}
	collectContent := func(content ojson.Result, basePath string, messageIndex int) {
		if content.Type == ojson.String {
			collectChange(content.String(), basePath, messageIndex, -1)
		} else if content.IsArray() {
			content.ForEach(func(blockIndex, block ojson.Result) bool {
				text := block.Get("text")
				if !block.IsObject() || text.Type != ojson.String || block.Get("thought").Bool() {
					return true
				}
				typ := block.Get("type").String()
				if typ == "text" || (responses && typ == "input_text") {
					collectChange(text.String(), basePath, messageIndex, int(blockIndex.Int()))
				}
				return true
			})
		}
	}
	collectParts := func(parts ojson.Result, basePath string, messageIndex int) {
		if parts.IsArray() {
			parts.ForEach(func(partIndex, part ojson.Result) bool {
				text := part.Get("text")
				if part.IsObject() && text.Type == ojson.String && !part.Get("thought").Bool() &&
					!part.Get("functionCall").Exists() && !part.Get("functionResponse").Exists() {
					collectChange(text.String(), basePath, messageIndex, int(partIndex.Int()))
				}
				return true
			})
		}
	}
	if pathTarget {
		// Resolve against one pre-write view, with indexes relative to its raw text.
		source := doc.Get("@this")
		source.Index = 0
		seen := make(map[int]struct{})
		collectPathChange := func(text ojson.Result, steps []payloadReplacePathStep) {
			// Computed strings can reuse a source offset without being source nodes.
			if text.Type != ojson.String || !payloadReplaceSourceChild(source, text) {
				return
			}
			// Snapshot offsets identify nodes without constructing write paths.
			if _, exists := seen[text.Index]; exists {
				return
			}
			original := text.String()
			updated := replaceText(original)
			if updated == original {
				seen[text.Index] = struct{}{}
				return
			}
			if writePath := payloadReplaceWritePath(steps); writePath != "" {
				// Unwritable selections must not mask a later concrete path.
				seen[text.Index] = struct{}{}
				changes = append(changes, textChange{path: writePath, value: updated})
			}
		}
		for _, path := range rule.Target.Paths {
			visitPayloadReplacePath(source, buildPayloadPath(root, path), collectPathChange)
		}
	} else {
		position := 0
		// Top-level instructions precede explicit system messages and count as one message.
		if rule.Target.Role == "system" && systemPath != "" {
			if system := doc.Get(systemPath); system.Exists() {
				position++
				if rule.Target.All || selected[position] {
					if gemini {
						collectParts(system.Get("parts"), systemPath, -1)
					} else {
						collectContent(system, systemPath, -1)
					}
				}
			}
		}
		array := doc.Get(arrayPath)
		if responses && array.Type == ojson.String {
			if rule.Target.Role == "user" && (rule.Target.All || selected[1]) {
				collectChange(array.String(), arrayPath, -1, -1)
			}
		} else if array.IsArray() && (rule.Target.All || position < lastPosition) {
			array.ForEach(func(index, message ojson.Result) bool {
				if !message.IsObject() || message.Get("role").String() != rule.Target.Role {
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
					collectParts(message.Get("parts"), arrayPath, messageIndex)
				} else {
					collectContent(message.Get("content"), arrayPath, messageIndex)
				}
				return keepScanning
			})
		}
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
