package llmreqlog

import _ "embed"

//go:embed page.html
var pageHTML []byte

// PageHTML returns the standalone LLM request log page.
func PageHTML() []byte {
	return pageHTML
}
