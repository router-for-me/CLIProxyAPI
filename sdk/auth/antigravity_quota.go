package auth

import (
	"bytes"
	"context"
	"math"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const (
	AntigravityQuotaSummaryPath    = "/v1internal:retrieveUserQuotaSummary"
	AntigravityQuotaWindowFiveHour = "5h"
	AntigravityQuotaWindowWeekly   = "weekly"
)

// AntigravityQuotaBucket is one usable bucket from the quota summary.
type AntigravityQuotaBucket struct {
	ID                string
	Window            string
	GroupDisplayName  string
	Description       string
	RemainingFraction float64
	ResetTime         time.Time
}

// FetchAntigravityQuotaSummary merges quota buckets in host order. An empty success permits catalog fallback.
func FetchAntigravityQuotaSummary(ctx context.Context, req AntigravityModelProbeRequest) ([]AntigravityQuotaBucket, AntigravityProbeStatus) {
	var merged []AntigravityQuotaBucket
	seen := make(map[string]struct{})
	hasSuccess := false
	hasFailure := false
	failureStatus := AntigravityProbeStatusTransientError
	for _, baseURL := range req.BaseURLs {
		hostReq := req
		hostReq.BaseURLs = []string{baseURL}
		buckets, hostStatus := fetchAntigravityQuotaSummary(ctx, hostReq)
		switch hostStatus {
		case AntigravityProbeStatusSuccess:
			hasSuccess = true
		case AntigravityProbeStatusAuthError:
			hasFailure = true
			failureStatus = hostStatus
		case AntigravityProbeStatusForbidden:
			hasFailure = true
			if failureStatus == AntigravityProbeStatusTransientError {
				failureStatus = hostStatus
			}
		default:
			hasFailure = true
		}
		for _, bucket := range buckets {
			if _, exists := seen[bucket.ID]; !exists {
				seen[bucket.ID] = struct{}{}
				merged = append(merged, bucket)
			}
		}
	}
	if len(merged) > 0 {
		return merged, AntigravityProbeStatusSuccess
	}
	if hasFailure {
		return nil, failureStatus
	}
	if hasSuccess {
		return nil, AntigravityProbeStatusSuccess
	}
	return nil, AntigravityProbeStatusTransientError
}

func fetchAntigravityQuotaSummary(ctx context.Context, req AntigravityModelProbeRequest) ([]AntigravityQuotaBucket, AntigravityProbeStatus) {
	data, status := fetchAntigravityProbe(ctx, req, AntigravityQuotaSummaryPath)
	if status != AntigravityProbeStatusSuccess || len(bytes.TrimSpace(data)) == 0 {
		return nil, status
	}
	if !ojson.ValidBytes(data) {
		return nil, AntigravityProbeStatusTransientError
	}
	top := ojson.ParseBytes(data)
	if !top.IsObject() {
		return nil, AntigravityProbeStatusTransientError
	}
	return parseAntigravityQuotaBuckets(top), AntigravityProbeStatusSuccess
}

func parseAntigravityQuotaBuckets(top ojson.Result) []AntigravityQuotaBucket {
	var buckets []AntigravityQuotaBucket
	seen := make(map[string]struct{})
	appendBuckets := func(items []ojson.Result, groupName string) {
		for _, item := range items {
			id := NormalizeAntigravityModelID(item.Get("bucketId").Str)
			window := item.Get("window").Str
			if id == "" || item.Get("disabled").Type == ojson.True ||
				(window != AntigravityQuotaWindowFiveHour && window != AntigravityQuotaWindowWeekly) {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			fraction, reset, ok := parseAntigravityQuotaValues(item)
			if !ok {
				continue
			}
			buckets = append(buckets, AntigravityQuotaBucket{
				ID: id, Window: strings.Clone(window), GroupDisplayName: groupName,
				Description: strings.Clone(item.Get("description").Str), RemainingFraction: fraction, ResetTime: reset,
			})
			seen[id] = struct{}{}
		}
	}
	hasGroupedBuckets := false
	for _, group := range top.Get("groups").Array() {
		items := group.Get("buckets").Array()
		if len(items) > 0 {
			hasGroupedBuckets = true
			appendBuckets(items, strings.Clone(strings.TrimSpace(group.Get("displayName").Str)))
		}
	}
	if !hasGroupedBuckets {
		appendBuckets(top.Get("buckets").Array(), "")
	}
	return buckets
}

func addAntigravityModelQuota(quotas map[string]AntigravityModelQuota, id string, model ojson.Result, field string) map[string]AntigravityModelQuota {
	if _, exists := quotas[id]; exists || model.Get("disabled").Type == ojson.True {
		return quotas
	}
	info := model.Get(field)
	if !info.IsObject() {
		return quotas
	}
	fraction, reset, ok := parseAntigravityQuotaValues(info)
	if !ok || fraction < 0 || fraction > 1 {
		return quotas
	}
	if quotas == nil {
		quotas = make(map[string]AntigravityModelQuota)
	}
	quotas[id] = AntigravityModelQuota{
		ModelID: id, DisplayName: strings.Clone(model.Get("displayName").Str),
		ModelProvider:     strings.Clone(model.Get("modelProvider").Str),
		RemainingFraction: fraction, ResetTime: reset,
	}
	return quotas
}

func parseAntigravityQuotaValues(info ojson.Result) (float64, time.Time, bool) {
	reset, _ := time.Parse(time.RFC3339Nano, info.Get("resetTime").Str)
	remaining := info.Get("remainingFraction")
	if !remaining.Exists() {
		// Proto3 omits a zero fraction. A valid reset distinguishes it from missing quota.
		return 0, reset, !reset.IsZero()
	}
	return remaining.Num, reset, remaining.Type == ojson.Number && !math.IsNaN(remaining.Num) && !math.IsInf(remaining.Num, 0)
}
