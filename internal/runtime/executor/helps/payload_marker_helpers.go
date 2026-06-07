package helps

import "bytes"

var (
	claudeToolUseMarker    = []byte(`"tool_use"`)
	claudeToolResultMarker = []byte(`"tool_result"`)
)

// HasClaudeToolUseOrResultMarkers reports whether a payload might contain Claude
// tool_use/tool_result blocks. It intentionally uses a cheap byte scan so
// callers can skip expensive JSON repair work on obviously unrelated requests.
func HasClaudeToolUseOrResultMarkers(body []byte) bool {
	return bytes.Contains(body, claudeToolUseMarker) || bytes.Contains(body, claudeToolResultMarker)
}

// HasClaudeToolResultMarker reports whether a payload might contain a Claude
// tool_result block.
func HasClaudeToolResultMarker(body []byte) bool {
	return bytes.Contains(body, claudeToolResultMarker)
}
