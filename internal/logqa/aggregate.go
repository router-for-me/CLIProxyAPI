package logqa

import (
	"sort"
	"strings"
	"time"
)

// AggregateSessions groups request records by session_id and picks the max-input snapshot.
//
// A session snapshot is the single request record used to judge the whole session:
// among all logs sharing the same session_id, we pick the one with the longest input
// (then latest timestamp) and evaluate prompt_rounds / tool_calls / duplicate assistant
// only on that request. Compaction turns are excluded from snapshot selection when any
// non-compaction request exists, because they embed full history but zero out "real"
// user prompts via request_kind filtering.
func AggregateSessions(requests []RequestRecord, rules RulesConfig) []SessionRecord {
	type group struct {
		requests []RequestRecord
	}
	groups := make(map[string]*group)
	order := make([]string, 0)
	for _, req := range requests {
		sid := req.SessionID
		if sid == "" {
			sid = "unknown:" + req.SourceFile
		}
		g, ok := groups[sid]
		if !ok {
			g = &group{}
			groups[sid] = g
			order = append(order, sid)
		}
		g.requests = append(g.requests, req)
	}

	sessions := make([]SessionRecord, 0, len(order))
	for _, sid := range order {
		g := groups[sid]
		snapshot := pickSnapshot(g.requests)
		latest := pickLatestNonTitleRequest(g.requests)
		toolCalls := snapshot.ToolCalls
		unpaired := snapshot.UnpairedToolCalls
		hasRealSession := false
		for _, r := range g.requests {
			if r.ToolCalls > toolCalls {
				toolCalls = r.ToolCalls
			}
			if r.UnpairedToolCalls {
				unpaired = true
			}
			if r.HasRealSessionID {
				hasRealSession = true
			}
		}
		// Synthetic aggregation keys (unknown:path / orphan) never count as real session ids.
		if strings.HasPrefix(sid, "unknown:") {
			hasRealSession = false
		}
		extras := evaluateExtras{
			HasRealSessionID:  hasRealSession,
			UnpairedToolCalls: unpaired,
			ResponseParsed:    latest.ResponseParsed,
			LastResponseType:  latest.LastResponseType,
		}
		ok, reasons := EvaluateSession(snapshot.PromptRounds, toolCalls, snapshot.DupAssistant, rules, extras)

		threadSet := map[string]struct{}{}
		keySet := map[string]struct{}{}
		files := make([]string, 0, len(g.requests))
		var firstTS, lastTS time.Time
		for _, r := range g.requests {
			if r.ThreadID != "" {
				threadSet[r.ThreadID] = struct{}{}
			}
			if r.KeyName != "" {
				keySet[r.KeyName] = struct{}{}
			}
			files = append(files, r.SourceFile)
			if firstTS.IsZero() || r.Timestamp.Before(firstTS) {
				firstTS = r.Timestamp
			}
			if lastTS.IsZero() || r.Timestamp.After(lastTS) {
				lastTS = r.Timestamp
			}
			if !r.ModTime.IsZero() {
				if lastTS.IsZero() || r.ModTime.After(lastTS) {
					// keep timestamp preference but track modtime already in request
				}
			}
		}
		threads := sortedKeys(threadSet)
		keys := sortedKeys(keySet)
		sort.Strings(files)
		title, titleSource := pickSessionTitle(g.requests)

		eligibility := ComputeUploadEligibility(sid, hasRealSession, reasons)

		sessions = append(sessions, SessionRecord{
			SessionID:    sid,
			Title:        title,
			TitleSource:  titleSource,
			ThreadIDs:    threads,
			KeyNames:     keys,
			PromptRounds: snapshot.PromptRounds,
			// Report snapshot tool count (max_input view); evaluation used max across files.
			ToolCalls:          snapshot.ToolCalls,
			DupAssistantGroups: snapshot.DupAssistant,
			OK:                 ok,
			FailReasons:        reasons,
			UploadEligibility:  eligibility,
			FirstTS:            firstTS,
			LastTS:             lastTS,
			SourceFiles:        files,
			SamplePrompts:      snapshot.SamplePrompts,
			InputLen:           snapshot.InputLen,
			RequestCount:       len(g.requests),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].OK != sessions[j].OK {
			return !sessions[i].OK && sessions[j].OK // fails first
		}
		return sessions[i].SessionID < sessions[j].SessionID
	})
	return sessions
}

// pickSnapshot chooses the request used as the session's QA verdict source.
// Prefer non-compaction requests; only fall back to compaction when that is all we have.
// Among candidates: max InputLen, then latest Timestamp.
func pickSnapshot(requests []RequestRecord) RequestRecord {
	if len(requests) == 0 {
		return RequestRecord{}
	}
	candidates := make([]RequestRecord, 0, len(requests))
	for _, r := range requests {
		if isCompactionRequestKind(r.RequestKind) {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		// Session only has compaction turns (earlier turn logs may already be gone).
		candidates = requests
	}
	best := candidates[0]
	for _, r := range candidates[1:] {
		if r.InputLen > best.InputLen {
			best = r
			continue
		}
		if r.InputLen == best.InputLen && r.Timestamp.After(best.Timestamp) {
			best = r
		}
	}
	return best
}

// isCompactionRequestKind reports whether a Codex turn is a context checkpoint compaction.
// Those turns often carry the longest input (full history) but are not real user turns.
func isCompactionRequestKind(requestKind string) bool {
	rk := strings.ToLower(strings.TrimSpace(requestKind))
	if rk == "" {
		return false
	}
	return strings.Contains(rk, "compact")
}

func isTitleOrSummaryRequestKind(requestKind string) bool {
	rk := strings.ToLower(strings.TrimSpace(requestKind))
	return strings.Contains(rk, "title") || strings.Contains(rk, "summary")
}

// pickLatestNonTitleRequest selects the newest non-title/non-compact turn for rule 3 (RESPONSE tail).
func pickLatestNonTitleRequest(requests []RequestRecord) RequestRecord {
	if len(requests) == 0 {
		return RequestRecord{}
	}
	var best RequestRecord
	found := false
	for _, r := range requests {
		if isTitleOrSummaryRequestKind(r.RequestKind) || isCompactionRequestKind(r.RequestKind) {
			continue
		}
		if !found || r.Timestamp.After(best.Timestamp) || (r.Timestamp.Equal(best.Timestamp) && r.ModTime.After(best.ModTime)) {
			best = r
			found = true
		}
	}
	if !found {
		best = requests[0]
		for _, r := range requests[1:] {
			if r.Timestamp.After(best.Timestamp) || (r.Timestamp.Equal(best.Timestamp) && r.ModTime.After(best.ModTime)) {
				best = r
			}
		}
	}
	return best
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func summarizeSessions(sessions []SessionRecord) (total, pass, fail int, hist map[string]int, rate float64) {
	hist = map[string]int{
		"prompt_rounds":       0,
		"no_tool_call":        0,
		"duplicate_assistant": 0,
		"empty_session_id":    0,
		"ends_with_tool_call": 0,
		"unpaired_tool_calls": 0,
		"response_unparsed":   0,
	}
	total = len(sessions)
	for _, s := range sessions {
		if s.OK {
			pass++
			continue
		}
		fail++
		for _, reason := range s.FailReasons {
			switch {
			case hasPrefix(reason, "prompt_rounds"):
				hist["prompt_rounds"]++
			case reason == "no_tool_call":
				hist["no_tool_call"]++
			case hasPrefix(reason, "duplicate_assistant"):
				hist["duplicate_assistant"]++
			case reason == "empty_session_id":
				hist["empty_session_id"]++
			case reason == "ends_with_tool_call":
				hist["ends_with_tool_call"]++
			case reason == "unpaired_tool_calls":
				hist["unpaired_tool_calls"]++
			case reason == "response_unparsed":
				hist["response_unparsed"]++
			}
		}
	}
	if total > 0 {
		rate = float64(pass) / float64(total)
	}
	return total, pass, fail, hist, rate
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
