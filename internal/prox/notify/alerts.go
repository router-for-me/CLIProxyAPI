package notify

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
)

const (
	// earlyResetJump is the remaining-percent rise, within one cycle, that
	// counts as a quota restored before its reset time.
	earlyResetJump = 30
	credFailures   = 3
	credFailSpan   = 30 * time.Minute
	fiveHourLead   = time.Hour
	weeklyLead     = 24 * time.Hour
)

// WindowState tracks alert progress for one account window.
type WindowState struct {
	Level         int        `json:"level"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	Exhausted     bool       `json:"exhausted,omitempty"`
	LastRemaining float64    `json:"last_remaining"`
	// EndRemaining is the last reading in the final 15 minutes of a cycle.
	EndRemaining *float64  `json:"end_remaining,omitempty"`
	RemindedFor  time.Time `json:"reminded_for,omitempty"`
}

// AccountState tracks alert progress for one account.
type AccountState struct {
	Plan        string                  `json:"plan,omitempty"`
	CredFirst   time.Time               `json:"cred_first,omitempty"`
	CredCount   int                     `json:"cred_count,omitempty"`
	CredLastAt  time.Time               `json:"cred_last_at,omitempty"`
	CredAlerted bool                    `json:"cred_alerted,omitempty"`
	Windows     map[string]*WindowState `json:"windows"`
}

// State is the persisted alert state.
type State struct {
	Accounts map[string]*AccountState `json:"accounts"`
}

// Evaluator derives alerts from successive account snapshots.
type Evaluator struct {
	mu    sync.Mutex
	state State
	dirty bool
}

// NewEvaluator returns an empty evaluator.
func NewEvaluator() *Evaluator {
	return &Evaluator{state: State{Accounts: map[string]*AccountState{}}}
}

// Import restores persisted state.
func (e *Evaluator) Import(s State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.Accounts == nil {
		s.Accounts = map[string]*AccountState{}
	}
	e.state = s
}

// Export returns the state for persistence. Callers must not mutate it.
func (e *Evaluator) Export() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := State{Accounts: make(map[string]*AccountState, len(e.state.Accounts))}
	for id, a := range e.state.Accounts {
		c := *a
		c.Windows = make(map[string]*WindowState, len(a.Windows))
		for k, w := range a.Windows {
			wc := *w
			c.Windows[k] = &wc
		}
		out.Accounts[id] = &c
	}
	return out
}

// TakeDirty reports and clears whether state changed.
func (e *Evaluator) TakeDirty() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.dirty
	e.dirty = false
	return d
}

// Evaluate compares accounts against the stored state and returns the
// notifications due. The first sighting of anything only records it.
func (e *Evaluator) Evaluate(cfg Config, accounts []router.AccountView, now time.Time) []Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	labels := Labels(accounts)
	var out []Message
	for _, a := range accounts {
		if a.Provider != "claude" && a.Provider != "codex" {
			continue
		}
		st := e.state.Accounts[a.ID]
		if st == nil {
			st = &AccountState{Windows: map[string]*WindowState{}}
			e.state.Accounts[a.ID] = st
			e.dirty = true
		}
		label := labels[a.ID]
		planChanged := false
		if a.Plan != "" && a.Plan != st.Plan {
			if st.Plan != "" {
				out = append(out, Message{Title: fmt.Sprintf("🎫 %s · plan %s → %s", label, st.Plan, a.Plan), Level: "info"})
				planChanged = true
			}
			st.Plan = a.Plan
			e.dirty = true
		}
		out = append(out, e.credentialLocked(cfg, st, a, label, now)...)
		for _, w := range a.Windows {
			if w.Stale {
				continue
			}
			ws := st.Windows[w.Key]
			if ws == nil || planChanged {
				// New window or new plan: record a fresh baseline silently.
				st.Windows[w.Key] = baseline(cfg, w)
				e.dirty = true
				continue
			}
			if m, ok := e.windowLocked(cfg, ws, a, w, label, now); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func baseline(cfg Config, w router.WindowView) *WindowState {
	return &WindowState{Level: level(cfg, w), ResetsAt: w.ResetsAt, LastRemaining: w.Remaining, Exhausted: w.Remaining <= 0 || w.Rejected}
}

// level maps remaining quota to 0 (fine) .. 4 (empty).
func level(cfg Config, w router.WindowView) int {
	switch {
	case w.Rejected || w.Remaining <= 0:
		return 4
	case w.Remaining <= cfg.Thresholds[2]:
		return 3
	case w.Remaining <= cfg.Thresholds[1]:
		return 2
	case w.Remaining <= cfg.Thresholds[0]:
		return 1
	}
	return 0
}

func short(kind string) string {
	switch kind {
	case "five_hour":
		return "5h"
	case "weekly":
		return "7d"
	case "monthly":
		return "30d"
	}
	return kind
}

func isFiveHour(w router.WindowView) bool { return w.Kind == "five_hour" }

func (e *Evaluator) windowLocked(cfg Config, ws *WindowState, a router.AccountView, w router.WindowView, label string, now time.Time) (Message, bool) {
	defer func() { e.dirty = true }()
	name := short(w.Kind)
	if w.Family != "" {
		name = w.Family + " " + name
	}
	// A new cycle: the stored reset passed and the window now reports a
	// later reset (or none, when it has not started).
	if ws.ResetsAt != nil && !now.Before(*ws.ResetsAt) && (w.ResetsAt == nil || w.ResetsAt.After(ws.ResetsAt.Add(time.Minute))) {
		prev := *ws
		*ws = *baseline(cfg, w)
		policy := cfg.Recovery.Weekly
		if isFiveHour(w) {
			policy = cfg.Recovery.FiveHour
		}
		used := prev.LastRemaining < 100 || prev.Exhausted
		if used && (policy == PolicyAll || (policy == PolicyAfterExhausted && prev.Exhausted)) {
			body := ""
			if prev.EndRemaining != nil {
				body = fmt.Sprintf("Previous cycle ended with %s left.\n", pct(*prev.EndRemaining))
			}
			return Message{Title: fmt.Sprintf("✅ %s · %s reset, %s left", label, name, pct(w.Remaining)), Body: body + windowsBody(cfg, a, now), Level: "info"}, true
		}
		return Message{}, false
	}
	// Quota restored without the reset time changing (for example a
	// provider-granted reset).
	if w.Remaining-ws.LastRemaining >= earlyResetJump && ws.Level > 0 {
		ws.Level, ws.Exhausted, ws.LastRemaining, ws.ResetsAt = level(cfg, w), false, w.Remaining, w.ResetsAt
		return Message{Title: fmt.Sprintf("✅ %s · %s restored early, %s left", label, name, pct(w.Remaining)), Body: windowsBody(cfg, a, now), Level: "info"}, true
	}
	if w.ResetsAt != nil {
		if ws.ResetsAt == nil {
			ws.ResetsAt = w.ResetsAt
		}
		if w.ResetsAt.Sub(now) <= 15*time.Minute {
			r := w.Remaining
			ws.EndRemaining = &r
		}
	}
	ws.LastRemaining = w.Remaining
	if l := level(cfg, w); l > ws.Level {
		ws.Level = l
		if l == 4 {
			ws.Exhausted = true
		}
		icon, sev := "🟡", "warning"
		if l >= 2 {
			icon, sev = "🔴", "critical"
		}
		return Message{Title: fmt.Sprintf("%s %s · %s %s | %s", icon, label, name, pct(w.Remaining), until(w.ResetsAt, now)), Body: windowsBody(cfg, a, now), Level: sev}, true
	}
	// Reset reminder.
	if w.ResetsAt != nil && w.ResetsAt.After(now) && !ws.RemindedFor.Equal(*w.ResetsAt) {
		policy, lead := cfg.ResetReminder.Weekly, weeklyLead
		if isFiveHour(w) {
			policy, lead = cfg.ResetReminder.FiveHour, fiveHourLead
		}
		if w.ResetsAt.Sub(now) <= lead && policy != PolicyOff {
			ws.RemindedFor = *w.ResetsAt
			if policy == PolicyAll || w.Remaining > cfg.Thresholds[2] {
				return Message{Title: fmt.Sprintf("⏰ %s · %s resets in %s with %s left", label, name, until(w.ResetsAt, now), pct(w.Remaining)), Body: windowsBody(cfg, a, now), Level: "info"}, true
			}
		}
	}
	return Message{}, false
}

func (e *Evaluator) credentialLocked(cfg Config, st *AccountState, a router.AccountView, label string, now time.Time) []Message {
	authErr := a.PollError == "quota_http_401" || a.PollError == "quota_http_403"
	if !authErr {
		if st.CredAlerted && a.PollError == "" && a.LastPollAt != nil && a.LastPollAt.After(st.CredLastAt) {
			st.CredAlerted, st.CredCount, st.CredFirst = false, 0, time.Time{}
			e.dirty = true
			return []Message{{Title: fmt.Sprintf("✅ %s · credential works again", label), Level: "info"}}
		}
		if st.CredCount != 0 && !st.CredAlerted {
			st.CredCount, st.CredFirst = 0, time.Time{}
			e.dirty = true
		}
		return nil
	}
	if a.LastPollAt == nil || !a.LastPollAt.After(st.CredLastAt) {
		return nil
	}
	st.CredLastAt = *a.LastPollAt
	if st.CredCount == 0 {
		st.CredFirst = *a.LastPollAt
	}
	st.CredCount++
	e.dirty = true
	if !st.CredAlerted && st.CredCount >= credFailures && st.CredLastAt.Sub(st.CredFirst) >= credFailSpan {
		st.CredAlerted = true
		return []Message{{Title: fmt.Sprintf("🔑 %s · credential rejected", label), Body: "Quota polls have been rejected for " + st.CredLastAt.Sub(st.CredFirst).Round(time.Minute).String() + ". Sign in again.", Level: "critical"}}
	}
	return nil
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", math.Round(v)) }

func until(t *time.Time, now time.Time) string {
	if t == nil {
		return "-"
	}
	d := t.Sub(now)
	switch {
	case d <= 0:
		return "now"
	case d >= 48*time.Hour:
		return fmt.Sprintf("%02dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%02dh", int(d.Hours()))
	}
	return fmt.Sprintf("%02dm", int(d.Minutes()))
}

func windowsBody(cfg Config, a router.AccountView, now time.Time) string {
	var lines []string
	ws := append([]router.WindowView(nil), a.Windows...)
	sort.SliceStable(ws, func(i, j int) bool { return isFiveHour(ws[i]) && !isFiveHour(ws[j]) })
	for _, w := range ws {
		name := short(w.Kind)
		if w.Family != "" {
			name = w.Family + " " + name
		}
		line := fmt.Sprintf("%s: %s | %s", name, pct(w.Remaining), until(w.ResetsAt, now))
		if w.ResetsAt != nil {
			line += " | " + w.ResetsAt.In(cfg.location()).Format("01/02 Mon 15:04")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// Labels names accounts for notifications: the alias when set, otherwise
// the provider plus the last two characters of the email user name.
func Labels(accounts []router.AccountView) map[string]string {
	count := map[string]int{}
	for _, a := range accounts {
		count[a.Provider]++
	}
	out := make(map[string]string, len(accounts))
	for _, a := range accounts {
		base := providerName(a.Provider)
		switch {
		case a.Alias != "":
			out[a.ID] = base + "#" + a.Alias
		case count[a.Provider] > 1:
			out[a.ID] = base + "#" + suffix(a.Display)
		default:
			out[a.ID] = base
		}
	}
	return out
}

func providerName(p string) string {
	switch p {
	case "codex":
		// "GPT" keeps Codex and Claude from starting with the same letter.
		return "GPT"
	case "claude":
		return "Claude"
	}
	if p == "" {
		return "?"
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

func suffix(display string) string {
	user := display
	if i := strings.Index(user, "@"); i >= 0 {
		user = user[:i]
	}
	user = strings.Trim(user, `"' `)
	if r := []rune(user); len(r) > 2 {
		return string(r[len(r)-2:])
	}
	if user == "" {
		return "?"
	}
	return user
}
