package payloadcompress

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// MCP tool-description compression, ported from upstream OmniRoute
// open-sse/mcp-server/descriptionCompressor.ts. Upstream rewrites the
// description of every MCP tool it serves at full caveman intensity and keeps
// the toggle independent of the compression engines
// (services/compression/types.ts:429 mcpDescriptionCompressionEnabled), so it
// runs whatever CompressionConfig.enabled says. This port has no MCP server of
// its own, so the same rewrite applies to the tool descriptions in the body,
// and it is opt-in because a client-authored description can carry grammar
// markers that filler removal must not touch.

var (
	mcpDescriptionMultiSpace = regexp.MustCompile(`[ \t]{2,}`)
	mcpDescriptionPunctSpace = regexp.MustCompile(`[ \t]([,.;:!?])`)
	mcpDescriptionBlankRuns  = regexp.MustCompile(`\n{3,}`)
)

// ApplyMCPDescriptionCompression rewrites tool descriptions in place and reports
// how many it shortened. Both the flat {name, description} shape and the OpenAI
// {type:"function", function:{description}} shape are covered.
func ApplyMCPDescriptionCompression(doc *ojson.Doc) int {
	if doc == nil {
		return 0
	}
	tools := doc.Get("tools")
	if !tools.IsArray() {
		return 0
	}
	changed := 0
	for i, tool := range tools.Array() {
		path := "tools." + strconv.Itoa(i) + ".description"
		desc := tool.Get("description")
		if tool.Get("function").IsObject() {
			path = "tools." + strconv.Itoa(i) + ".function.description"
			desc = tool.Get("function.description")
		}
		if desc.Type != ojson.String {
			continue
		}
		out, ok := compressMCPDescription(desc.String())
		if !ok {
			continue
		}
		if err := doc.Set(path, out); err != nil {
			continue
		}
		changed++
	}
	return changed
}

// compressMCPDescription applies upstream compressMcpDescription: the full
// caveman rule set, then whitespace and capitalization cleanup, keeping
// protected spans (code, URLs, error output) byte-identical. It reports false
// when the rewrite is not shorter, because a longer description is never worth
// sending.
func compressMCPDescription(description string) (string, bool) {
	if description == "" {
		return description, false
	}
	text, blocks := extractPreservedBlocks(description, nil)
	for _, rule := range mcpDescriptionRules() {
		text = rule.apply(text)
	}
	// Upstream also recapitalizes sentence starts. That step is dropped here: it
	// rewrites embedded notation such as a grammar's "start: patch".
	text = mcpDescriptionBlankRuns.ReplaceAllString(
		mcpDescriptionPunctSpace.ReplaceAllString(
			mcpDescriptionMultiSpace.ReplaceAllString(text, " "), "$1"), "\n\n")
	out := strings.TrimSpace(restorePreservedBlocks(text, blocks))
	if out == description || len(out) >= len(description) {
		return description, false
	}
	return out, true
}

// mcpDescriptionRules is upstream getRulesForContext("all", "full"): every
// English rule written for any role, at full intensity.
func mcpDescriptionRules() []cavemanRule {
	rules := packRulesFor("en")
	rank := levelRank("full")
	out := make([]cavemanRule, 0, len(rules))
	for _, rule := range rules {
		if rule.context == "all" && levelRank(rule.level) <= rank {
			out = append(out, rule)
		}
	}
	return out
}
