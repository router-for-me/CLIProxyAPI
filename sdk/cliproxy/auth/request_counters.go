package auth

import (
	"sort"
	"strings"
	"time"
)

// RequestCounterBucket is a persisted per-credential request-count bucket.
// BucketID carries the absolute bucket index (unix seconds divided by the
// bucket width) so restored buckets stay aligned with wall-clock time and
// buckets that have aged out of the ring are dropped instead of shifted.
type RequestCounterBucket struct {
	BucketID int64 `json:"bucket_id"`
	Success  int64 `json:"success"`
	Failed   int64 `json:"failed"`
}

// AuthRequestCounts is a persisted snapshot of one credential's request
// counters. It is the durable counterpart of the in-memory Auth.Success,
// Auth.Failed and Auth.recentRequests fields.
type AuthRequestCounts struct {
	Provider       string                 `json:"provider,omitempty"`
	AuthID         string                 `json:"auth_id"`
	AuthIndex      string                 `json:"auth_index,omitempty"`
	Success        int64                  `json:"success"`
	Failed         int64                  `json:"failed"`
	RecentRequests []RequestCounterBucket `json:"recent_requests,omitempty"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

func (a *Auth) requestCounterBuckets() []RequestCounterBucket {
	if a == nil {
		return nil
	}
	out := make([]RequestCounterBucket, 0, recentRequestBucketCount)
	for i := range a.recentRequests.buckets {
		bucket := a.recentRequests.buckets[i]
		if bucket.bucketID == 0 {
			continue
		}
		out = append(out, RequestCounterBucket{
			BucketID: bucket.bucketID,
			Success:  bucket.success,
			Failed:   bucket.failed,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BucketID < out[j].BucketID })
	return out
}

// RequestCountersRevision returns a revision that advances whenever any
// credential request counter changes. It is a cheap atomic read suitable for
// polling by a background persistence loop.
func (m *Manager) RequestCountersRevision() uint64 {
	if m == nil {
		return 0
	}
	return m.requestCountersSeq.Load()
}

// RequestCountersSnapshot returns the current per-credential request counters
// in a stable order so the persisted file is reproducible.
func (m *Manager) RequestCountersSnapshot() []AuthRequestCounts {
	if m == nil {
		return nil
	}
	now := time.Now()
	m.mu.RLock()
	out := make([]AuthRequestCounts, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		out = append(out, AuthRequestCounts{
			Provider:       strings.TrimSpace(auth.Provider),
			AuthID:         auth.ID,
			AuthIndex:      auth.Index,
			Success:        auth.Success,
			Failed:         auth.Failed,
			RecentRequests: auth.requestCounterBuckets(),
			UpdatedAt:      now,
		})
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthID != out[j].AuthID {
			return out[i].AuthID < out[j].AuthID
		}
		return out[i].AuthIndex < out[j].AuthIndex
	})
	return out
}

// ApplyRequestCounters seeds persisted request counters into registered auths.
// Only currently registered credentials are updated and counters never move
// backwards, so a stale file cannot erase live counts. It returns the number of
// credentials that were updated.
func (m *Manager) ApplyRequestCounters(records []AuthRequestCounts) int {
	if m == nil || len(records) == 0 {
		return 0
	}
	currentBucketID := recentRequestBucketID(time.Now())
	oldestBucketID := currentBucketID - int64(recentRequestBucketCount) + 1

	m.mu.Lock()
	defer m.mu.Unlock()

	applied := 0
	for _, record := range records {
		authID := strings.TrimSpace(record.AuthID)
		if authID == "" {
			continue
		}
		auth := m.auths[authID]
		if auth == nil {
			continue
		}
		if record.Success > auth.Success {
			auth.Success = record.Success
		}
		if record.Failed > auth.Failed {
			auth.Failed = record.Failed
		}
		for _, bucket := range record.RecentRequests {
			if bucket.BucketID < oldestBucketID || bucket.BucketID > currentBucketID {
				continue
			}
			target := &auth.recentRequests.buckets[recentRequestBucketIndex(bucket.BucketID)]
			target.bucketID = bucket.BucketID
			target.success = bucket.Success
			target.failed = bucket.Failed
		}
		applied++
	}
	if applied > 0 {
		m.requestCountersSeq.Add(1)
	}
	return applied
}
