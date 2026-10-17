package helps

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/tidwall/gjson"
)

// MetaSubscriptionUsageEventType is the Responses stream event Meta sends after
// response.completed with the account's subscription usage.
const MetaSubscriptionUsageEventType = "response.subscription_usage"

// Quota signal names for Meta subscription usage. They follow the X-Codex-*
// pattern so the passive quota snapshot and the management API treat both alike.
const (
	MetaQuotaTierHeader          = "X-Meta-Tier"
	MetaQuotaWindowUsedHeader    = "X-Meta-Window-Used-Percent"
	MetaQuotaWindowMinutesHeader = "X-Meta-Window-Minutes"
	MetaQuotaWindowResetHeader   = "X-Meta-Window-Reset-At"
	MetaQuotaWeeklyUsedHeader    = "X-Meta-Weekly-Used-Percent"
	MetaQuotaWeeklyResetHeader   = "X-Meta-Weekly-Reset-At"
)

// ParseMetaSubscriptionUsageHeaders converts one Meta response.subscription_usage
// event into the bounded header representation used for passive quota
// observations. Meta sends it once per response, after response.completed:
//
//	{"type":"response.subscription_usage","subscription":{"tier":"…",
//	 "window":{"used_percent":0,"window_duration_mins":300,"resets_at":1791465557},
//	 "weekly":{"used_percent":5,"resets_at":1791763200}}}
//
// window is the five-hour-class block and weekly the rolling week. Percentages
// pass through verbatim (above 100 means over quota) and reset times stay unix
// seconds. A window without a percentage or reset time is dropped, and an event
// with no usable window returns nil.
func ParseMetaSubscriptionUsageHeaders(payload []byte) http.Header {
	if len(payload) == 0 || gjson.GetBytes(payload, "type").String() != MetaSubscriptionUsageEventType {
		return nil
	}
	return metaSubscriptionUsageHeaders(gjson.GetBytes(payload, "subscription"))
}

// ParseMetaSubscriptionUsageSSE finds the last response.subscription_usage
// event in a buffered Responses event stream.
func ParseMetaSubscriptionUsageSSE(body []byte) http.Header {
	var headers http.Header
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if parsed := ParseMetaSubscriptionUsageHeaders(bytes.TrimSpace(line[len("data:"):])); parsed != nil {
			headers = parsed
		}
	}
	return headers
}

// ObserveMetaSubscriptionUsage records a subscription usage event on the request
// context. The conductor stores it on the credential when the request finishes.
func ObserveMetaSubscriptionUsage(ctx context.Context, payload []byte) {
	logging.MergeResponseHeaders(ctx, ParseMetaSubscriptionUsageHeaders(payload))
}

// ObserveMetaSubscriptionUsageSSE records the subscription usage carried by a
// buffered Responses event stream.
func ObserveMetaSubscriptionUsageSSE(ctx context.Context, body []byte) {
	logging.MergeResponseHeaders(ctx, ParseMetaSubscriptionUsageSSE(body))
}

func metaSubscriptionUsageHeaders(subscription gjson.Result) http.Header {
	if !subscription.IsObject() {
		return nil
	}
	headers := make(http.Header)
	window := subscription.Get("window")
	minutes := window.Get("window_duration_mins")
	if used, reset, ok := metaQuotaWindow(window); ok && minutes.Type == gjson.Number && minutes.Int() > 0 {
		headers.Set(MetaQuotaWindowUsedHeader, used)
		headers.Set(MetaQuotaWindowMinutesHeader, strconv.FormatInt(minutes.Int(), 10))
		headers.Set(MetaQuotaWindowResetHeader, reset)
	}
	if used, reset, ok := metaQuotaWindow(subscription.Get("weekly")); ok {
		headers.Set(MetaQuotaWeeklyUsedHeader, used)
		headers.Set(MetaQuotaWeeklyResetHeader, reset)
	}
	if len(headers) == 0 {
		return nil
	}
	// The tier is an opaque plan id. It only labels the snapshot, so a value that
	// could forge a request-log line is dropped rather than failing the event.
	if tier := strings.TrimSpace(subscription.Get("tier").String()); tier != "" && len(tier) <= 128 && !strings.ContainsAny(tier, "\r\n") {
		headers.Set(MetaQuotaTierHeader, tier)
	}
	return headers
}

func metaQuotaWindow(window gjson.Result) (used, reset string, ok bool) {
	if !window.IsObject() {
		return "", "", false
	}
	usedPercent := window.Get("used_percent")
	resetsAt := window.Get("resets_at")
	if usedPercent.Type != gjson.Number || usedPercent.Float() < 0 || resetsAt.Type != gjson.Number || resetsAt.Int() <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(usedPercent.Raw), strconv.FormatInt(resetsAt.Int(), 10), true
}
