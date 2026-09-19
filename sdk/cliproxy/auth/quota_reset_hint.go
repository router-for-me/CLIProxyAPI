package auth

import (
	"regexp"
	"strings"
	"time"
)

// maxQuotaResetHint bounds how far in the future a parsed reset hint may place a
// credential cooldown. Monthly quotas can reset weeks out, so the bound is
// generous while still guarding against a misparsed timestamp benching a
// credential indefinitely.
const maxQuotaResetHint = 45 * 24 * time.Hour

// quotaResetHintRe matches a timestamp that follows a "reset" keyword.
//
// Subscription plans report an exhausted quota this way instead of only
// signalling retry-after. Volcengine Ark Agent Plan (方舟 Agent Plan,
// https://www.volcengine.com/activity/agentplan), used through its /api/plan
// endpoint, answers with:
//
//	{"error":{"code":"AccountQuotaExceeded","message":"You have exceeded the
//	 monthly usage quota. It will reset at 2026-09-23 23:59:59 +0800 CST.
//	 We recommend upgrading your plan for more quota, or waiting for the
//	 reset."}}
//
// RFC3339 style hints such as `"reset_at":"2026-09-23T23:59:59+08:00"` are
// accepted as well.
var quotaResetHintRe = regexp.MustCompile(`(?i)reset(?:s|ting)?[^0-9]{0,24}([0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\s*(?:[+-][0-9]{4}\s*[A-Za-z]{0,6}|[+-][0-9]{2}:[0-9]{2}|Z))?)`)

// quotaResetHintLayouts lists accepted timestamp layouts, most specific first.
var quotaResetHintLayouts = []string{
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05 -07:00",
	"2006-01-02 15:04:05 MST",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05Z",
}

// quotaResetHint extracts a credential recovery deadline from an upstream error
// body, such as the reset time reported by Volcengine Ark Agent Plan. Hints in
// the past, without an explicit offset/zone, or further out than
// maxQuotaResetHint are ignored so a malformed message can never bench a
// credential indefinitely.
//
// Callers only ever extend an existing cooldown with the returned deadline.
func quotaResetHint(text string, now time.Time) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}
	match := quotaResetHintRe.FindStringSubmatch(text)
	if len(match) < 2 {
		return time.Time{}, false
	}
	raw := strings.Join(strings.Fields(strings.TrimSpace(match[1])), " ")
	for _, layout := range quotaResetHintLayouts {
		parsed, errParse := time.Parse(layout, raw)
		if errParse != nil {
			continue
		}
		if parsed.IsZero() {
			continue
		}
		if !parsed.After(now) {
			continue
		}
		if parsed.Sub(now) > maxQuotaResetHint {
			continue
		}
		return parsed, true
	}
	return time.Time{}, false
}
