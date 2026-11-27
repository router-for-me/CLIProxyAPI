// Package quota models provider quota windows, parses poll and passive
// observations, and classifies whether an account can serve a model.
package quota

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Kind names a quota window's semantics. Each provider maps its own windows
// onto these kinds; values are never compared across kinds.
type Kind string

const (
	FiveHour    Kind = "five_hour"
	Weekly      Kind = "weekly"
	Monthly     Kind = "monthly"
	ModelWeekly Kind = "model_weekly"
)

// Source records where a window observation came from.
type Source string

const (
	SourcePoll    Source = "poll"
	SourcePassive Source = "passive"
)

// Window is one provider limit. UsedPercent is always 0–100 utilization;
// providers that report fractions or remaining capacity are converted by the
// parsers, never by callers.
type Window struct {
	Kind        Kind       `json:"kind"`
	Family      string     `json:"family,omitempty"`
	UsedPercent float64    `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	Rejected    bool       `json:"rejected,omitempty"`
	Source      Source     `json:"source"`
	ObservedAt  time.Time  `json:"observed_at"`
}

// Key identifies a window within one account's snapshot.
func (w Window) Key() string {
	if w.Kind == ModelWeekly {
		return string(w.Kind) + ":" + w.Family
	}
	return string(w.Kind)
}

// Long reports whether the window is a long (weekly, monthly, or model weekly) window.
func (w Window) Long() bool { return w.Kind != FiveHour }

// Account reports whether the window applies to every model of the account.
func (w Window) Account() bool { return w.Kind != ModelWeekly }

// RemainingPercent is the capacity left in the window, 0–100.
func (w Window) RemainingPercent() float64 {
	return math.Max(0, math.Min(100, 100-w.UsedPercent))
}

// Full reports whether the provider says the window has no capacity left.
func (w Window) Full() bool { return w.Rejected || w.UsedPercent >= 100 }

// Snapshot is the merged set of windows known for one account.
type Snapshot struct {
	Provider string    `json:"provider"`
	PlanType string    `json:"plan_type,omitempty"`
	Windows  []Window  `json:"windows"`
	PolledAt time.Time `json:"polled_at,omitempty"`
	// Identity is a SHA-256 of the upstream account identity at the last poll.
	Identity string `json:"identity,omitempty"`
}

// Clone returns a deep copy.
func (s Snapshot) Clone() Snapshot {
	out := s
	out.Windows = make([]Window, len(s.Windows))
	for i, w := range s.Windows {
		if w.ResetsAt != nil {
			t := *w.ResetsAt
			w.ResetsAt = &t
		}
		out.Windows[i] = w
	}
	return out
}

// Window returns the window with the given key.
func (s Snapshot) Window(key string) (Window, bool) {
	for _, w := range s.Windows {
		if w.Key() == key {
			return w, true
		}
	}
	return Window{}, false
}

// ReplacePolled installs a complete poll result. Windows absent from the poll
// are dropped: the poll is authoritative for which windows exist.
func (s *Snapshot) ReplacePolled(p Snapshot) {
	s.Provider = p.Provider
	if p.PlanType != "" {
		s.PlanType = p.PlanType
	}
	s.PolledAt = p.PolledAt
	s.Identity = p.Identity
	s.Windows = nil
	for _, w := range p.Windows {
		s.Windows = append(s.Windows, w)
	}
	s.sort()
}

// MergeObserved applies passive observations. A window is replaced only by a
// newer observation; windows the observation does not mention are kept.
func (s *Snapshot) MergeObserved(ws []Window) bool {
	changed := false
	for _, w := range ws {
		replaced := false
		for i, cur := range s.Windows {
			if cur.Key() != w.Key() {
				continue
			}
			replaced = true
			if !w.ObservedAt.Before(cur.ObservedAt) {
				s.Windows[i] = w
				changed = true
			}
		}
		if !replaced {
			s.Windows = append(s.Windows, w)
			changed = true
		}
	}
	s.sort()
	return changed
}

func (s *Snapshot) sort() {
	sort.Slice(s.Windows, func(i, j int) bool { return s.Windows[i].Key() < s.Windows[j].Key() })
}

// LastObserved is the newest observation time across windows.
func (s Snapshot) LastObserved() time.Time {
	var t time.Time
	for _, w := range s.Windows {
		if w.ObservedAt.After(t) {
			t = w.ObservedAt
		}
	}
	return t
}

// State classifies an account's quota for one model.
type State string

const (
	Ready        State = "ready"
	Exhausted    State = "exhausted"
	ResetPending State = "reset_pending"
	Stale        State = "stale"
	Unknown      State = "unknown"
)

// Assessment explains an account's quota state for one model.
type Assessment struct {
	State  State  `json:"state"`
	Reason string `json:"reason"`
	// Applicable is the long window whose reset ranks the account: the earliest
	// reset among relevant long windows. Nil when no long window has a reset.
	Applicable *Window `json:"applicable,omitempty"`
	// Remaining is the tightest remaining percentage across relevant long windows.
	Remaining float64 `json:"remaining_percent"`
	// FiveHourUsed is the five-hour utilization when known, else -1.
	FiveHourUsed float64 `json:"five_hour_used_percent"`
	// Until is when every exhausted window resets (Exhausted only).
	Until *time.Time `json:"until,omitempty"`
}

// Relevant reports whether a window constrains requests for model.
func Relevant(w Window, model string) bool {
	if w.Kind != ModelWeekly {
		return true
	}
	return w.Family != "" && strings.Contains(strings.ToLower(model), w.Family)
}

// Assess classifies the snapshot for a model at now. Exhaustion is checked
// before staleness: usage only grows within a window, so an exhausted window
// stays exhausted until its reset regardless of age.
func Assess(s Snapshot, model string, now time.Time, maxAge time.Duration) Assessment {
	a := Assessment{State: Unknown, FiveHourUsed: -1, Remaining: 100}
	var relevant []Window
	hasLong := false
	for _, w := range s.Windows {
		if !Relevant(w, model) {
			continue
		}
		relevant = append(relevant, w)
		if w.Kind == Weekly || w.Kind == Monthly {
			hasLong = true
		}
		if w.Kind == FiveHour {
			a.FiveHourUsed = w.UsedPercent
		}
	}
	if !hasLong {
		a.Reason = "no_long_window"
		return a
	}
	var until time.Time
	exhausted, pendingFull := "", ""
	for _, w := range relevant {
		if !w.Full() {
			continue
		}
		// A full window with no reported reset is exhausted until fresh data says otherwise.
		if w.ResetsAt == nil || w.ResetsAt.After(now) {
			if exhausted == "" {
				exhausted = w.Key()
			}
			if w.ResetsAt != nil && w.ResetsAt.After(until) {
				until = *w.ResetsAt
			}
		} else if pendingFull == "" {
			pendingFull = w.Key()
		}
	}
	if exhausted != "" {
		a.State, a.Reason = Exhausted, exhausted+"_exhausted"
		if !until.IsZero() {
			a.Until = &until
		}
		a.Remaining = 0
		return a
	}
	if pendingFull != "" {
		a.State, a.Reason = ResetPending, pendingFull+"_awaiting_reset_evidence"
		return a
	}
	for _, w := range relevant {
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			a.State, a.Reason = ResetPending, w.Key()+"_reset_passed"
			return a
		}
	}
	for _, w := range relevant {
		if w.ObservedAt.IsZero() || now.Before(w.ObservedAt.Add(-time.Minute)) || now.Sub(w.ObservedAt) >= maxAge {
			a.State, a.Reason = Stale, w.Key()+"_stale"
			return a
		}
	}
	for i := range relevant {
		w := relevant[i]
		if !w.Long() {
			continue
		}
		a.Remaining = math.Min(a.Remaining, w.RemainingPercent())
		if w.ResetsAt == nil {
			continue
		}
		if a.Applicable == nil || w.ResetsAt.Before(*a.Applicable.ResetsAt) {
			a.Applicable = &w
		}
	}
	a.State, a.Reason = Ready, "fresh"
	return a
}
