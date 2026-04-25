package usage

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

const (
	defaultRequestEventBufferSize = 5000
	defaultRequestEventLimit      = 500
	maxRequestEventLimit          = 2000
)

type RequestEvent struct {
	EventID            uint64     `json:"event_id"`
	Timestamp          time.Time  `json:"timestamp"`
	Model              string     `json:"model"`
	Source             string     `json:"source"`
	AuthIndex          string     `json:"auth_index"`
	Failed             bool       `json:"failed"`
	FailureStage       string     `json:"failure_stage,omitempty"`
	ErrorCode          string     `json:"error_code,omitempty"`
	ErrorMessage       string     `json:"error_message,omitempty"`
	StatusCode         int        `json:"status_code,omitempty"`
	RequestID          string     `json:"request_id,omitempty"`
	RequestLogRef      string     `json:"request_log_ref,omitempty"`
	AttemptCount       int        `json:"attempt_count,omitempty"`
	UpstreamRequestIDs []string   `json:"upstream_request_ids,omitempty"`
	Tokens             TokenStats `json:"tokens"`
}

type RequestEventSubscription struct {
	ch         chan RequestEvent
	overflowed atomic.Bool
	closed     atomic.Bool
}

func (s *RequestEventSubscription) C() <-chan RequestEvent {
	if s == nil {
		return nil
	}
	return s.ch
}

func (s *RequestEventSubscription) Overflowed() bool {
	return s != nil && s.overflowed.Load()
}

type RequestEventHub struct {
	mu          sync.RWMutex
	buffer      []RequestEvent
	start       int
	size        int
	nextID      uint64
	nextSubID   uint64
	subscribers map[uint64]*RequestEventSubscription
}

type RequestEventQuery struct {
	TimeRange string
	Limit     int
}

type RequestEventPage struct {
	Items         []RequestEvent `json:"items"`
	LatestEventID uint64         `json:"latest_event_id"`
}

type RequestEventPlugin struct {
	hub *RequestEventHub
}

var defaultRequestEventHub = NewRequestEventHub(defaultRequestEventBufferSize)

func init() {
	coreusage.RegisterPlugin(NewRequestEventPlugin())
}

func NewRequestEventPlugin() *RequestEventPlugin {
	return &RequestEventPlugin{hub: defaultRequestEventHub}
}

func (p *RequestEventPlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() || p == nil || p.hub == nil {
		return
	}
	p.hub.Publish(record)
}

func GetRequestEventHub() *RequestEventHub { return defaultRequestEventHub }

func NewRequestEventHub(capacity int) *RequestEventHub {
	if capacity <= 0 {
		capacity = defaultRequestEventBufferSize
	}
	return &RequestEventHub{
		buffer:      make([]RequestEvent, capacity),
		subscribers: make(map[uint64]*RequestEventSubscription),
	}
}

func (h *RequestEventHub) CurrentID() uint64 {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nextID
}

func (h *RequestEventHub) Publish(record coreusage.Record) RequestEvent {
	if h == nil {
		return RequestEvent{}
	}

	event, subscribers := h.append(record)
	for _, sub := range subscribers {
		if sub == nil || sub.closed.Load() || sub.overflowed.Load() {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			sub.overflowed.Store(true)
		}
	}
	return event
}

func (h *RequestEventHub) append(record coreusage.Record) (RequestEvent, []*RequestEventSubscription) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++
	event := requestEventFromRecord(h.nextID, record)

	if len(h.buffer) > 0 {
		if h.size < len(h.buffer) {
			idx := (h.start + h.size) % len(h.buffer)
			h.buffer[idx] = event
			h.size++
		} else {
			h.buffer[h.start] = event
			h.start = (h.start + 1) % len(h.buffer)
		}
	}

	subscribers := make([]*RequestEventSubscription, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		subscribers = append(subscribers, sub)
	}

	return event, subscribers
}

func (h *RequestEventHub) Subscribe(sinceID uint64) (*RequestEventSubscription, []RequestEvent, bool) {
	if h == nil {
		return nil, nil, false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	backlog, resetRequired := h.eventsAfterLocked(sinceID)
	if resetRequired {
		return nil, nil, true
	}

	h.nextSubID++
	sub := &RequestEventSubscription{ch: make(chan RequestEvent, 128)}
	h.subscribers[h.nextSubID] = sub
	return sub, backlog, false
}

func (h *RequestEventHub) Unsubscribe(sub *RequestEventSubscription) {
	if h == nil || sub == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for id, current := range h.subscribers {
		if current != sub {
			continue
		}
		delete(h.subscribers, id)
		if !sub.closed.Swap(true) {
			close(sub.ch)
		}
		return
	}
}

func (h *RequestEventHub) eventsAfterLocked(sinceID uint64) ([]RequestEvent, bool) {
	ordered := h.snapshotLocked()
	if len(ordered) == 0 {
		return nil, false
	}

	oldestID := ordered[0].EventID
	latestID := ordered[len(ordered)-1].EventID
	if sinceID != 0 && sinceID < oldestID && sinceID < latestID {
		return nil, true
	}

	if sinceID == 0 {
		return nil, false
	}

	index := sort.Search(len(ordered), func(i int) bool {
		return ordered[i].EventID > sinceID
	})
	if index >= len(ordered) {
		return nil, false
	}
	return append([]RequestEvent(nil), ordered[index:]...), false
}

func (h *RequestEventHub) snapshotLocked() []RequestEvent {
	if h.size == 0 {
		return nil
	}

	out := make([]RequestEvent, 0, h.size)
	for i := 0; i < h.size; i++ {
		idx := (h.start + i) % len(h.buffer)
		out = append(out, h.buffer[idx])
	}
	return out
}

func BuildRequestEventPage(stats *RequestStatistics, hub *RequestEventHub, query RequestEventQuery, now time.Time) RequestEventPage {
	page := RequestEventPage{}
	if hub != nil {
		page.LatestEventID = hub.CurrentID()
	}
	if stats == nil {
		return page
	}

	limit := normalizeRequestEventLimit(query.Limit)
	cutoff := resolveRequestEventCutoff(query.TimeRange, now)
	snapshot := stats.Snapshot()
	items := make([]RequestEvent, 0)

	for _, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if !cutoff.IsZero() && detail.Timestamp.Before(cutoff) {
					continue
				}
				items = append(items, RequestEvent{
					Timestamp:          detail.Timestamp.UTC(),
					Model:              strings.TrimSpace(modelName),
					Source:             detail.Source,
					AuthIndex:          detail.AuthIndex,
					Failed:             detail.Failed,
					FailureStage:       detail.FailureStage,
					ErrorCode:          detail.ErrorCode,
					ErrorMessage:       detail.ErrorMessage,
					StatusCode:         detail.StatusCode,
					RequestID:          detail.RequestID,
					RequestLogRef:      detail.RequestLogRef,
					AttemptCount:       detail.AttemptCount,
					UpstreamRequestIDs: append([]string(nil), detail.UpstreamRequestIDs...),
					Tokens:             detail.Tokens,
				})
			}
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Timestamp.Equal(items[j].Timestamp) {
			if items[i].RequestID == items[j].RequestID {
				return items[i].Model < items[j].Model
			}
			return items[i].RequestID > items[j].RequestID
		}
		return items[i].Timestamp.After(items[j].Timestamp)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	page.Items = items
	return page
}

func requestEventFromRecord(id uint64, record coreusage.Record) RequestEvent {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := normaliseDetail(record.Detail)
	return RequestEvent{
		EventID:            id,
		Timestamp:          timestamp.UTC(),
		Model:              strings.TrimSpace(record.Model),
		Source:             record.Source,
		AuthIndex:          record.AuthIndex,
		Failed:             record.Failed,
		FailureStage:       strings.TrimSpace(record.FailureStage),
		ErrorCode:          strings.TrimSpace(record.ErrorCode),
		ErrorMessage:       strings.TrimSpace(record.ErrorMessage),
		StatusCode:         record.StatusCode,
		RequestID:          strings.TrimSpace(record.RequestID),
		RequestLogRef:      strings.TrimSpace(record.RequestLogRef),
		AttemptCount:       maxInt(record.AttemptCount, 0),
		UpstreamRequestIDs: append([]string(nil), record.UpstreamRequestIDs...),
		Tokens:             detail,
	}
}

func normalizeRequestEventLimit(limit int) int {
	if limit <= 0 {
		return defaultRequestEventLimit
	}
	if limit > maxRequestEventLimit {
		return maxRequestEventLimit
	}
	return limit
}

func resolveRequestEventCutoff(timeRange string, now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	switch strings.TrimSpace(strings.ToLower(timeRange)) {
	case "", "24h":
		return now.Add(-24 * time.Hour)
	case "7h":
		return now.Add(-7 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "all":
		return time.Time{}
	default:
		return now.Add(-24 * time.Hour)
	}
}
