package payloadcompress

import (
	"regexp"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// Progressive aging ported from upstream progressiveAging.ts. The further a
// non-system, non-current-user message sits from the end of the conversation,
// the harder it is compressed: verbatim, lite, moderate or a rule summary.

type agingThresholds struct {
	fullSummary int
	moderate    int
	light       int
	verbatim    int
}

// agingDefaultThresholds mirrors DEFAULT_AGGRESSIVE_CONFIG.thresholds.
var agingDefaultThresholds = agingThresholds{
	fullSummary: 5,
	moderate:    3,
	light:       2,
	verbatim:    2,
}

const agingSummaryMaxLen = 2000

var fenceRe = regexp.MustCompile("^```")

func structuredKind(text string) string {
	trimmed := strings.TrimSpace(text)
	if fenceRe.MatchString(trimmed) && strings.HasSuffix(trimmed, "```") {
		return "fenced"
	}
	if jsonPrefixRe.MatchString(trimmed) && ojson.Valid(trimmed) {
		return "json"
	}
	return ""
}

func tagAged(tier, originalText, compressed string) string {
	switch structuredKind(originalText) {
	case "json":
		return originalText
	case "fenced":
		return compressedPrefix + "aging:" + tier + "]\n" + originalText
	default:
		return compressedPrefix + "aging:" + tier + "] " + compressed
	}
}

// applyAging rewrites old message raws and reports the tokens saved. A negative
// spareUserIndex means "spare the last user turn"; an explicit index spares it.
func applyAging(raws []string, thresholds agingThresholds, preserveSystem bool, spareUserIndex int) ([]string, int) {
	if len(raws) == 0 {
		return raws, 0
	}
	lastUserIdx := spareUserIndex
	if lastUserIdx < 0 {
		for i := len(raws) - 1; i >= 0; i-- {
			if ojson.Parse(raws[i]).Get("role").String() == "user" {
				lastUserIdx = i
				break
			}
		}
	}

	total := len(raws)
	out := make([]string, len(raws))
	saved := 0
	lite := liteEngine{}
	caveman := cavemanEngine{}
	for i, raw := range raws {
		item := ojson.Parse(raw)
		role := item.Get("role").String()
		text := msgText(item.Get("content"))

		if (preserveSystem && role == "system") ||
			strings.HasPrefix(text, compressedPrefix) ||
			i == lastUserIdx {
			out[i] = raw
			continue
		}

		distance := total - 1 - i
		switch {
		case distance <= thresholds.verbatim:
			out[i] = raw
		case distance <= thresholds.light:
			out[i] = agingApplyTier(item, raw, "light", text, lite.compressKind, &saved)
		case distance <= thresholds.moderate:
			out[i] = agingApplyTier(item, raw, "moderate", text, caveman.compressKind, &saved)
		default:
			out[i] = agingApplyFullSummary(item, raw, text, role, &saved)
		}
	}
	if saved < 0 {
		saved = 0
	}
	return out, saved
}

func agingApplyTier(item ojson.Result, raw, tier, text string, compressKind func(segmentKind, string) (string, bool), saved *int) string {
	next, changed := compressKind(roleSegmentKind(item.Get("role").String()), text)
	if !changed || next == "" {
		return raw
	}
	tagged := tagAged(tier, text, next)
	if tagged == text {
		return raw
	}
	*saved += charTokens(text) - charTokens(tagged)
	return msgReplaceTextItem(item, tagged)
}

func agingApplyFullSummary(item ojson.Result, raw, text, role string, saved *int) string {
	var tagged string
	if role == "assistant" {
		summary := summarizeRuleBased([]ojson.Result{item}, agingSummaryMaxLen, true)
		if summary == "" {
			return raw
		}
		tagged = tagAged("fullSummary", text, summary)
	} else if role == "user" {
		firstLine := text
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			firstLine = text[:idx]
		}
		tagged = tagAged("fullSummary", text, summaryTruncChars(firstLine, 120))
	} else {
		return raw
	}
	if tagged == text {
		return raw
	}
	*saved += charTokens(text) - charTokens(tagged)
	return msgReplaceTextItem(item, tagged)
}

// roleSegmentKind maps a chat role onto the pipeline's segment taxonomy.
func roleSegmentKind(role string) segmentKind {
	switch role {
	case "assistant":
		return kindAssistant
	case "tool", "function":
		return kindToolOutput
	case "system":
		return kindSystem
	default:
		return kindUser
	}
}
