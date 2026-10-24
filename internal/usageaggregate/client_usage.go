package usageaggregate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// ClientKeyUsage attributes execution attempts to a client key, not an upstream credential.
// Only a fingerprint is retained; raw client keys never enter snapshots or persistence.
type ClientKeyUsage struct {
	Provider string                `json:"provider"`
	Buckets  []ClientTrafficBucket `json:"buckets"`
	ID       string                `json:"id"`
	Model    string                `json:"model"`
	Requests int64                 `json:"requests"`
	Failures int64                 `json:"failures"`
	Tokens   int64                 `json:"tokens"`
	LastUsed time.Time             `json:"last_used"`
}

type ClientTrafficBucket struct {
	Time    time.Time `json:"time"`
	Success int64     `json:"success"`
	Failed  int64     `json:"failed"`
}

func (a *aggregator) recordClientUsage(record coreusage.Record) {
	key := strings.TrimSpace(record.APIKey)
	if key == "" {
		return
	}
	digest := sha256.Sum256([]byte(key))
	id := hex.EncodeToString(digest[:])
	model := strings.TrimSpace(record.Alias)
	if model == "" {
		model = strings.TrimSpace(record.Model)
	}
	if model == "" {
		model = "unknown"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byClient == nil {
		a.byClient = make(map[string]*ClientKeyUsage)
	}
	bucketKey := id + "\x00" + model + "\x00" + record.Provider
	bucket := a.byClient[bucketKey]
	if bucket == nil {
		if len(a.byClient) >= maxTrackedCredentials {
			return
		}
		bucket = &ClientKeyUsage{ID: id, Model: model, Provider: record.Provider}
		a.byClient[bucketKey] = bucket
	}
	bucket.Requests++
	at := record.RequestedAt.Truncate(10 * time.Minute)
	if !at.IsZero() {
		found := false
		for i := range bucket.Buckets {
			if bucket.Buckets[i].Time.Equal(at) {
				if record.Failed {
					bucket.Buckets[i].Failed++
				} else {
					bucket.Buckets[i].Success++
				}
				found = true
				break
			}
		}
		if !found {
			next := ClientTrafficBucket{Time: at}
			if record.Failed {
				next.Failed = 1
			} else {
				next.Success = 1
			}
			bucket.Buckets = append(bucket.Buckets, next)
		}
		cutoff := record.RequestedAt.Add(-200 * time.Minute)
		retained := bucket.Buckets[:0]
		for _, b := range bucket.Buckets {
			if b.Time.After(cutoff) {
				retained = append(retained, b)
			}
		}
		bucket.Buckets = retained
	}
	if record.Failed {
		bucket.Failures++
	}
	bucket.Tokens += breakdownFor(record.Detail).TotalTokens
	if record.RequestedAt.After(bucket.LastUsed) {
		bucket.LastUsed = record.RequestedAt
	}
	a.revision.Add(1)
}

// restoreClientUsage is called while the aggregator mutex is held.
func (a *aggregator) restoreClientUsage(rows []ClientKeyUsage) {
	a.byClient = make(map[string]*ClientKeyUsage)
	for _, row := range rows {
		if len(a.byClient) >= maxTrackedCredentials {
			break
		}
		copyRow := row
		a.byClient[row.ID+"\x00"+row.Model+"\x00"+row.Provider] = &copyRow
	}
}
