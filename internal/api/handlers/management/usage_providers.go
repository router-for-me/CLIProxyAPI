package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	usageWeekMinutes  = 7 * 24 * 60
	usageMonthMinutes = 30 * 24 * 60
)

// usageResult is one provider usage reading. Claude and Codex return their main
// windows as the quota headers their responses carry, so consume-first ranks on
// them; windows holds everything else.
type usageResult struct {
	plan    string
	windows []coreauth.UsageWindow
	headers http.Header
}

type usageFetcher func(ctx context.Context, client *http.Client, auth *coreauth.Auth, token string) (usageResult, error)

// usageFetchers maps a provider to the usage endpoint CodexBar reads for it.
var usageFetchers = map[string]usageFetcher{
	"claude": func(ctx context.Context, client *http.Client, _ *coreauth.Auth, token string) (usageResult, error) {
		body, err := usageRequest(ctx, client, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil, map[string]string{
			"Authorization":  "Bearer " + token,
			"anthropic-beta": "oauth-2025-04-20",
		})
		return parseClaudeUsage(body), err
	},
	"codex": func(ctx context.Context, client *http.Client, auth *coreauth.Auth, token string) (usageResult, error) {
		headers := map[string]string{"Authorization": "Bearer " + token}
		if accountID := codexAccountID(auth); accountID != "" {
			headers["ChatGPT-Account-Id"] = accountID
		}
		body, err := usageRequest(ctx, client, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil, headers)
		return parseCodexUsage(body), err
	},
	"kimi": func(ctx context.Context, client *http.Client, auth *coreauth.Auth, token string) (usageResult, error) {
		base := kimiauth.KimiAPIBaseURL
		if kimiauth.IsKimiAIAuth(auth) {
			base = kimiauth.KimiAIAPIBaseURL
		}
		body, err := usageRequest(ctx, client, http.MethodGet, base+"/v1/usages", nil, map[string]string{"Authorization": "Bearer " + token})
		return parseKimiUsage(body), err
	},
	"xai": func(ctx context.Context, client *http.Client, _ *coreauth.Auth, token string) (usageResult, error) {
		body, err := usageRequest(ctx, client, http.MethodGet, xaiauth.CLIChatProxyBaseURL+"/billing?format=credits", nil, map[string]string{
			"Authorization":    "Bearer " + token,
			"x-xai-token-auth": "xai-grok-cli",
		})
		return parseXAIUsage(body), err
	},
	"antigravity": func(ctx context.Context, client *http.Client, auth *coreauth.Auth, token string) (usageResult, error) {
		project := authProjectID(auth)
		if project == "" {
			return usageResult{}, errors.New("credential has no project id")
		}
		payload, _ := json.Marshal(map[string]string{"project": project})
		body, err := usageRequest(ctx, client, http.MethodPost,
			antigravityauth.DailyAPIEndpoint+"/"+antigravityauth.APIVersion+":retrieveUserQuotaSummary", payload, map[string]string{
				"Authorization": "Bearer " + token,
				"Content-Type":  "application/json",
				"User-Agent":    misc.AntigravityUserAgent(),
			})
		return parseAntigravityUsage(body), err
	},
	// Muse reports usage on the call that mints its API key. The minted key is
	// discarded; the executor keeps the one it already holds.
	"meta": func(ctx context.Context, client *http.Client, _ *coreauth.Auth, token string) (usageResult, error) {
		payload, _ := json.Marshal(map[string]string{"dca_token": token})
		body, err := usageRequest(ctx, client, http.MethodPost, "https://api.meta.ai/muse-code/key", payload, map[string]string{
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
			"User-Agent":    "muse-code/1.0.2",
		})
		return parseMetaUsage(body), err
	},
}

// usageRequest sends one usage request and returns the JSON body of a 2xx reply.
func usageRequest(ctx context.Context, client *http.Client, method, url string, payload []byte, headers map[string]string) (gjson.Result, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, errRequest := http.NewRequestWithContext(ctx, method, url, body)
	if errRequest != nil {
		return gjson.Result{}, errRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI/"+buildinfo.Version)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return gjson.Result{}, fmt.Errorf("usage request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("usage refresh: close response body: %v", errClose)
		}
	}()
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return gjson.Result{}, fmt.Errorf("read usage response: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return gjson.Result{}, fmt.Errorf("usage endpoint returned HTTP %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(data) {
		return gjson.Result{}, errors.New("usage endpoint returned invalid JSON")
	}
	return gjson.ParseBytes(data), nil
}

func usageTime(values ...gjson.Result) time.Time {
	for _, value := range values {
		if value.Type == gjson.Number {
			if seconds := value.Int(); seconds > 0 {
				return time.Unix(seconds, 0).UTC()
			}
			continue
		}
		if parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(value.String())); errParse == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func formatUsageFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// parseClaudeUsage maps /api/oauth/usage onto the rate-limit headers Claude
// sends on responses (utilization as a 0-1 fraction, reset as Unix seconds).
// Model-scoped weekly limits become extra windows.
func parseClaudeUsage(body gjson.Result) usageResult {
	result := usageResult{headers: http.Header{}}
	for _, window := range []struct{ key, name string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		value := body.Get(window.key)
		reset := usageTime(value.Get("resets_at"))
		if !value.IsObject() || reset.IsZero() {
			continue
		}
		prefix := "anthropic-ratelimit-unified-" + window.name + "-"
		result.headers.Set(prefix+"utilization", formatUsageFloat(value.Get("utilization").Float()/100))
		result.headers.Set(prefix+"reset", strconv.FormatInt(reset.Unix(), 10))
	}
	for _, window := range []struct{ key, label string }{{"seven_day_opus", "Opus"}, {"seven_day_sonnet", "Sonnet"}} {
		if value := body.Get(window.key); value.IsObject() {
			result.windows = append(result.windows, coreauth.UsageWindow{Label: window.label, UsedPercent: value.Get("utilization").Float(),
				WindowMinutes: usageWeekMinutes, ResetAt: usageTime(value.Get("resets_at"))})
		}
	}
	for _, limit := range body.Get("limits").Array() {
		name := strings.TrimSpace(limit.Get("scope.model.display_name").String())
		if limit.Get("kind").String() != "weekly_scoped" || name == "" || strings.EqualFold(name, "all models") {
			continue
		}
		result.windows = append(result.windows, coreauth.UsageWindow{Label: name + " only", UsedPercent: limit.Get("percent").Float(),
			WindowMinutes: usageWeekMinutes, ResetAt: usageTime(limit.Get("resets_at"))})
	}
	return result
}

// parseCodexUsage maps /backend-api/wham/usage onto the x-codex-* headers Codex
// sends on responses. Per-model additional limits become extra windows.
func parseCodexUsage(body gjson.Result) usageResult {
	result := usageResult{plan: body.Get("plan_type").String(), headers: http.Header{}}
	for _, window := range []struct{ key, prefix string }{{"primary_window", "x-codex-primary-"}, {"secondary_window", "x-codex-secondary-"}} {
		value := body.Get("rate_limit." + window.key)
		if !value.IsObject() {
			continue
		}
		result.headers.Set(window.prefix+"used-percent", formatUsageFloat(value.Get("used_percent").Float()))
		if seconds := value.Get("limit_window_seconds").Float(); seconds > 0 {
			result.headers.Set(window.prefix+"window-minutes", formatUsageFloat(seconds/60))
		}
		if reset := usageTime(value.Get("reset_at")); !reset.IsZero() {
			result.headers.Set(window.prefix+"reset-at", strconv.FormatInt(reset.Unix(), 10))
		} else if after := value.Get("reset_after_seconds"); after.Exists() {
			result.headers.Set(window.prefix+"reset-after-seconds", formatUsageFloat(after.Float()))
		}
	}
	for _, extra := range body.Get("additional_rate_limits").Array() {
		for _, key := range []string{"primary_window", "secondary_window"} {
			if value := extra.Get("rate_limit." + key); value.IsObject() {
				result.windows = append(result.windows, coreauth.UsageWindow{Label: extra.Get("limit_name").String(), UsedPercent: value.Get("used_percent").Float(),
					WindowMinutes: value.Get("limit_window_seconds").Float() / 60, ResetAt: usageTime(value.Get("reset_at"))})
			}
		}
	}
	return result
}

func codexAccountID(auth *coreauth.Auth) string {
	if accountID, _ := auth.Metadata["account_id"].(string); strings.TrimSpace(accountID) != "" {
		return strings.TrimSpace(accountID)
	}
	accountID, _ := extractCodexIDTokenClaims(auth)["chatgpt_account_id"].(string)
	return accountID
}

var kimiPlanNames = map[string]string{
	"LEVEL_FREE": "Adagio", "LEVEL_TRIAL": "Andante", "LEVEL_BASIC": "Moderato",
	"LEVEL_INTERMEDIATE": "Allegretto", "LEVEL_ADVANCED": "Allegro",
}

// parseKimiUsage reads /coding/v1/usages: ratio pools under "usages" when
// present, else the older "usage" (weekly) and "limits" (short windows) counts.
func parseKimiUsage(body gjson.Result) usageResult {
	level := body.Get("user.membership.level").String()
	result := usageResult{plan: kimiPlanNames[level]}
	if result.plan == "" {
		result.plan = level
	}
	for _, pool := range []struct {
		key     string
		minutes float64
	}{{"limit_5h", 300}, {"limit_7d", usageWeekMinutes}, {"limit_month_total", usageMonthMinutes}} {
		value := body.Get("usages." + pool.key)
		if ratio := value.Get("used_ratio"); ratio.Exists() {
			result.windows = append(result.windows, coreauth.UsageWindow{UsedPercent: min(1, ratio.Float()) * 100,
				WindowMinutes: pool.minutes, ResetAt: usageTime(value.Get("reset_time"))})
		}
	}
	if len(result.windows) > 0 {
		return result
	}
	if window, ok := kimiDetailWindow(body.Get("usage"), usageWeekMinutes); ok {
		result.windows = append(result.windows, window)
	}
	units := map[string]float64{"TIME_UNIT_MINUTE": 1, "TIME_UNIT_HOUR": 60, "TIME_UNIT_DAY": 24 * 60}
	for _, limit := range body.Get("limits").Array() {
		minutes := limit.Get("window.duration").Float() * units[limit.Get("window.timeUnit").String()]
		if window, ok := kimiDetailWindow(limit.Get("detail"), minutes); ok {
			result.windows = append(result.windows, window)
		}
	}
	return result
}

func kimiDetailWindow(detail gjson.Result, minutes float64) (coreauth.UsageWindow, bool) {
	limit := detail.Get("limit").Float()
	if limit <= 0 {
		return coreauth.UsageWindow{}, false
	}
	used := detail.Get("used").Float()
	if !detail.Get("used").Exists() {
		used = limit - detail.Get("remaining").Float()
	}
	return coreauth.UsageWindow{UsedPercent: used / limit * 100, WindowMinutes: minutes,
		ResetAt: usageTime(detail.Get("resetTime"), detail.Get("resetAt"), detail.Get("reset_time"), detail.Get("reset_at"))}, true
}

// parseXAIUsage reads Grok's credit billing: one window, the billing period.
func parseXAIUsage(body gjson.Result) usageResult {
	billing := body.Get("config")
	result := usageResult{plan: billing.Get("subscriptionTier").String()}
	if result.plan == "" {
		result.plan = body.Get("subscriptionTier").String()
	}
	percent := billing.Get("creditUsagePercent")
	used := percent.Float()
	if !percent.Exists() {
		limit := billing.Get("onDemandCap.val").Float()
		if limit <= 0 {
			return result
		}
		used = billing.Get("onDemandUsed.val").Float() / limit * 100
	}
	start := usageTime(billing.Get("currentPeriod.start"), billing.Get("billingPeriodStart"))
	end := usageTime(billing.Get("currentPeriod.end"), billing.Get("billingPeriodEnd"))
	window := coreauth.UsageWindow{UsedPercent: used, ResetAt: end}
	if !start.IsZero() && end.After(start) {
		window.WindowMinutes = end.Sub(start).Minutes()
	}
	result.windows = append(result.windows, window)
	return result
}

// parseAntigravityUsage reads retrieveUserQuotaSummary: groups (the Gemini pool
// and the Claude/GPT pool) of buckets with a remaining fraction and a cadence.
func parseAntigravityUsage(body gjson.Result) usageResult {
	for _, wrapper := range []string{"response", "summary"} {
		if body.Get(wrapper + ".groups").Exists() {
			body = body.Get(wrapper)
		}
	}
	var result usageResult
	for _, group := range body.Get("groups").Array() {
		for _, bucket := range group.Get("buckets").Array() {
			remaining := bucket.Get("remainingFraction")
			if !remaining.Exists() {
				remaining = bucket.Get("remaining.remainingFraction")
			}
			if bucket.Get("disabled").Bool() || !remaining.Exists() {
				continue
			}
			result.windows = append(result.windows, coreauth.UsageWindow{Label: group.Get("displayName").String(),
				UsedPercent: 100 - remaining.Float()*100, WindowMinutes: antigravityBucketMinutes(bucket), ResetAt: usageTime(bucket.Get("resetTime"))})
		}
	}
	return result
}

func antigravityBucketMinutes(bucket gjson.Result) float64 {
	names := []string{bucket.Get("window").String()}
	if strings.TrimSpace(names[0]) == "" {
		names = []string{bucket.Get("bucketId").String(), bucket.Get("displayName").String()}
	}
	for _, name := range names {
		name = strings.ReplaceAll(strings.ToLower(name), "_", "-")
		switch {
		case strings.Contains(name, "5h"), strings.Contains(name, "5-hour"), strings.Contains(name, "five"), strings.Contains(name, "session"):
			return 300
		case strings.Contains(name, "week"):
			return usageWeekMinutes
		}
	}
	return 0
}

// parseMetaUsage reads subs_usage from Muse's key-mint response: a short window
// with its own length and a weekly window. It is null while both are idle.
func parseMetaUsage(body gjson.Result) usageResult {
	result := usageResult{plan: body.Get("subs_tier_name").String()}
	if window := body.Get("subs_usage.window"); window.IsObject() {
		result.windows = append(result.windows, coreauth.UsageWindow{UsedPercent: window.Get("used_percent").Float(),
			WindowMinutes: window.Get("window_duration_mins").Float(), ResetAt: usageTime(window.Get("resets_at"))})
	}
	if window := body.Get("subs_usage.weekly"); window.IsObject() {
		result.windows = append(result.windows, coreauth.UsageWindow{UsedPercent: window.Get("used_percent").Float(),
			WindowMinutes: usageWeekMinutes, ResetAt: usageTime(window.Get("resets_at"))})
	}
	return result
}
