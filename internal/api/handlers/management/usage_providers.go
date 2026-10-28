package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
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
	balance *usageBalance
	// banked is read only when bankedRead is set, so a failed read keeps the
	// last known list.
	banked     []usageCredit
	bankedRead bool
}

// usageBalance is money left on an API account: a prepaid balance, or the
// monthly credit grant with its size in Total.
type usageBalance struct {
	Left     float64 `json:"left"`
	Total    float64 `json:"total,omitempty"`
	Currency string  `json:"currency"`
}

// usageCredit is one banked Codex rate-limit reset. Each clears a spent
// window once and lapses at ExpiresAt; it is redeemed in Codex itself.
type usageCredit struct {
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

type usageFetcher func(ctx context.Context, client *http.Client, auth *coreauth.Auth, token string) (usageResult, error)

// usageKind names the fetcher for a credential: its provider for a login, or
// for an API key from the config file the service its base URL points at.
// Empty when the credential has no usage endpoint.
func usageKind(auth *coreauth.Auth) string {
	if auth.AuthSourceKind() != coreauth.AuthSourceConfig {
		return strings.ToLower(strings.TrimSpace(auth.Provider))
	}
	base, errParse := url.Parse(strings.TrimSpace(auth.Attributes["base_url"]))
	if errParse != nil {
		return ""
	}
	switch host := strings.ToLower(base.Hostname()); {
	case host == "api.deepseek.com":
		return "deepseek"
	case host == "commandcode.ai" || strings.HasSuffix(host, ".commandcode.ai"):
		return "commandcode"
	case host == "api.z.ai" || host == "open.bigmodel.cn":
		return "zai"
	case host == "opencode.ai" && strings.Contains(base.Path, "/go"):
		return "opencode-go"
	}
	return ""
}

// usageFetchers maps a usage kind to the endpoint CodexBar reads for it.
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
		result := parseCodexUsage(body)
		if err != nil {
			return result, err
		}
		headers["OpenAI-Beta"] = "codex-1"
		headers["originator"] = "Codex Desktop"
		credits, errCredits := usageRequest(ctx, client, http.MethodGet, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits", nil, headers)
		if errCredits != nil {
			log.WithError(errCredits).Debug("usage refresh: codex banked resets")
			return result, nil
		}
		result.banked, result.bankedRead = parseCodexResetCredits(credits, time.Now()), true
		return result, nil
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
	// Devin reports its daily and weekly quota on the seat-management status
	// call its executor makes, with the CLI login's own session token.
	"devin": func(ctx context.Context, client *http.Client, auth *coreauth.Auth, token string) (usageResult, error) {
		seed, _ := auth.Metadata["device_seed"].(string)
		status, err := devinauth.NewDevinAuthService(client).FetchUserStatus(ctx, token, seed)
		if err != nil {
			return usageResult{}, err
		}
		return devinUsage(status), nil
	},
	"deepseek": func(ctx context.Context, client *http.Client, _ *coreauth.Auth, token string) (usageResult, error) {
		body, err := usageRequest(ctx, client, http.MethodGet, "https://api.deepseek.com/user/balance", nil, map[string]string{"Authorization": "Bearer " + token})
		return parseDeepSeekBalance(body), err
	},
	// CommandCode reports credits and rolling limits on one call and the plan,
	// which sizes the monthly grant and dates its end, on another.
	"commandcode": func(ctx context.Context, client *http.Client, _ *coreauth.Auth, token string) (usageResult, error) {
		headers := map[string]string{"Authorization": "Bearer " + token}
		credits, err := usageRequest(ctx, client, http.MethodGet, "https://api.commandcode.ai/alpha/billing/credits", nil, headers)
		if err != nil {
			return usageResult{}, err
		}
		subscription, errSubscription := usageRequest(ctx, client, http.MethodGet, "https://api.commandcode.ai/alpha/billing/subscriptions", nil, headers)
		if errSubscription != nil {
			log.WithError(errSubscription).Debug("usage refresh: commandcode subscription")
		}
		return parseCommandCodeUsage(credits, subscription), nil
	},
	"zai": func(ctx context.Context, client *http.Client, auth *coreauth.Auth, token string) (usageResult, error) {
		base, _ := url.Parse(strings.TrimSpace(auth.Attributes["base_url"]))
		body, err := usageRequest(ctx, client, http.MethodGet, "https://"+base.Host+"/api/monitor/usage/quota/limit", nil, map[string]string{"Authorization": "Bearer " + token})
		if err == nil && !body.Get("success").Bool() {
			err = fmt.Errorf("quota endpoint refused: %s", body.Get("msg").String())
		}
		return parseZaiUsage(body), err
	},
	"opencode-go": func(ctx context.Context, client *http.Client, _ *coreauth.Auth, token string) (usageResult, error) {
		body, err := usageRequest(ctx, client, http.MethodGet, "https://opencode.ai/zen/go/v1/usage", nil, map[string]string{"Authorization": "Bearer " + token})
		return parseOpenCodeGoUsage(body, time.Now()), err
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
			if seconds := value.Int(); seconds > 1e11 {
				// Milliseconds (GLM).
				return time.UnixMilli(seconds).UTC()
			} else if seconds > 0 {
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

// parseCodexResetCredits lists the available banked resets, soonest expiry
// first and those without an expiry last.
func parseCodexResetCredits(body gjson.Result, now time.Time) []usageCredit {
	credits := make([]usageCredit, 0)
	for _, credit := range body.Get("credits").Array() {
		expires := usageTime(credit.Get("expires_at"))
		if credit.Get("status").String() == "available" && (expires.IsZero() || expires.After(now)) {
			credits = append(credits, usageCredit{ExpiresAt: expires})
		}
	}
	sort.SliceStable(credits, func(i, j int) bool {
		a, b := credits[i].ExpiresAt, credits[j].ExpiresAt
		return !a.IsZero() && (b.IsZero() || a.Before(b))
	})
	return credits
}

// devinUsage turns Devin's remaining percentages into windows. A window with no
// reset time was not reported: its remaining 0 is protobuf's default, not spent.
func devinUsage(status *devinauth.DevinUserStatus) usageResult {
	result := usageResult{plan: status.Plan}
	for _, window := range []struct {
		left    int64
		minutes float64
		reset   time.Time
	}{
		{status.DailyQuotaRemainingPercent, 24 * 60, status.DailyQuotaResetAt},
		{status.WeeklyQuotaRemainingPercent, usageWeekMinutes, status.WeeklyQuotaResetAt},
	} {
		if !window.reset.IsZero() {
			result.windows = append(result.windows, coreauth.UsageWindow{UsedPercent: float64(100 - window.left), WindowMinutes: window.minutes, ResetAt: window.reset})
		}
	}
	return result
}

// parseDeepSeekBalance reads /user/balance: a prepaid balance per currency.
func parseDeepSeekBalance(body gjson.Result) usageResult {
	info := body.Get("balance_infos.0")
	if !info.Exists() {
		return usageResult{}
	}
	return usageResult{balance: &usageBalance{Left: info.Get("total_balance").Float(), Currency: info.Get("currency").String()}}
}

// commandCodePlans sizes each plan's monthly credit grant in USD, from
// commandcode.ai/pricing, for credit replies that do not carry the grant.
var commandCodePlans = map[string]struct {
	name string
	usd  float64
}{
	"individual-go": {"Go", 10}, "individual-go-v1": {"Go", 10}, "individual-goat": {"GOAT", 70},
	"individual-pro": {"Pro", 30}, "individual-pro-v1": {"Pro", 80}, "individual-max": {"Max", 150},
	"individual-ultra": {"Ultra", 300},
}

// parseCommandCodeUsage reads /billing/credits (5-hour and weekly caps, and the
// USD left in the monthly grant) and /billing/subscriptions (plan, period end).
func parseCommandCodeUsage(credits, subscription gjson.Result) usageResult {
	var result usageResult
	limits := credits.Get("windowLimits")
	if !limits.Exists() {
		limits = credits.Get("credits.windowLimits")
	}
	for _, window := range []struct {
		key     string
		minutes float64
	}{{"fiveHour", 300}, {"weekly", usageWeekMinutes}} {
		limit := limits.Get(window.key)
		if cap := limit.Get("cap").Float(); cap > 0 {
			result.windows = append(result.windows, coreauth.UsageWindow{UsedPercent: min(100, limit.Get("used").Float()/cap*100),
				WindowMinutes: window.minutes, ResetAt: usageTime(limit.Get("resetAt"))})
		}
	}
	left := credits.Get("credits.monthlyCredits")
	if !left.Exists() {
		return result
	}
	planID := strings.ToLower(subscription.Get("data.planId").String())
	plan := commandCodePlans[planID]
	result.plan = plan.name
	if result.plan == "" {
		result.plan = planID
	}
	total := credits.Get("credits.monthlyCreditsGranted").Float()
	if total <= 0 {
		total = plan.usd
	}
	result.balance = &usageBalance{Left: left.Float(), Total: total, Currency: "USD"}
	if total > 0 {
		result.windows = append(result.windows, coreauth.UsageWindow{UsedPercent: min(100, max(0, (total-left.Float())/total*100)),
			WindowMinutes: usageMonthMinutes, ResetAt: usageTime(subscription.Get("data.currentPeriodEnd"))})
	}
	return result
}

// parseZaiUsage reads the GLM coding plan's quota/limit: token windows (5-hour
// and weekly) and the monthly count of web search and reader calls ("MCP").
func parseZaiUsage(body gjson.Result) usageResult {
	data := body.Get("data")
	var result usageResult
	for _, key := range []string{"planName", "plan", "plan_type", "packageName", "level"} {
		if plan := strings.TrimSpace(data.Get(key).String()); plan != "" {
			result.plan = plan
			break
		}
	}
	unitMinutes := map[int64]float64{1: 24 * 60, 3: 60, 5: 1, 6: usageWeekMinutes}
	for _, limit := range data.Get("limits").Array() {
		unit, number := limit.Get("unit").Int(), limit.Get("number").Int()
		window := coreauth.UsageWindow{UsedPercent: limit.Get("percentage").Float(), WindowMinutes: float64(number) * unitMinutes[unit],
			ResetAt: usageTime(limit.Get("nextResetTime"))}
		if limit.Get("type").String() == "TIME_LIMIT" {
			window.Label = "MCP"
			if unit == 5 && number == 1 {
				window.WindowMinutes = usageMonthMinutes
			}
		}
		result.windows = append(result.windows, window)
	}
	return result
}

// parseOpenCodeGoUsage reads /zen/go/v1/usage: rolling 5-hour, weekly and
// monthly windows, each a used percent and seconds until it resets.
func parseOpenCodeGoUsage(body gjson.Result, now time.Time) usageResult {
	var result usageResult
	for _, window := range []struct {
		key     string
		minutes float64
	}{{"rolling", 300}, {"weekly", usageWeekMinutes}, {"monthly", usageMonthMinutes}} {
		value := body.Get("usage." + window.key)
		for _, key := range []string{"percent", "usagePercent", "usedPercent"} {
			if percent := value.Get(key); percent.Exists() {
				reset := usageTime(value.Get("resetAt"), value.Get("resetsAt"))
				if seconds := value.Get("resetInSec").Int(); seconds > 0 {
					reset = now.Add(time.Duration(seconds) * time.Second).UTC().Truncate(time.Second)
				}
				result.windows = append(result.windows, coreauth.UsageWindow{UsedPercent: percent.Float(), WindowMinutes: window.minutes, ResetAt: reset})
				break
			}
		}
	}
	return result
}
