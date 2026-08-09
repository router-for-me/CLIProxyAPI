package main

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	claudeVersionPattern       = regexp.MustCompile(`claude-(?:opus|sonnet|haiku)-(\d+)(?:[.-](\d+))?`)
	claudeDatedSnapshotPattern = regexp.MustCompile(`claude-(?:opus|sonnet|haiku)-\d+-\d{8}(?:[^0-9]|$)`)
)

func estimateApproxTokens(text string) int {
	if text == "" {
		return 0
	}
	runes := []rune(text)
	if len(runes) < 5 {
		return max(1, int(math.Ceil(float64(len(runes))/3)))
	}
	var ascii, digits, symbols, nonASCII int
	for _, value := range runes {
		switch {
		case value >= 0x80:
			nonASCII++
		case value >= '0' && value <= '9':
			digits++
		case (value >= '!' && value <= '/') || (value >= ':' && value <= '@') ||
			(value >= '[' && value <= '`') || (value >= '{' && value <= '~'):
			symbols++
		default:
			ascii++
		}
	}
	estimated := int(math.Ceil(float64(ascii)/4.5 + float64(digits)/2 + float64(symbols)/1.5 + float64(nonASCII)/1.5))
	if estimated < 1 {
		return 1
	}
	return estimated
}

func estimateClaudeRequestInputTokens(request *claudeRequest) int {
	if request == nil {
		return 0
	}
	total := estimateClaudeValueTokens(request.System)
	for _, message := range request.Messages {
		total += estimateClaudeValueTokens(message.Content)
	}
	for _, tool := range request.Tools {
		total += estimateApproxTokens(tool.Name)
		total += estimateApproxTokens(tool.Description)
		total += estimateJSONTokens(tool.InputSchema)
	}
	return total
}

func estimateClaudeOutputTokens(blocks []claudeContentBlock) int {
	total := 0
	for _, block := range blocks {
		switch block.Type {
		case "text":
			total += estimateApproxTokens(block.Text)
		case "thinking":
			total += estimateApproxTokens(block.Thinking)
		case "tool_use":
			total += estimateApproxTokens(block.Name)
			total += estimateJSONTokens(block.Input)
		}
	}
	return total
}

func estimateClaudeValueTokens(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case string:
		return estimateApproxTokens(typed)
	case []any:
		total := 0
		for _, item := range typed {
			total += estimateClaudeValueTokens(item)
		}
		return total
	case map[string]any:
		blockType, _ := typed["type"].(string)
		switch blockType {
		case "text", "input_text", "output_text":
			if text, ok := typed["text"].(string); ok {
				return estimateApproxTokens(text)
			}
		case "thinking":
			if thinking, ok := typed["thinking"].(string); ok {
				return estimateApproxTokens(thinking)
			}
		case "tool_use":
			return estimateApproxTokens(stringValue(typed["name"])) + estimateJSONTokens(typed["input"])
		case "tool_result":
			return estimateClaudeValueTokens(typed["content"])
		}
		return estimateJSONTokens(typed)
	default:
		return estimateJSONTokens(typed)
	}
}

func estimateJSONTokens(value any) int {
	if value == nil {
		return 0
	}
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return 0
	}
	return estimateApproxTokens(string(raw))
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func contextWindowTokens(model string) int {
	model = strings.ToLower(model)
	if claudeDatedSnapshotPattern.MatchString(model) {
		return 200_000
	}

	match := claudeVersionPattern.FindStringSubmatch(model)
	if len(match) == 3 {
		major, errMajor := strconv.Atoi(match[1])
		minor := 0
		if match[2] != "" {
			minor, _ = strconv.Atoi(match[2])
		}
		if errMajor == nil && (major > 4 || major == 4 && minor >= 6) {
			return 1_000_000
		}
	}
	return 200_000
}
