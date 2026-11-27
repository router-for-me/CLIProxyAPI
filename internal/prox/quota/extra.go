package quota

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
)

// Daily is a 24-hour window (monitor-only providers such as Kimi).
const Daily Kind = "daily"

// Endpoints for monitor-only providers, as used by the CPA Management Center.
const (
	KimiUsageURL       = "https://api.kimi.com/coding/v1/usages"
	KimiAIUsageURL     = "https://api.kimi.ai/coding/v1/usages"
	XaiBillingWeekly   = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	XaiBillingMonthly  = "https://cli-chat-proxy.grok.com/v1/billing"
	xaiTokenAuthValue  = "xai-grok-cli"
	xaiClientIDValue   = "grok-shell"
	xaiUserAgentFormat = "grok-pager/%[1]s grok-shell/%[1]s (linux; x86_64)"
)

// KindForPeriod maps a window length to a Kind.
func KindForPeriod(hours float64) Kind {
	switch {
	case hours <= 0:
		return Weekly
	case hours <= 6:
		return FiveHour
	case hours <= 36:
		return Daily
	case hours <= 10*24:
		return Weekly
	}
	return Monthly
}

// GetJSON performs an authenticated GET through the account's proxy and
// returns the body of a 200 response.
func (f *HTTPFetcher) GetJSON(ctx context.Context, proxyURL, url string, headers map[string]string) ([]byte, error) {
	client, err := f.clientFor(proxyURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &Error{Code: "quota_request_failed"}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Code: "quota_canceled"}
		}
		return nil, &Error{Code: "quota_transport_failed"}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		e := &Error{Code: fmt.Sprintf("quota_http_%d", resp.StatusCode), Status: resp.StatusCode}
		if resp.StatusCode == http.StatusTooManyRequests {
			e.RetryAfter = RetryAfter(resp.Header.Get("Retry-After"), f.Now())
		}
		return nil, e
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUsageBytes+1))
	if err != nil || len(body) > maxUsageBytes {
		return nil, &Error{Code: "invalid_quota_body"}
	}
	return body, nil
}

// KimiUsageURLFor picks the Kimi usage host from the credential, matching
// CPA's credential synthesis: explicit domain, then base URL, then type.
func KimiUsageURLFor(domain, baseURL, typ string) string {
	pick := func(v string) string {
		v = strings.ToLower(strings.TrimSpace(v))
		switch {
		case v == "":
			return ""
		case v == "ai" || v == "kimi-ai" || v == "kimi.ai" || strings.HasSuffix(v, ".kimi.ai"):
			return KimiAIUsageURL
		case v == "com" || v == "kimi" || v == "kimi.com" || strings.HasSuffix(v, ".kimi.com"):
			return KimiUsageURL
		}
		return ""
	}
	if strings.TrimSpace(domain) != "" {
		if u := pick(domain); u != "" {
			return u
		}
		return KimiUsageURL
	}
	if baseURL != "" {
		host := strings.ToLower(baseURL)
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		if i := strings.IndexAny(host, "/:"); i >= 0 {
			host = host[:i]
		}
		if u := pick(host); u != "" {
			return u
		}
	}
	if u := pick(strings.ReplaceAll(typ, "_", "-")); u != "" {
		return u
	}
	return KimiUsageURL
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	case map[string]any:
		return num(n["val"])
	}
	return 0, false
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func parseInstant(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	// Some providers send nanosecond fractions; RFC3339Nano accepts them.
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func kimiReset(m map[string]any, now time.Time) *time.Time {
	for _, k := range []string{"reset_at", "resetAt", "reset_time", "resetTime"} {
		if t, ok := parseInstant(str(m[k])); ok {
			return &t
		}
	}
	for _, k := range []string{"reset_in", "resetIn", "ttl"} {
		if s, ok := num(m[k]); ok && s > 0 {
			t := now.Add(time.Duration(s) * time.Second)
			return &t
		}
	}
	return nil
}

func kimiPeriodHours(duration float64, unit string) float64 {
	unit = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(unit)), "TIME_UNIT_")
	switch unit {
	case "SECOND", "SECONDS":
		return duration / 3600
	case "HOUR", "HOURS":
		return duration
	case "DAY", "DAYS":
		return duration * 24
	case "WEEK", "WEEKS":
		return duration * 24 * 7
	}
	return duration / 60
}

// ParseKimiUsage converts a Kimi /usages response into windows.
func ParseKimiUsage(body []byte, now time.Time) (Snapshot, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return Snapshot{}, errors.New("invalid_kimi_body")
	}
	snap := Snapshot{Provider: "kimi"}
	add := func(m map[string]any, label string, hours float64) {
		limit, hasLimit := num(m["limit"])
		used, hasUsed := num(m["used"])
		if !hasUsed {
			if rem, ok := num(m["remaining"]); ok && hasLimit {
				used, hasUsed = limit-rem, true
			}
		}
		if !hasUsed || !hasLimit || limit <= 0 {
			return
		}
		if n := str(m["name"]); n != "" {
			label = n
		} else if n := str(m["title"]); n != "" {
			label = n
		}
		w := Window{Kind: KindForPeriod(hours), Family: label, UsedPercent: math.Max(0, math.Min(100, used/limit*100)), Source: SourcePoll, ObservedAt: now, ResetsAt: kimiReset(m, now)}
		snap.Windows = append(snap.Windows, w)
	}
	if limits, ok := payload["limits"].([]any); ok {
		for i, raw := range limits {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			detail := item
			if d, ok := item["detail"].(map[string]any); ok {
				detail = d
			}
			window, _ := item["window"].(map[string]any)
			duration, unit := 0.0, ""
			for _, src := range []map[string]any{window, item, detail} {
				if src == nil {
					continue
				}
				if d, ok := num(src["duration"]); ok && duration == 0 {
					duration = d
				}
				if u := str(src["timeUnit"]); u != "" && unit == "" {
					unit = u
				}
			}
			label := ""
			for _, k := range []string{"name", "title", "scope"} {
				if label = str(item[k]); label != "" {
					break
				}
			}
			if label == "" {
				label = fmt.Sprintf("limit %d", i+1)
			}
			add(detail, label, kimiPeriodHours(duration, unit))
		}
	}
	if usage, ok := payload["usage"].(map[string]any); ok {
		add(usage, "weekly", 24*7)
	}
	if usages, ok := payload["usages"].(map[string]any); ok {
		if month, ok := usages["limit_month_total"].(map[string]any); ok {
			if ratio, ok := num(month["used_ratio"]); ok {
				add(map[string]any{"used": math.Round(ratio * 100), "limit": 100.0, "reset_time": month["reset_time"]}, "monthly", 24*30)
			}
		}
	}
	if len(snap.Windows) == 0 {
		return Snapshot{}, errors.New("empty_kimi_usage")
	}
	dedupeFamilies(snap.Windows)
	return snap, nil
}

// dedupeFamilies drops the family label from windows that are the only one
// of their kind; repeated kinds keep it so they stay distinguishable.
func dedupeFamilies(ws []Window) {
	count := map[Kind]int{}
	for _, w := range ws {
		count[w.Kind]++
	}
	for i := range ws {
		if count[ws[i].Kind] == 1 {
			ws[i].Family = ""
		}
	}
}

// XaiHeaders builds Grok CLI request headers.
func XaiHeaders(token, userID, clientVersion string) map[string]string {
	h := map[string]string{
		"Authorization":            "Bearer " + token,
		"X-XAI-Token-Auth":         xaiTokenAuthValue,
		"x-grok-client-version":    clientVersion,
		"x-grok-client-identifier": xaiClientIDValue,
		"Accept":                   "*/*",
		"User-Agent":               fmt.Sprintf(xaiUserAgentFormat, clientVersion),
	}
	if userID != "" {
		h["x-userid"] = userID
	}
	return h
}

// ParseXaiBilling converts a Grok billing response into at most one window:
// weekly credits when present, otherwise the monthly included allowance.
func ParseXaiBilling(body []byte, now time.Time) (*Window, error) {
	var payload struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("invalid_xai_body")
	}
	c := payload.Config
	if c == nil {
		return nil, nil
	}
	pick := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := c[k]; ok && v != nil {
				return v
			}
		}
		return nil
	}
	period, _ := pick("currentPeriod", "current_period").(map[string]any)
	periodType := strings.ToLower(str(period["type"]))
	credit, hasCredit := num(pick("creditUsagePercent", "credit_usage_percent"))
	products, _ := pick("productUsage", "product_usage").([]any)
	weekly := hasCredit || strings.Contains(periodType, "weekly") || len(products) > 0
	w := &Window{Source: SourcePoll, ObservedAt: now}
	var end string
	if weekly {
		if !hasCredit {
			return nil, nil
		}
		w.Kind = Weekly
		if strings.Contains(periodType, "monthly") {
			w.Kind = Monthly
		}
		w.UsedPercent = credit
		end = str(period["end"])
		if end == "" {
			end = str(pick("billingPeriodEnd", "billing_period_end"))
		}
	} else {
		limit, okLimit := num(pick("monthlyLimit", "monthly_limit"))
		used, okUsed := num(pick("used"))
		if !okLimit || !okUsed || limit <= 0 {
			return nil, nil
		}
		w.Kind = Monthly
		w.UsedPercent = math.Min(used, limit) / limit * 100
		end = str(pick("billingPeriodEnd", "billing_period_end"))
	}
	w.UsedPercent = math.Max(0, math.Min(100, w.UsedPercent))
	if t, ok := parseInstant(end); ok {
		w.ResetsAt = &t
	}
	return w, nil
}
