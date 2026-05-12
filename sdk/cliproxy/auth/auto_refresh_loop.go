package auth

import (
	"container/heap"
	"context"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type authAutoRefreshLoop struct {
	manager     *Manager
	interval    time.Duration
	concurrency int

	mu    sync.Mutex
	queue refreshMinHeap
	index map[string]*refreshHeapItem
	dirty map[string]struct{}

	wakeCh chan struct{}
	jobs   chan string
}

func newAuthAutoRefreshLoop(manager *Manager, interval time.Duration, concurrency int) *authAutoRefreshLoop {
	if interval <= 0 {
		interval = refreshCheckInterval
	}
	if concurrency <= 0 {
		concurrency = refreshMaxConcurrency
	}
	jobBuffer := concurrency * 4
	if jobBuffer < 64 {
		jobBuffer = 64
	}
	return &authAutoRefreshLoop{
		manager:     manager,
		interval:    interval,
		concurrency: concurrency,
		index:       make(map[string]*refreshHeapItem),
		dirty:       make(map[string]struct{}),
		wakeCh:      make(chan struct{}, 1),
		jobs:        make(chan string, jobBuffer),
	}
}

func (l *authAutoRefreshLoop) queueReschedule(authID string) {
	if l == nil || authID == "" {
		return
	}
	l.mu.Lock()
	l.dirty[authID] = struct{}{}
	l.mu.Unlock()
	select {
	case l.wakeCh <- struct{}{}:
	default:
	}
}

func (l *authAutoRefreshLoop) run(ctx context.Context) {
	if l == nil || l.manager == nil {
		return
	}

	workers := l.concurrency
	if workers <= 0 {
		workers = refreshMaxConcurrency
	}
	for i := 0; i < workers; i++ {
		go l.worker(ctx)
	}

	l.loop(ctx)
}

func (l *authAutoRefreshLoop) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case authID := <-l.jobs:
			if authID == "" {
				continue
			}
			l.manager.refreshAuth(ctx, authID)
			l.queueReschedule(authID)
		}
	}
}

func (l *authAutoRefreshLoop) rebuild(now time.Time) {
	type entry struct {
		id   string
		next time.Time
	}

	entries := make([]entry, 0)

	l.manager.mu.RLock()
	for id, auth := range l.manager.auths {
		next, ok := nextRefreshCheckAt(now, auth, l.interval)
		if !ok {
			continue
		}
		entries = append(entries, entry{id: id, next: next})
	}
	l.manager.mu.RUnlock()

	l.mu.Lock()
	l.queue = l.queue[:0]
	l.index = make(map[string]*refreshHeapItem, len(entries))
	for _, e := range entries {
		item := &refreshHeapItem{id: e.id, next: e.next}
		heap.Push(&l.queue, item)
		l.index[e.id] = item
	}
	l.mu.Unlock()
}

func (l *authAutoRefreshLoop) loop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	var timerCh <-chan time.Time
	l.resetTimer(timer, &timerCh, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wakeCh:
			now := time.Now()
			l.applyDirty(now)
			l.resetTimer(timer, &timerCh, now)
		case <-timerCh:
			now := time.Now()
			l.handleDue(ctx, now)
			l.applyDirty(now)
			l.resetTimer(timer, &timerCh, now)
		}
	}
}

func (l *authAutoRefreshLoop) resetTimer(timer *time.Timer, timerCh *<-chan time.Time, now time.Time) {
	next, ok := l.peek()
	if !ok {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		*timerCh = nil
		return
	}

	wait := next.Sub(now)
	if wait < 0 {
		wait = 0
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(wait)
	*timerCh = timer.C
}

func (l *authAutoRefreshLoop) peek() (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) == 0 {
		return time.Time{}, false
	}
	return l.queue[0].next, true
}

func (l *authAutoRefreshLoop) handleDue(ctx context.Context, now time.Time) {
	due := l.popDue(now)
	if len(due) == 0 {
		return
	}
	if log.IsLevelEnabled(log.DebugLevel) {
		log.Debugf("auto-refresh scheduler due auths: %d", len(due))
	}
	for _, authID := range due {
		l.handleDueAuth(ctx, now, authID)
	}
}

func (l *authAutoRefreshLoop) popDue(now time.Time) []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var due []string
	for len(l.queue) > 0 {
		item := l.queue[0]
		if item == nil || item.next.After(now) {
			break
		}
		popped := heap.Pop(&l.queue).(*refreshHeapItem)
		if popped == nil {
			continue
		}
		delete(l.index, popped.id)
		due = append(due, popped.id)
	}
	return due
}

func (l *authAutoRefreshLoop) handleDueAuth(ctx context.Context, now time.Time, authID string) {
	if authID == "" {
		return
	}

	manager := l.manager

	manager.mu.RLock()
	auth := manager.auths[authID]
	if auth == nil {
		manager.mu.RUnlock()
		return
	}
	next, shouldSchedule := nextRefreshCheckAt(now, auth, l.interval)
	shouldRefresh := manager.shouldRefresh(auth, now)
	exec := manager.executors[auth.Provider]
	manager.mu.RUnlock()

	if !shouldSchedule {
		l.remove(authID)
		return
	}

	if !shouldRefresh {
		l.upsert(authID, next)
		return
	}

	if exec == nil {
		l.upsert(authID, now.Add(l.interval))
		return
	}

	if !manager.markRefreshPending(authID, now) {
		manager.mu.RLock()
		auth = manager.auths[authID]
		next, shouldSchedule = nextRefreshCheckAt(now, auth, l.interval)
		manager.mu.RUnlock()
		if shouldSchedule {
			l.upsert(authID, next)
		} else {
			l.remove(authID)
		}
		return
	}

	select {
	case <-ctx.Done():
		return
	case l.jobs <- authID:
	}
}

func (l *authAutoRefreshLoop) applyDirty(now time.Time) {
	dirty := l.drainDirty()
	if len(dirty) == 0 {
		return
	}

	for _, authID := range dirty {
		l.manager.mu.RLock()
		auth := l.manager.auths[authID]
		next, ok := nextRefreshCheckAt(now, auth, l.interval)
		l.manager.mu.RUnlock()

		if !ok {
			l.remove(authID)
			continue
		}
		l.upsert(authID, next)
	}
}

func (l *authAutoRefreshLoop) drainDirty() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.dirty))
	for authID := range l.dirty {
		out = append(out, authID)
		delete(l.dirty, authID)
	}
	return out
}

func (l *authAutoRefreshLoop) upsert(authID string, next time.Time) {
	if authID == "" || next.IsZero() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if item, ok := l.index[authID]; ok && item != nil {
		item.next = next
		heap.Fix(&l.queue, item.index)
		return
	}
	item := &refreshHeapItem{id: authID, next: next}
	heap.Push(&l.queue, item)
	l.index[authID] = item
}

func (l *authAutoRefreshLoop) remove(authID string) {
	if authID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item, ok := l.index[authID]
	if !ok || item == nil {
		return
	}
	heap.Remove(&l.queue, item.index)
	delete(l.index, authID)
}

func nextRefreshCheckAt(now time.Time, auth *Auth, interval time.Duration) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}

	accountType, _ := auth.AccountInfo()
	if accountType == "api_key" {
		return time.Time{}, false
	}

	if !auth.NextRefreshAfter.IsZero() && now.Before(auth.NextRefreshAfter) {
		return auth.NextRefreshAfter, true
	}

	if evaluator, ok := auth.Runtime.(RefreshEvaluator); ok && evaluator != nil {
		if interval <= 0 {
			interval = refreshCheckInterval
		}
		return now.Add(interval), true
	}

	lastRefresh := auth.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(auth); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := auth.ExpirationTime()

	if pref := authPreferredInterval(auth); pref > 0 {
		candidates := make([]time.Time, 0, 2)
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) || expiry.Sub(now) <= pref {
				return now, true
			}
			candidates = append(candidates, expiry.Add(-pref))
		}
		if lastRefresh.IsZero() {
			return now, true
		}
		candidates = append(candidates, lastRefresh.Add(pref))
		next := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate.Before(next) {
				next = candidate
			}
		}
		if !next.After(now) {
			return now, true
		}
		return next, true
	}

	provider := strings.ToLower(auth.Provider)
	lead := ProviderRefreshLead(provider, auth.Runtime)
	if lead == nil {
		return time.Time{}, false
	}
	if hasExpiry && !expiry.IsZero() {
		dueAt := expiry.Add(-*lead)
		if !dueAt.After(now) {
			return now, true
		}
		return dueAt, true
	}
	if !lastRefresh.IsZero() {
		dueAt := lastRefresh.Add(*lead)
		if !dueAt.After(now) {
			return now, true
		}
		return dueAt, true
	}
	return now, true
}

type refreshHeapItem struct {
	id    string
	next  time.Time
	index int
}

type refreshMinHeap []*refreshHeapItem

func (h refreshMinHeap) Len() int { return len(h) }

func (h refreshMinHeap) Less(i, j int) bool {
	return h[i].next.Before(h[j].next)
}

func (h refreshMinHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *refreshMinHeap) Push(x any) {
	item, ok := x.(*refreshHeapItem)
	if !ok || item == nil {
		return
	}
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *refreshMinHeap) Pop() any {
	old := *h
	n := len(old)
	if n == 0 {
		return (*refreshHeapItem)(nil)
	}
	item := old[n-1]
	item.index = -1
	*h = old[:n-1]
	return item
}
