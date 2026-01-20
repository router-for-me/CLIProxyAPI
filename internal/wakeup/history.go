package wakeup

import (
	"time"
)

const defaultHistoryMaxSize = 1000

// NewHistory creates a new History instance with the specified maximum size.
func NewHistory(maxSize int) *History {
	if maxSize <= 0 {
		maxSize = defaultHistoryMaxSize
	}
	return &History{
		records: make([]WakeupRecord, 0, maxSize),
		maxSize: maxSize,
	}
}

// Add adds a new record to the history, removing oldest entries if capacity is exceeded.
func (h *History) Add(record WakeupRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Prepend to keep newest first
	h.records = append([]WakeupRecord{record}, h.records...)

	// Trim to max size
	if len(h.records) > h.maxSize {
		h.records = h.records[:h.maxSize]
	}
}

// List returns records with optional filtering and pagination.
func (h *History) List(limit, offset int, scheduleID, accountID string) []WakeupRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var filtered []WakeupRecord
	for _, r := range h.records {
		if scheduleID != "" && r.ScheduleID != scheduleID {
			continue
		}
		if accountID != "" && r.AccountID != accountID {
			continue
		}
		filtered = append(filtered, r)
	}

	// Apply pagination
	if offset >= len(filtered) {
		return nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end]
}

// Count returns the total number of records, optionally filtered.
func (h *History) Count(scheduleID, accountID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if scheduleID == "" && accountID == "" {
		return len(h.records)
	}

	count := 0
	for _, r := range h.records {
		if scheduleID != "" && r.ScheduleID != scheduleID {
			continue
		}
		if accountID != "" && r.AccountID != accountID {
			continue
		}
		count++
	}
	return count
}

// LastExecution returns the time of the most recent execution, or nil if none.
func (h *History) LastExecution() *time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.records) == 0 {
		return nil
	}
	t := h.records[0].ExecutedAt
	return &t
}

// Clear removes all records from history.
func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = h.records[:0]
}

// GetByScheduleID returns all records for a specific schedule.
func (h *History) GetByScheduleID(scheduleID string, limit int) []WakeupRecord {
	return h.List(limit, 0, scheduleID, "")
}

// GetByAccountID returns all records for a specific account.
func (h *History) GetByAccountID(accountID string, limit int) []WakeupRecord {
	return h.List(limit, 0, "", accountID)
}

// Stats returns statistics about the history.
func (h *History) Stats() map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	stats := map[string]int{
		"total":   len(h.records),
		"success": 0,
		"failed":  0,
	}
	for _, r := range h.records {
		switch r.Status {
		case StatusSuccess:
			stats["success"]++
		case StatusFailed:
			stats["failed"]++
		}
	}
	return stats
}

