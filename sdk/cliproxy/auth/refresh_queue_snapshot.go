package auth

import (
	"sort"
	"time"
)

// RefreshQueueEntry is a read-only snapshot entry for an auth scheduled for auto refresh.
type RefreshQueueEntry struct {
	Auth          *Auth
	NextRefreshAt time.Time
}

type refreshQueueSchedule struct {
	id   string
	next time.Time
}

// RefreshQueueSnapshot returns a read-only snapshot of queued auth auto-refresh schedules.
func (m *Manager) RefreshQueueSnapshot(now time.Time) []RefreshQueueEntry {
	if m == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	m.mu.RLock()
	loop := m.refreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return nil
	}

	loop.applyDirty(now)
	schedules := loop.snapshot()
	if len(schedules) == 0 {
		return nil
	}

	out := make([]RefreshQueueEntry, 0, len(schedules))
	m.mu.RLock()
	for _, schedule := range schedules {
		if schedule.id == "" || schedule.next.IsZero() {
			continue
		}
		auth := m.auths[schedule.id]
		if auth == nil {
			continue
		}
		cloned := auth.Clone()
		if cloned == nil {
			continue
		}
		out = append(out, RefreshQueueEntry{
			Auth:          cloned,
			NextRefreshAt: schedule.next,
		})
	}
	m.mu.RUnlock()

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].NextRefreshAt.Before(out[j].NextRefreshAt)
	})
	return out
}

func (l *authAutoRefreshLoop) snapshot() []refreshQueueSchedule {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]refreshQueueSchedule, 0, len(l.queue))
	for _, item := range l.queue {
		if item == nil || item.id == "" || item.next.IsZero() {
			continue
		}
		out = append(out, refreshQueueSchedule{
			id:   item.id,
			next: item.next,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].next.Before(out[j].next)
	})
	return out
}
