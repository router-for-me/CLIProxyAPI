package executor

import (
	"bytes"
	"encoding/json"
	"strings"
)

const codexIntermediaryUpdatesHeading = "## Intermediary updates"

func stripCodexIntermediaryUpdatesFromPayload(body []byte) []byte {
	if !bytes.Contains(body, []byte(codexIntermediaryUpdatesHeading)) {
		return body
	}

	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return body
	}

	instructions, ok := payload["instructions"].(string)
	if !ok {
		return body
	}
	stripped, changed := stripCodexIntermediaryUpdatesText(instructions)
	if !changed {
		return body
	}
	payload["instructions"] = stripped

	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return body
	}
	return bytes.TrimSpace(out.Bytes())
}

func stripCodexIntermediaryUpdatesText(text string) (string, bool) {
	if !strings.Contains(text, codexIntermediaryUpdatesHeading) {
		return text, false
	}

	lines := strings.SplitAfter(text, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	changed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		if !skipping && trimmed == codexIntermediaryUpdatesHeading {
			skipping = true
			changed = true
			continue
		}

		if skipping {
			if isCodexIntermediaryUpdatesStopHeading(trimmed) {
				skipping = false
				out = append(out, line)
			}
			continue
		}

		out = append(out, line)
	}

	if !changed {
		return text, false
	}
	return strings.Join(out, ""), true
}

func isCodexIntermediaryUpdatesStopHeading(trimmedLine string) bool {
	if trimmedLine == codexIntermediaryUpdatesHeading {
		return false
	}

	headingLevel := 0
	for headingLevel < len(trimmedLine) && headingLevel < 6 && trimmedLine[headingLevel] == '#' {
		headingLevel++
	}
	return headingLevel > 0 && len(trimmedLine) > headingLevel && trimmedLine[headingLevel] == ' '
}
