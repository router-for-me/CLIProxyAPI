package loguploader

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// sessionHoldRecord is durable hold metadata keyed by session_id or orphan path.
type sessionHoldRecord struct {
	Verdict       string    `json:"verdict"`
	Reasons       []string  `json:"reasons,omitempty"`
	FirstSeen     time.Time `json:"first_seen"`
	LastActivity  time.Time `json:"last_activity"`
	LastEvaluated time.Time `json:"last_evaluated"`
	SourceFiles   []string  `json:"source_files,omitempty"`
	PromptRounds  int       `json:"prompt_rounds"`
	ToolCalls     int       `json:"tool_calls"`
}

type sessionGateStore struct {
	Sessions map[string]sessionHoldRecord `json:"sessions"`
}

type sessionGateRunStats struct {
	ReadyFiles   int
	HeldFiles    int
	DroppedFiles int
	PassSessions int
	HeldSessions int
	DropSessions int
	ReasonHist   map[string]int
	ParseErrors  int
}

// applySessionGate partitions settled sources into upload-ready files.
// When the gate is disabled, sources are returned unchanged.
func (s *Service) applySessionGate(sources []sourceLog, state *uploadState, dryRun bool) ([]sourceLog, sessionGateRunStats, error) {
	stats := sessionGateRunStats{ReasonHist: map[string]int{}}
	if !s.cfg.SessionGate.Enabled {
		stats.ReadyFiles = len(sources)
		return sources, stats, nil
	}
	if state.SessionGate == nil {
		state.SessionGate = &sessionGateStore{Sessions: make(map[string]sessionHoldRecord)}
		state.dirty = true
	}
	if state.SessionGate.Sessions == nil {
		state.SessionGate.Sessions = make(map[string]sessionHoldRecord)
		state.dirty = true
	}

	now := s.now().In(s.location)
	metrics := make([]gateFileMetrics, 0, len(sources))
	for _, source := range sources {
		m := parseGateFile(source, s.location, s.cfg.SessionGate)
		if m.ParseError != "" {
			stats.ParseErrors++
		}
		metrics = append(metrics, m)
	}

	verdicts := evaluateSessions(metrics, s.cfg.SessionGate)
	liveKeys := make(map[string]struct{}, len(verdicts))
	var ready []sourceLog

	for _, v := range verdicts {
		liveKeys[v.Key] = struct{}{}
		prev, hadPrev := state.SessionGate.Sessions[v.Key]
		firstSeen := now
		if hadPrev && !prev.FirstSeen.IsZero() {
			firstSeen = prev.FirstSeen
		} else if !v.FirstActivity.IsZero() {
			firstSeen = v.FirstActivity
		}
		lastActivity := v.LastActivity
		if lastActivity.IsZero() {
			lastActivity = now
		}

		if v.OK {
			// Pick eligibility hour and rebucket sources.
			hour, okHour := s.pickEligibilityHour(v.Files, *state)
			if !okHour {
				// No unsealed settled hour yet — hold and retry later.
				v.OK = false
				v.Reasons = append(v.Reasons, "no_unsealed_hour")
			} else {
				stats.PassSessions++
				for _, f := range v.Files {
					src := f.Source
					src.ArchiveHour = hour
					ready = append(ready, src)
					stats.ReadyFiles++
				}
				// Clear hold entry on pass.
				if hadPrev {
					delete(state.SessionGate.Sessions, v.Key)
					state.dirty = true
				}
				continue
			}
		}

		// Hold path (including pass that could not rebucket).
		expired := now.Sub(lastActivity) > s.cfg.SessionGate.MaxHoldAge ||
			now.Sub(firstSeen) > s.cfg.SessionGate.MaxAbsoluteAge
		if expired {
			stats.DropSessions++
			dropped, errDrop := s.dropHeldSession(v, dryRun)
			stats.DroppedFiles += dropped
			if errDrop != nil {
				log.WithError(errDrop).WithField("session_key", v.Key).Warn("session gate drop incomplete")
			}
			if hadPrev {
				delete(state.SessionGate.Sessions, v.Key)
				state.dirty = true
			}
			if !dryRun {
				_ = s.appendAudit(auditRecord{
					Timestamp:      now,
					Status:         "session_gate_dropped",
					Hour:           lastActivity.Truncate(time.Hour),
					SourceCount:    dropped,
					DeletedSources: dropped,
					Error:          strings.Join(v.Reasons, ";"),
					KeyNames:       map[string]auditKeyNameSummary{},
				})
			}
			for _, reason := range v.Reasons {
				stats.ReasonHist[reasonKey(reason)]++
			}
			continue
		}

		stats.HeldSessions++
		stats.HeldFiles += len(v.Files)
		for _, reason := range v.Reasons {
			stats.ReasonHist[reasonKey(reason)]++
		}
		files := make([]string, 0, len(v.Files))
		for _, f := range v.Files {
			files = append(files, f.Source.Relative)
		}
		sort.Strings(files)
		state.SessionGate.Sessions[v.Key] = sessionHoldRecord{
			Verdict:       verdictHold,
			Reasons:       append([]string(nil), v.Reasons...),
			FirstSeen:     firstSeen,
			LastActivity:  lastActivity,
			LastEvaluated: now,
			SourceFiles:   files,
			PromptRounds:  v.PromptRounds,
			ToolCalls:     v.ToolCalls,
		}
		state.dirty = true
	}

	// Drop stale hold records for sessions no longer present on disk.
	for key := range state.SessionGate.Sessions {
		if _, ok := liveKeys[key]; !ok {
			delete(state.SessionGate.Sessions, key)
			state.dirty = true
		}
	}

	if stats.HeldSessions > 0 || stats.DropSessions > 0 || stats.PassSessions > 0 {
		log.WithFields(log.Fields{
			"ready_files":   stats.ReadyFiles,
			"held_files":    stats.HeldFiles,
			"dropped_files": stats.DroppedFiles,
			"pass_sessions": stats.PassSessions,
			"held_sessions": stats.HeldSessions,
			"drop_sessions": stats.DropSessions,
			"parse_errors":  stats.ParseErrors,
			"reasons":       stats.ReasonHist,
		}).Info("session gate evaluated")
		if !dryRun {
			_ = s.appendAudit(auditRecord{
				Timestamp:   now,
				Status:      "session_gate_hold",
				Hour:        now.Truncate(time.Hour),
				SourceCount: stats.HeldFiles,
				KeyNames:    map[string]auditKeyNameSummary{},
				Error: fmt.Sprintf("pass=%d held=%d drop=%d ready_files=%d held_files=%d dropped_files=%d",
					stats.PassSessions, stats.HeldSessions, stats.DropSessions,
					stats.ReadyFiles, stats.HeldFiles, stats.DroppedFiles),
			})
		}
	}
	return ready, stats, nil
}

func (s *Service) dropHeldSession(v sessionVerdict, dryRun bool) (int, error) {
	if dryRun {
		return len(v.Files), nil
	}
	var dropped int
	var errs []error
	for _, f := range v.Files {
		// Re-check identity before delete.
		info, errStat := os.Stat(f.Source.Path)
		if errStat != nil {
			if os.IsNotExist(errStat) {
				dropped++
				continue
			}
			errs = append(errs, errStat)
			continue
		}
		if info.Size() != f.Source.Size || !info.ModTime().Equal(f.Source.ModTime) {
			// File changed; skip delete so a later run re-evaluates.
			log.WithField("source", f.Source.Relative).Warn("session gate skip drop: source fingerprint changed")
			continue
		}
		if errRemove := os.Remove(f.Source.Path); errRemove != nil && !os.IsNotExist(errRemove) {
			errs = append(errs, fmt.Errorf("remove %s: %w", f.Source.Relative, errRemove))
			continue
		}
		dropped++
		// Clean empty key dirs best-effort.
		_ = os.Remove(filepath.Dir(f.Source.Path))
	}
	if len(errs) > 0 {
		return dropped, errs[0]
	}
	return dropped, nil
}

// pickEligibilityHour chooses a settled, unsealed hour for delayed session packaging.
func (s *Service) pickEligibilityHour(files []gateFileMetrics, state uploadState) (time.Time, bool) {
	if len(files) == 0 {
		return time.Time{}, false
	}
	now := s.now().In(s.location)
	provider := files[0].Source.Provider
	for _, f := range files[1:] {
		if f.Source.Provider != "" && f.Source.Provider != provider {
			// Mixed providers: use the first file's provider; rare for one session.
			break
		}
	}

	maxHour := files[0].Source.ArchiveHour
	for _, f := range files[1:] {
		if f.Source.ArchiveHour.After(maxHour) {
			maxHour = f.Source.ArchiveHour
		}
	}
	maxHour = maxHour.In(s.location).Truncate(time.Hour)

	if s.hourUsable(maxHour, provider, state, now) {
		return maxHour, true
	}

	// Prefer hours at/after maxHour (eligibility-hour), then walk back for any usable slot.
	latestSettled := now.Truncate(time.Hour).Add(-time.Hour)
	for h := maxHour; !h.After(latestSettled); h = h.Add(time.Hour) {
		if s.hourUsable(h, provider, state, now) {
			return h, true
		}
	}
	for h := latestSettled; !h.Before(maxHour.Add(-48 * time.Hour)); h = h.Add(-time.Hour) {
		if s.hourUsable(h, provider, state, now) {
			return h, true
		}
	}
	return time.Time{}, false
}

func (s *Service) hourUsable(hour time.Time, provider string, state uploadState, now time.Time) bool {
	hour = hour.In(s.location).Truncate(time.Hour)
	readyAt := hour.Add(time.Hour).Add(s.cfg.Schedule.SettleDelay)
	if readyAt.After(now) {
		return false
	}
	if _, sealed := state.Hours[hourStateKey(hour, provider)]; sealed {
		return false
	}
	if prepared, ok := state.PreparedHours[hourStateKey(hour, provider)]; ok && prepared.ObjectKey != "" {
		return false
	}
	return true
}

func reasonKey(reason string) string {
	switch {
	case strings.HasPrefix(reason, "prompt_rounds"):
		return "prompt_rounds"
	case reason == "no_tool_call":
		return "no_tool_call"
	case reason == "empty_session_id":
		return "empty_session_id"
	case reason == "ends_with_tool_call":
		return "ends_with_tool_call"
	case reason == "unpaired_tool_calls":
		return "unpaired_tool_calls"
	case reason == "response_unparsed":
		return "response_unparsed"
	case reason == "no_unsealed_hour":
		return "no_unsealed_hour"
	default:
		return reason
	}
}
