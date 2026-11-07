package management

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	claudeOAuthUsageURL      = "https://api.anthropic.com/api/oauth/usage"
	claudeOAuthBetaHeader    = "oauth-2025-04-20"
	claudeUsageUserAgent     = "claude-cli/2.1.241 (external, cli)"
	codexUsageURL            = "https://chatgpt.com/backend-api/wham/usage"
	codexResetCreditsURL     = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexResetConsumeURL     = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	codexUsageUserAgent      = "codex_cli_rs/0.149.0"
	antigravityBaseURLProd   = "https://cloudcode-pa.googleapis.com"
	providerUsageCacheTTL    = 90 * time.Second
	providerUsageHTTPTimeout = 10 * time.Second
	// providerUsagePrewarmBudget bounds the whole fan-out so the management
	// endpoint always answers before an upstream gateway timeout.
	providerUsagePrewarmBudget = 15 * time.Second

	// autoClaimEnvVar must be set to "1" for any reset credit to be redeemed.
	autoClaimEnvVar = "CPA_CODEX_AUTO_CLAIM_RESET"
	// autoClaimThresholdPercent is the utilization at which a reset becomes eligible.
	autoClaimThresholdPercent = 80.0
	// autoClaimMinInterval caps redemptions to one per account per day.
	autoClaimMinInterval = 24 * time.Hour
	// autoClaimAuditPathEnvVar optionally names a file that receives one audit
	// line per redemption attempt. When unset, the audit line is only written to
	// the regular application log.
	autoClaimAuditPathEnvVar = "CPA_CODEX_AUTO_CLAIM_AUDIT_LOG"
)

type providerUsageEntry struct {
	windows   []authUsageWindow
	fetchedAt time.Time
}

var (
	providerUsageCache sync.Map // authID -> providerUsageEntry
	lastAutoClaimAt    sync.Map // authID -> time.Time
	xaiTeamIDCache     sync.Map // authID -> team id
	providerUsageHTTP  = &http.Client{Timeout: providerUsageHTTPTimeout}
)

// providerUsageWindows returns quota windows sourced directly from the upstream
// provider account APIs. Results are cached briefly because the status
// dashboard polls this endpoint every few seconds.
func providerUsageWindows(auth *coreauth.Auth) []authUsageWindow {
	if auth == nil {
		return nil
	}
	if cached, ok := providerUsageCache.Load(auth.ID); ok {
		if entry, ok2 := cached.(providerUsageEntry); ok2 && time.Since(entry.fetchedAt) < providerUsageCacheTTL {
			return entry.windows
		}
	}
	var windows []authUsageWindow
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		windows = fetchClaudeUsageWindows(auth)
	case "codex":
		windows = fetchCodexUsageWindows(auth)
	case "antigravity", "gemini":
		windows = fetchAntigravityUsageWindows(auth)
	case "xai":
		windows = xaiUsageWindows(auth)
	}
	providerUsageCache.Store(auth.ID, providerUsageEntry{windows: windows, fetchedAt: time.Now()})
	return windows
}

// prewarmProviderUsage refreshes every auth's provider usage concurrently so a
// cold cache costs one round trip in wall time instead of one per account.
func prewarmProviderUsage(auths []*coreauth.Auth) {
	var wg sync.WaitGroup
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		wg.Add(1)
		go func(a *coreauth.Auth) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Warnf("auth usage prewarm panic for %s: %v", a.ID, r)
				}
			}()
			providerUsageWindows(a)
		}(auth)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(providerUsagePrewarmBudget):
		log.Warnf("auth usage prewarm exceeded %s budget; serving partial provider data", providerUsagePrewarmBudget)
	}
}

func authAccessToken(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["access_token"]); v != "" {
			return v
		}
	}
	return ""
}

func authMetaString(auth *coreauth.Auth, key string) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
			return v
		}
	}
	return ""
}

func fetchProviderJSON(method, url string, headers map[string]string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), providerUsageHTTPTimeout)
	defer cancel()
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := providerUsageHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("auth usage: close response body error: %v", errClose)
		}
	}()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payload, fmt.Errorf("http %d", resp.StatusCode)
	}
	return payload, nil
}

func utilizationStatus(percent float64) string {
	switch {
	case percent >= 100:
		return "exhausted"
	case percent >= autoClaimThresholdPercent:
		return "warning"
	default:
		return "active"
	}
}

func floatPtr(v float64) *float64 { return &v }

// notOfferedWindow marks a window the provider genuinely does not implement, so
// the UI can distinguish "no such quota" from "value unknown".
func notOfferedWindow(window, provider, detail string) authUsageWindow {
	return authUsageWindow{
		Window:     window,
		Status:     "not_offered",
		Source:     provider + "_no_such_window",
		Confidence: "exact",
		Message:    detail,
	}
}

// ensureWindows appends not_offered placeholders for labels the provider never
// reports, so every dashboard cell carries a meaning.
func ensureWindows(windows []authUsageWindow, provider, detail string, labels ...string) []authUsageWindow {
	present := make(map[string]bool, len(windows))
	for _, w := range windows {
		present[w.Window] = true
	}
	for _, label := range labels {
		if !present[label] {
			windows = append(windows, notOfferedWindow(label, provider, detail))
		}
	}
	return windows
}

func errorWindow(window, source, message string) authUsageWindow {
	return authUsageWindow{
		Window:     window,
		Status:     "unknown",
		Source:     source,
		Confidence: "unknown",
		Message:    message,
	}
}

// ---------------------------------------------------------------- Claude ----

func fetchClaudeUsageWindows(auth *coreauth.Auth) []authUsageWindow {
	token := authAccessToken(auth)
	if token == "" {
		return []authUsageWindow{errorWindow("account", "claude_oauth_usage", "no access_token available for provider usage lookup")}
	}
	payload, err := fetchProviderJSON(http.MethodGet, claudeOAuthUsageURL, map[string]string{
		"Authorization":   "Bearer " + token,
		"anthropic-beta":  claudeOAuthBetaHeader,
		"User-Agent":      claudeUsageUserAgent,
		"Accept":          "application/json",
		"Content-Type":    "application/json",
		"Accept-Encoding": "identity",
	}, nil)
	if err != nil {
		return []authUsageWindow{errorWindow("account", "claude_oauth_usage", "usage fetch failed: "+err.Error())}
	}
	windows := make([]authUsageWindow, 0, 4)
	for _, spec := range []struct {
		field string
		label string
	}{
		{"five_hour", "5h"},
		{"seven_day", "7d"},
	} {
		node := gjson.GetBytes(payload, spec.field)
		if !node.Exists() || node.Type == gjson.Null {
			continue
		}
		w := authUsageWindow{
			Window:     spec.label,
			Used:       floatPtr(node.Get("utilization").Float()),
			Limit:      floatPtr(100),
			Source:     "claude_oauth_usage(" + spec.field + ".utilization)",
			Confidence: "exact",
		}
		w.Status = utilizationStatus(node.Get("utilization").Float())
		if ts := strings.TrimSpace(node.Get("resets_at").String()); ts != "" {
			if parsed, errParse := time.Parse(time.RFC3339, ts); errParse == nil {
				utc := parsed.UTC()
				w.ResetAt = &utc
			}
		}
		windows = append(windows, w)
	}
	if extra := gjson.GetBytes(payload, "extra_usage"); extra.Exists() {
		if extra.Get("is_enabled").Bool() {
			windows = append(windows, authUsageWindow{
				Window:     "credits",
				Status:     utilizationStatus(extra.Get("utilization").Float()),
				Used:       floatPtr(extra.Get("used_credits").Float()),
				Limit:      floatPtr(extra.Get("monthly_limit").Float()),
				Source:     "claude_oauth_usage(extra_usage)",
				Confidence: "exact",
			})
		} else {
			windows = append(windows, authUsageWindow{
				Window:     "credits",
				Status:     "off",
				Source:     "claude_oauth_usage(extra_usage.is_enabled=false)",
				Confidence: "exact",
				Message:    "extra usage billing is disabled for this account",
			})
		}
	}
	windows = ensureWindows(windows, "claude", "Anthropic exposes only 5h and 7d unified windows", "24h")
	windows = ensureWindows(windows, "claude", "Anthropic offers no claimable usage reset", "free_reset")
	return windows
}

// ----------------------------------------------------------------- Codex ----

func codexWindowLabel(seconds int64) string {
	switch seconds {
	case 18000:
		return "5h"
	case 86400:
		return "24h"
	case 604800:
		return "7d"
	default:
		return fmt.Sprintf("window_%ds", seconds)
	}
}

func fetchCodexUsageWindows(auth *coreauth.Auth) []authUsageWindow {
	token := authAccessToken(auth)
	if token == "" {
		return []authUsageWindow{errorWindow("account", "codex_wham_usage", "no access_token available for provider usage lookup")}
	}
	accountID := authMetaString(auth, "account_id")
	headers := map[string]string{
		"Authorization":      "Bearer " + token,
		"chatgpt-account-id": accountID,
		"User-Agent":         codexUsageUserAgent,
		"Accept":             "application/json",
	}
	windows := make([]authUsageWindow, 0, 4)
	maxUtilization := 0.0

	payload, err := fetchProviderJSON(http.MethodGet, codexUsageURL, headers, nil)
	if err != nil {
		windows = append(windows, errorWindow("account", "codex_wham_usage", "usage fetch failed: "+err.Error()))
	} else {
		for _, path := range []string{"rate_limit.primary_window", "rate_limit.secondary_window"} {
			node := gjson.GetBytes(payload, path)
			if !node.Exists() || node.Type == gjson.Null {
				continue
			}
			used := node.Get("used_percent").Float()
			if used > maxUtilization {
				maxUtilization = used
			}
			w := authUsageWindow{
				Window:     codexWindowLabel(node.Get("limit_window_seconds").Int()),
				Status:     utilizationStatus(used),
				Used:       floatPtr(used),
				Limit:      floatPtr(100),
				Source:     "codex_wham_usage(" + path + ")",
				Confidence: "exact",
			}
			if epoch := node.Get("reset_at").Int(); epoch > 0 {
				t := time.Unix(epoch, 0).UTC()
				w.ResetAt = &t
			}
			windows = append(windows, w)
		}
		for _, extra := range gjson.GetBytes(payload, "additional_rate_limits").Array() {
			name := strings.TrimSpace(extra.Get("limit_name").String())
			for _, path := range []string{"rate_limit.primary_window", "rate_limit.secondary_window"} {
				node := extra.Get(path)
				if !node.Exists() || node.Type == gjson.Null {
					continue
				}
				used := node.Get("used_percent").Float()
				if used > maxUtilization {
					maxUtilization = used
				}
				label := codexWindowLabel(node.Get("limit_window_seconds").Int())
				w := authUsageWindow{
					Window:     label,
					Model:      name,
					Status:     utilizationStatus(used),
					Used:       floatPtr(used),
					Limit:      floatPtr(100),
					Source:     "codex_wham_usage(additional_rate_limits." + path + ")",
					Confidence: "exact",
				}
				if epoch := node.Get("reset_at").Int(); epoch > 0 {
					t := time.Unix(epoch, 0).UTC()
					w.ResetAt = &t
				}
				windows = append(windows, w)
			}
		}
		if credits := gjson.GetBytes(payload, "credits"); credits.Exists() {
			w := authUsageWindow{
				Window:     "credits",
				Status:     "active",
				Used:       floatPtr(credits.Get("balance").Float()),
				Source:     "codex_wham_usage(credits.balance)",
				Confidence: "exact",
			}
			if !credits.Get("has_credits").Bool() {
				w.Status = "off"
				w.Message = "account has no purchased credit balance"
			}
			windows = append(windows, w)
		}
	}

	creditsPayload, errCredits := fetchProviderJSON(http.MethodGet, codexResetCreditsURL, headers, nil)
	if errCredits != nil {
		windows = append(windows, errorWindow("free_reset", "codex_rate_limit_reset_credits", "reset credit fetch failed: "+errCredits.Error()))
		return windows
	}
	available := gjson.GetBytes(creditsPayload, "available_count").Int()
	resetWindow := authUsageWindow{
		Window:     "free_reset",
		Status:     "none",
		Used:       floatPtr(float64(available)),
		Source:     "codex_rate_limit_reset_credits",
		Confidence: "exact",
	}
	var claimableID string
	for _, credit := range gjson.GetBytes(creditsPayload, "credits").Array() {
		if !strings.EqualFold(credit.Get("status").String(), "available") {
			continue
		}
		claimableID = credit.Get("id").String()
		resetWindow.Status = "available"
		resetWindow.Message = strings.TrimSpace(credit.Get("title").String())
		if ts := strings.TrimSpace(credit.Get("expires_at").String()); ts != "" {
			if parsed, errParse := time.Parse(time.RFC3339, ts); errParse == nil {
				utc := parsed.UTC()
				resetWindow.ResetAt = &utc
			}
		}
		break
	}
	if claimableID != "" {
		if claimed, outcome := maybeClaimCodexReset(auth, headers, claimableID, maxUtilization); claimed {
			resetWindow.Status = "claimed"
			resetWindow.Message = "auto-claimed: " + outcome
			providerUsageCache.Delete(auth.ID)
		}
	}
	windows = append(windows, resetWindow)
	windows = ensureWindows(windows, "codex", "OpenAI exposes only 5h and weekly windows", "24h")
	return windows
}

func newRedeemRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d-cliproxy-reset", time.Now().UnixNano())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func autoClaimEnabled() bool {
	return strings.TrimSpace(os.Getenv(autoClaimEnvVar)) == "1"
}

// maybeClaimCodexReset redeems a free rate limit reset only when the account is
// genuinely near or past its quota, at most once per day, and only when the
// operator has explicitly opted in. The credit id is always sent explicitly so
// the provider cannot pick a credit on our behalf.
func maybeClaimCodexReset(auth *coreauth.Auth, headers map[string]string, creditID string, maxUtilization float64) (bool, string) {
	if !autoClaimEnabled() {
		return false, ""
	}
	cooling := auth.Unavailable || auth.Quota.Exceeded || !auth.NextRetryAfter.IsZero()
	if maxUtilization < autoClaimThresholdPercent && !cooling {
		return false, ""
	}
	if last, ok := lastAutoClaimAt.Load(auth.ID); ok {
		if ts, ok2 := last.(time.Time); ok2 && time.Since(ts) < autoClaimMinInterval {
			return false, ""
		}
	}
	body := []byte(fmt.Sprintf(`{"credit_id":%q,"redeem_request_id":%q}`, creditID, newRedeemRequestID()))
	postHeaders := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		postHeaders[k] = v
	}
	postHeaders["Content-Type"] = "application/json"
	payload, err := fetchProviderJSON(http.MethodPost, codexResetConsumeURL, postHeaders, body)
	outcome := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
	if err != nil {
		auditAutoClaim(auth, creditID, maxUtilization, cooling, "error:"+err.Error())
		return false, ""
	}
	lastAutoClaimAt.Store(auth.ID, time.Now())
	auditAutoClaim(auth, creditID, maxUtilization, cooling, outcome)
	return outcome == "reset", outcome
}

func auditAutoClaim(auth *coreauth.Auth, creditID string, maxUtilization float64, cooling bool, outcome string) {
	line := fmt.Sprintf("%s auth=%s provider=%s label=%q credit_id=%s max_utilization=%.1f cooling=%t outcome=%s\n",
		time.Now().UTC().Format(time.RFC3339), auth.ID, auth.Provider, auth.Label, creditID, maxUtilization, cooling, outcome)
	log.Infof("codex reset auto-claim: %s", strings.TrimSpace(line))
	auditPath := strings.TrimSpace(os.Getenv(autoClaimAuditPathEnvVar))
	if auditPath == "" {
		return
	}
	f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		log.Warnf("codex reset auto-claim: audit log unavailable: %v", err)
		return
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Warnf("codex reset auto-claim: close audit log error: %v", errClose)
		}
	}()
	if _, errWrite := f.WriteString(line); errWrite != nil {
		log.Warnf("codex reset auto-claim: audit write error: %v", errWrite)
	}
}

// ----------------------------------------------------------- Antigravity ----

func fetchAntigravityUsageWindows(auth *coreauth.Auth) []authUsageWindow {
	return antigravityEnsure(fetchAntigravityCreditWindows(auth))
}

func antigravityEnsure(windows []authUsageWindow) []authUsageWindow {
	windows = ensureWindows(windows, "antigravity",
		"Google Code Assist exposes no per-account usage counters, only tier entitlement and credit pools",
		"5h", "24h", "7d")
	return ensureWindows(windows, "antigravity", "Google offers no claimable usage reset", "free_reset")
}

func fetchAntigravityCreditWindows(auth *coreauth.Auth) []authUsageWindow {
	if hint, ok := coreauth.GetAntigravityCreditsHint(auth.ID); ok && hint.Known {
		return []authUsageWindow{antigravityCreditsWindow(hint.CreditAmount, hint.MinCreditAmount, hint.Available, hint.PaidTierID, "executor_credits_hint")}
	}
	token := authAccessToken(auth)
	if token == "" {
		return []authUsageWindow{errorWindow("credits", "antigravity_load_code_assist", "no access_token available for provider usage lookup")}
	}
	payload, err := fetchProviderJSON(http.MethodPost, antigravityBaseURLProd+"/v1internal:loadCodeAssist", map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"Accept":        "*/*",
	}, []byte(`{"metadata":{"ideType":"ANTIGRAVITY"}}`))
	if err != nil {
		return []authUsageWindow{errorWindow("credits", "antigravity_load_code_assist", "credits fetch failed: "+err.Error())}
	}
	paidTierID := strings.TrimSpace(gjson.GetBytes(payload, "paidTier.id").String())
	for _, credit := range gjson.GetBytes(payload, "paidTier.availableCredits").Array() {
		if !strings.EqualFold(credit.Get("creditType").String(), "GOOGLE_ONE_AI") {
			continue
		}
		amount := credit.Get("creditAmount").Float()
		minAmount := credit.Get("minimumCreditAmountForUsage").Float()
		return []authUsageWindow{antigravityCreditsWindow(amount, minAmount, amount >= minAmount, paidTierID, "loadCodeAssist.paidTier.availableCredits")}
	}
	tier := strings.TrimSpace(gjson.GetBytes(payload, "currentTier.id").String())
	if tier == "" {
		tier = strings.TrimSpace(gjson.GetBytes(payload, "allowedTiers.0.id").String())
	}
	// Google returns tier entitlement but no usage counters for Code Assist
	// accounts without a paid credit pool, so no window can be derived here.
	return []authUsageWindow{{
		Window:     "credits",
		Status:     "none",
		Used:       floatPtr(0),
		Source:     "antigravity_load_code_assist(tier=" + tier + ")",
		Confidence: "exact",
		Message:    "tier " + tier + " has no credit pool; Google exposes no usage counters for this account",
	}}
}

func antigravityCreditsWindow(amount, minAmount float64, available bool, paidTierID, source string) authUsageWindow {
	status := "active"
	if !available {
		status = "exhausted"
	}
	return authUsageWindow{
		Window:     "credits",
		Status:     status,
		Used:       floatPtr(amount),
		Limit:      floatPtr(minAmount),
		Source:     "antigravity_" + source,
		Confidence: "exact",
		Message:    "Google One AI credits, paid_tier_id=" + paidTierID,
	}
}

// ------------------------------------------------------------------- xAI ----

// xaiUsageWindows reports the xAI account state. xAI rejects OAuth bearer
// tokens on its billing/management API ("Action cannot be performed by OAuth2
// token users"), so exact credit balances require a team management key.
func xaiUsageWindows(auth *coreauth.Auth) []authUsageWindow {
	return xaiEnsure(xaiCreditWindows(auth))
}

func xaiEnsure(windows []authUsageWindow) []authUsageWindow {
	windows = ensureWindows(windows, "xai",
		"xAI exposes no per-account 5h/24h/7d counters; Grok Build quota is only observable from error responses",
		"5h", "24h", "7d")
	return ensureWindows(windows, "xai", "xAI offers no claimable usage reset", "free_reset")
}

// xaiTeamID resolves the team a Grok OAuth account belongs to. /v1/me is the
// only xAI endpoint that accepts an OAuth bearer, so the id is cached per auth.
func xaiTeamID(auth *coreauth.Auth) string {
	if id := authMetaString(auth, "team_id"); id != "" {
		return id
	}
	if cached, ok := xaiTeamIDCache.Load(auth.ID); ok {
		if id, ok2 := cached.(string); ok2 {
			return id
		}
	}
	token := authAccessToken(auth)
	if token == "" {
		return ""
	}
	payload, err := fetchProviderJSON(http.MethodGet, "https://api.x.ai/v1/me", map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
	}, nil)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(gjson.GetBytes(payload, "team_id").String())
	if id != "" {
		xaiTeamIDCache.Store(auth.ID, id)
	}
	return id
}

// xaiManagementKey returns the management key for a team. Keys are per team, so
// CPA_XAI_MANAGEMENT_KEYS holds a {"team_id":"key"} map; CPA_XAI_MANAGEMENT_KEY
// remains a single-team fallback.
func xaiManagementKey(teamID string) string {
	if raw := strings.TrimSpace(os.Getenv("CPA_XAI_MANAGEMENT_KEYS")); raw != "" && teamID != "" {
		if key := strings.TrimSpace(gjson.Get(raw, gjson.Escape(teamID)).String()); key != "" {
			return key
		}
	}
	return strings.TrimSpace(os.Getenv("CPA_XAI_MANAGEMENT_KEY"))
}

func xaiCreditWindows(auth *coreauth.Auth) []authUsageWindow {
	teamID := xaiTeamID(auth)
	key := xaiManagementKey(teamID)
	if key == "" || teamID == "" {
		detail := "xAI billing API rejects OAuth tokens; add a team management key via CPA_XAI_MANAGEMENT_KEYS for exact credit balance"
		if teamID != "" {
			detail = "no management key configured for xAI team " + teamID
		}
		return []authUsageWindow{errorWindow("credits", "xai_management_api", detail)}
	}
	payload, err := fetchProviderJSON(http.MethodGet,
		"https://management-api.x.ai/v1/billing/teams/"+teamID+"/prepaid/balance",
		map[string]string{"Authorization": "Bearer " + key, "Accept": "application/json"}, nil)
	if err != nil {
		return []authUsageWindow{errorWindow("credits", "xai_management_api", "balance fetch failed: "+err.Error())}
	}
	// Balance is reported in USD cents as a negative-signed liability value.
	cents := gjson.GetBytes(payload, "total.val").Float()
	return []authUsageWindow{{
		Window:     "credits",
		Status:     utilizationStatus(0),
		Used:       floatPtr(-cents / 100),
		Source:     "xai_management_api(prepaid/balance)",
		Confidence: "exact",
		Message:    "prepaid credit balance in USD",
	}}
}
