package loguploader

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	verdictPass = "pass"
	verdictHold = "hold"
)

// sessionVerdict is the gate result for one session_id (or orphan key).
type sessionVerdict struct {
	Key             string
	SessionID       string
	OK              bool
	Reasons         []string
	PromptRounds    int
	ToolCalls       int
	Files           []gateFileMetrics
	FirstActivity   time.Time
	LastActivity    time.Time
	EligibilityHour time.Time // max ArchiveHour among files; may be rebucketed later
}

func evaluateSessions(files []gateFileMetrics, rules SessionGateConfig) []sessionVerdict {
	groups := make(map[string][]gateFileMetrics)
	order := make([]string, 0)
	for _, f := range files {
		key := f.SessionID
		if key == "" {
			key = "orphan:" + f.Source.Relative
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], f)
	}

	out := make([]sessionVerdict, 0, len(order))
	for _, key := range order {
		group := groups[key]
		out = append(out, evaluateOneSession(key, group, rules))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Key < out[j].Key
	})
	return out
}

func evaluateOneSession(key string, files []gateFileMetrics, rules SessionGateConfig) sessionVerdict {
	v := sessionVerdict{
		Key:       key,
		SessionID: files[0].SessionID,
		Files:     files,
		OK:        true,
	}
	if strings.HasPrefix(key, "orphan:") {
		v.SessionID = ""
	}

	for _, f := range files {
		act := f.Source.ModTime
		if act.IsZero() {
			act = f.Timestamp
		}
		if v.FirstActivity.IsZero() || act.Before(v.FirstActivity) {
			v.FirstActivity = act
		}
		if v.LastActivity.IsZero() || act.After(v.LastActivity) {
			v.LastActivity = act
		}
		if v.EligibilityHour.IsZero() || f.Source.ArchiveHour.After(v.EligibilityHour) {
			v.EligibilityHour = f.Source.ArchiveHour
		}
	}

	// Rule 2: session_id required.
	if rules.RequireSessionID && strings.TrimSpace(v.SessionID) == "" {
		v.OK = false
		v.Reasons = append(v.Reasons, "empty_session_id")
	}

	snapshot := pickGateSnapshot(files)
	v.PromptRounds = snapshot.PromptRounds
	v.ToolCalls = snapshot.ToolCalls
	// Also count tools from any file (response fallback / earlier turns).
	for _, f := range files {
		if f.ToolCalls > v.ToolCalls {
			v.ToolCalls = f.ToolCalls
		}
	}

	// Rule 1: effective prompt rounds.
	if snapshot.PromptRounds < rules.MinPromptRounds {
		v.OK = false
		v.Reasons = append(v.Reasons, fmt.Sprintf("prompt_rounds=%d<%d", snapshot.PromptRounds, rules.MinPromptRounds))
	}

	// Rule 5: at least one tool call.
	if rules.RequireToolCall && v.ToolCalls < 1 {
		v.OK = false
		v.Reasons = append(v.Reasons, "no_tool_call")
	}

	// Rule 4: call/output pairing on snapshot (and any unpaired flag on files).
	if rules.RejectUnpairedToolCalls {
		unpaired := snapshot.UnpairedToolCalls
		for _, f := range files {
			if f.UnpairedToolCalls {
				unpaired = true
				break
			}
		}
		if unpaired {
			v.OK = false
			v.Reasons = append(v.Reasons, "unpaired_tool_calls")
		}
	}

	// Rule 3: last non-title/summary turn RESPONSE does not end with tool_call.
	if rules.RequireEndsWithoutToolCall {
		latest := pickLatestNonTitleFile(files)
		if latest.ParseError != "" && !latest.ResponseParsed {
			v.OK = false
			v.Reasons = append(v.Reasons, "response_unparsed")
		} else if !latest.ResponseParsed {
			v.OK = false
			v.Reasons = append(v.Reasons, "response_unparsed")
		} else if isToolCallType(latest.LastResponseType) {
			v.OK = false
			v.Reasons = append(v.Reasons, "ends_with_tool_call")
		}
	}

	if v.OK {
		v.Reasons = nil
	}
	return v
}

func pickGateSnapshot(files []gateFileMetrics) gateFileMetrics {
	if len(files) == 0 {
		return gateFileMetrics{}
	}
	candidates := make([]gateFileMetrics, 0, len(files))
	for _, f := range files {
		if isCompactionRequestKind(f.RequestKind) {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		candidates = files
	}
	best := candidates[0]
	for _, f := range candidates[1:] {
		if f.InputLen > best.InputLen {
			best = f
			continue
		}
		if f.InputLen == best.InputLen && f.Timestamp.After(best.Timestamp) {
			best = f
		}
	}
	return best
}

func pickLatestNonTitleFile(files []gateFileMetrics) gateFileMetrics {
	if len(files) == 0 {
		return gateFileMetrics{}
	}
	var best gateFileMetrics
	found := false
	for _, f := range files {
		if isTitleOrSummaryRequestKind(f.RequestKind) {
			continue
		}
		if isCompactionRequestKind(f.RequestKind) {
			continue
		}
		if !found || f.Timestamp.After(best.Timestamp) || (f.Timestamp.Equal(best.Timestamp) && f.Source.ModTime.After(best.Source.ModTime)) {
			best = f
			found = true
		}
	}
	if !found {
		// Fall back to absolute latest including title/compact.
		best = files[0]
		for _, f := range files[1:] {
			if f.Timestamp.After(best.Timestamp) || (f.Timestamp.Equal(best.Timestamp) && f.Source.ModTime.After(best.Source.ModTime)) {
				best = f
			}
		}
	}
	return best
}
