package payloadcompress

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Upstream MCP accessibility defaults (OmniRoute engines/mcpAccessibility/constants.ts).
const (
	mcpDefaultMaxTextChars       = 50000
	mcpDefaultCollapseThreshold  = 30
	mcpDefaultCollapseKeepHead   = 10
	mcpDefaultCollapseKeepTail   = 5
	mcpDefaultMinLengthToProcess = 2000
	mcpAccessibilityTailReserve  = 300
	// A smaller budget would leave no head after the tail reserve.
	mcpMinMaxTextChars = mcpAccessibilityTailReserve * 2
)

var (
	// A sibling line is an indented "- role" accessibility node header.
	mcpSiblingPattern = regexp.MustCompile(`^(\s*)-\s*([a-zA-Z]+)\b`)

	// Per-line noise matchers. Matched lines are deleted outright so they cannot split sibling runs.
	mcpNoisePatterns = []*regexp.Regexp{
		regexp.MustCompile(`^\s*-\s*generic:?\s*$`),
		regexp.MustCompile(`^\s*-\s*text:\s*""\s*$`),
	}

	// ref anchors are how an agent clicks elements, so collapse must preserve them.
	mcpRefPattern = regexp.MustCompile(`\[ref=e\d+\]`)
)

// mcpKeepValue resolves a keep knob: explicit 0 keeps no siblings, an omitted
// field falls back to the upstream default, and negatives clamp to 0.
func mcpKeepValue(v int, explicit bool, def int) int {
	if v < 0 {
		return 0
	}
	if explicit || v > 0 {
		return v
	}
	return def
}

// mcpAccessibilityEngine compresses MCP accessibility trees: drops noise lines,
// collapses long runs of repeated sibling roles, then clamps very large results.
type mcpAccessibilityEngine struct{ opts StepOptions }

func (mcpAccessibilityEngine) name() string { return EngineMCPAccessibility }

func (e mcpAccessibilityEngine) compress(text string) (string, bool) {
	minLength := e.opts.McpMinLength
	if minLength <= 0 {
		minLength = mcpDefaultMinLengthToProcess
	}
	if charLen(text) < minLength {
		return text, false
	}

	threshold := e.opts.McpCollapseThreshold
	if threshold <= 0 {
		threshold = mcpDefaultCollapseThreshold
	}
	keepHead := mcpKeepValue(e.opts.McpCollapseKeepHead, e.opts.mcpKeepHeadSet, mcpDefaultCollapseKeepHead)
	keepTail := mcpKeepValue(e.opts.McpCollapseKeepTail, e.opts.mcpKeepTailSet, mcpDefaultCollapseKeepTail)
	maxChars := e.opts.McpMaxChars
	if maxChars <= 0 {
		maxChars = mcpDefaultMaxTextChars
	}

	// JSON-aware path: a valid JSON tool result must stay valid. Parse the RAW
	// text before the line pipeline, which would otherwise corrupt it, and fall
	// back to the bounded preview envelope when the content cannot shrink.
	if charLen(text) > maxChars && json.Valid([]byte(text)) {
		if truncated, ok := mcpTruncateJSON(text, maxChars); ok {
			return truncated, truncated != text
		}
		return mcpBuildJSONEnvelope(text, maxChars), true
	}

	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !isMCPNoiseLine(line) {
			kept = append(kept, line)
		}
	}
	out := collapseRepeated(strings.Join(kept, "\n"), threshold, keepHead, keepTail)

	if charLen(out) > maxChars {
		// Clamp to >= 0: a maxTextChars below the tail reservation would otherwise
		// make the head size negative and wrongly keep a tail fragment instead.
		headSize := maxChars - mcpAccessibilityTailReserve
		if headSize < 0 {
			headSize = 0
		}
		head := string([]rune(out)[:headSize])
		omitted := charLen(out) - charLen(head)
		out = fmt.Sprintf("%s\n\n... [truncated %d chars by OmniRoute MCP filter. %s",
			head, omitted, mcpTruncationHint)
	}
	return out, out != text
}

func isMCPNoiseLine(line string) bool {
	for _, p := range mcpNoisePatterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

// collapseRepeated replaces runs of at least threshold same-role sibling nodes with
// a notice, keeping keepHead leading and keepTail trailing nodes. Like upstream, a
// run continues across child lines and blank lines but not across another role.
func collapseRepeated(text string, threshold, keepHead, keepTail int) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		m := mcpSiblingPattern.FindStringSubmatch(lines[i])
		if m == nil {
			out = append(out, lines[i])
			i++
			continue
		}
		indent, role := m[1], m[2]
		j := i
		for j < len(lines) {
			ln := lines[j]
			mm := mcpSiblingPattern.FindStringSubmatch(ln)
			if mm != nil && mm[1] == indent && mm[2] == role {
				j++
				continue
			}
			if strings.HasPrefix(ln, indent+" ") || strings.HasPrefix(ln, indent+"\t") {
				j++
				continue
			}
			// Noise removal can leave blank lines; they must not break a run.
			if strings.TrimSpace(ln) == "" {
				j++
				continue
			}
			break
		}
		groupLen := j - i
		if groupLen >= threshold {
			if keepHead < 0 {
				keepHead = 0
			}
			if keepTail < 0 {
				keepTail = 0
			}
			// Collect sibling starts inside the run so slices never leave it.
			siblings := make([]int, 0, groupLen)
			for k := i; k < j; k++ {
				mm := mcpSiblingPattern.FindStringSubmatch(lines[k])
				if mm != nil && mm[1] == indent && mm[2] == role {
					siblings = append(siblings, k)
				}
			}
			n := len(siblings)
			if n > keepHead+keepTail {
				headEnd := i
				if keepHead > 0 {
					headEnd = siblings[keepHead]
				}
				tailStart := j
				if keepTail > 0 {
					tailStart = siblings[n-keepTail]
				}
				out = append(out, lines[i:headEnd]...)
				out = append(out, fmt.Sprintf(
					`%s... [%d similar "%s" items omitted by OmniRoute MCP filter]`,
					indent, n-keepHead-keepTail, role,
				))
				// Omitted siblings may carry ref anchors the agent needs to click.
				if refs := extractMCPRefs(strings.Join(lines[headEnd:tailStart], "\n")); len(refs) > 0 {
					out = append(out, fmt.Sprintf(
						`%s  [refs of omitted "%s" items (clickable): %s]`, indent, role, strings.Join(refs, " "),
					))
				}
				out = append(out, lines[tailStart:j]...)
			} else {
				out = append(out, lines[i:j]...)
			}
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// extractMCPRefs returns every unique [ref=eNN] anchor in order of appearance.
func extractMCPRefs(text string) []string {
	matches := mcpRefPattern.FindAllString(text, -1)
	seen := make(map[string]struct{}, len(matches))
	refs := make([]string, 0, len(matches))
	for _, ref := range matches {
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
