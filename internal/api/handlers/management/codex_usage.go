package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexUsagePollInterval   = 60 * time.Second
	codexUsageRequestTimeout = 20 * time.Second
	codexUsageDefaultBaseURL = "https://chatgpt.com/backend-api"
	codexFreePlanWeight      = 0.2
	codexUsageStateFileName  = ".codex-usage-cache.json"
)

type codexUsageWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int   `json:"limit_window_seconds"`
	ResetAfterSeconds  int   `json:"reset_after_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

type codexUsageRateLimit struct {
	Allowed         bool              `json:"allowed"`
	LimitReached    bool              `json:"limit_reached"`
	PrimaryWindow   *codexUsageWindow `json:"primary_window,omitempty"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window,omitempty"`
}

type codexUsageCredits struct {
	HasCredits          bool    `json:"has_credits"`
	Unlimited           bool    `json:"unlimited"`
	Balance             *string `json:"balance,omitempty"`
	ApproxLocalMessages []any   `json:"approx_local_messages,omitempty"`
	ApproxCloudMessages []any   `json:"approx_cloud_messages,omitempty"`
}

type codexUsageAdditionalRateLimit struct {
	LimitName      string               `json:"limit_name"`
	MeteredFeature string               `json:"metered_feature"`
	RateLimit      *codexUsageRateLimit `json:"rate_limit,omitempty"`
}

type codexUsagePayload struct {
	PlanType             string                          `json:"plan_type"`
	RateLimit            *codexUsageRateLimit            `json:"rate_limit,omitempty"`
	Credits              *codexUsageCredits              `json:"credits,omitempty"`
	AdditionalRateLimits []codexUsageAdditionalRateLimit `json:"additional_rate_limits,omitempty"`
}

type codexAuthUsageStatus struct {
	AuthID        string             `json:"auth_id"`
	FileName      string             `json:"file_name,omitempty"`
	Email         string             `json:"email,omitempty"`
	AccountID     string             `json:"account_id,omitempty"`
	BaseURL       string             `json:"base_url,omitempty"`
	PathStyle     string             `json:"path_style,omitempty"`
	Status        string             `json:"status"`
	Error         string             `json:"error,omitempty"`
	LastPolledAt  time.Time          `json:"last_polled_at,omitempty"`
	LastSuccessAt *time.Time         `json:"last_success_at,omitempty"`
	HasUsage      bool               `json:"has_usage"`
	Usage         *codexUsagePayload `json:"usage,omitempty"`
}

type codexUsageWindowTotals struct {
	AuthFiles           int     `json:"auth_files"`
	UsedPercentSum      int     `json:"used_percent_sum"`
	TotalPercent        int     `json:"total_percent"`
	RemainingPercentSum int     `json:"remaining_percent_sum"`
	AverageUsedPercent  int     `json:"average_used_percent"`
	ProgressPercent     float64 `json:"progress_percent"`
	MinResetAfterSecond int     `json:"min_reset_after_seconds,omitempty"`
	MinResetAt          int64   `json:"min_reset_at,omitempty"`
}

type codexAdditionalRateLimitTotals struct {
	LimitName       string                  `json:"limit_name"`
	MeteredFeature  string                  `json:"metered_feature"`
	PrimaryWindow   *codexUsageWindowTotals `json:"primary_window,omitempty"`
	SecondaryWindow *codexUsageWindowTotals `json:"secondary_window,omitempty"`
}

type codexUsageTotalSummary struct {
	PrimaryWindow        *codexUsageWindowTotals          `json:"primary_window,omitempty"`
	SecondaryWindow      *codexUsageWindowTotals          `json:"secondary_window,omitempty"`
	AdditionalRateLimits []codexAdditionalRateLimitTotals `json:"additional_rate_limits,omitempty"`
}

type codexUsageSummaryResponse struct {
	UpdatedAt           time.Time              `json:"updated_at"`
	PollIntervalSeconds int                    `json:"poll_interval_seconds"`
	AuthFilesTotal      int                    `json:"auth_files_total"`
	AuthFilesWithUsage  int                    `json:"auth_files_with_usage"`
	AuthFilesWithErrors int                    `json:"auth_files_with_errors"`
	SelectedAuthID      string                 `json:"selected_auth_id,omitempty"`
	CompatPayload       codexUsagePayload      `json:"compat_payload"`
	Total               codexUsageTotalSummary `json:"total"`
	AuthFiles           []codexAuthUsageStatus `json:"auth_files"`
}

type codexUsagePersistentState struct {
	UpdatedAt      time.Time                       `json:"updated_at"`
	SelectedAuthID string                          `json:"selected_auth_id,omitempty"`
	ByAuth         map[string]codexAuthUsageStatus `json:"by_auth"`
	CompatPayload  codexUsagePayload               `json:"compat_payload"`
	Summary        codexUsageSummaryResponse       `json:"summary"`
	HasData        bool                            `json:"has_data"`
}

type codexUsageHTTPError struct {
	StatusCode int
	Preview    string
}

func (e *codexUsageHTTPError) Error() string {
	if e == nil {
		return "usage request failed"
	}
	if strings.TrimSpace(e.Preview) == "" {
		return fmt.Sprintf("usage request failed: status=%d", e.StatusCode)
	}
	return fmt.Sprintf("usage request failed: status=%d body=%s", e.StatusCode, e.Preview)
}

type codexWindowAccumulator struct {
	count                  int
	weightSum              float64
	usedPercentWeightedSum float64
	limitWindowWeightedSum float64
	minResetAfter          int
	minResetAt             int64
}

func (a *codexWindowAccumulator) add(window *codexUsageWindow, weight float64) {
	if window == nil {
		return
	}
	if weight <= 0 {
		weight = 1
	}
	a.count++
	a.weightSum += weight
	a.usedPercentWeightedSum += float64(window.UsedPercent) * weight
	a.limitWindowWeightedSum += float64(window.LimitWindowSeconds) * weight
	if window.ResetAfterSeconds > 0 && (a.minResetAfter == 0 || window.ResetAfterSeconds < a.minResetAfter) {
		a.minResetAfter = window.ResetAfterSeconds
	}
	if window.ResetAt > 0 && (a.minResetAt == 0 || window.ResetAt < a.minResetAt) {
		a.minResetAt = window.ResetAt
	}
}

func (a *codexWindowAccumulator) averageWindow() *codexUsageWindow {
	if a == nil || a.count == 0 || a.weightSum <= 0 {
		return nil
	}
	return &codexUsageWindow{
		UsedPercent:        int(math.Round(a.usedPercentWeightedSum / a.weightSum)),
		LimitWindowSeconds: int(math.Round(a.limitWindowWeightedSum / a.weightSum)),
		ResetAfterSeconds:  a.minResetAfter,
		ResetAt:            a.minResetAt,
	}
}

func (a *codexWindowAccumulator) totals() *codexUsageWindowTotals {
	if a == nil || a.count == 0 || a.weightSum <= 0 {
		return nil
	}
	totalPercentFloat := a.weightSum * 100
	totalPercent := int(math.Round(totalPercentFloat))
	usedPercentSum := int(math.Round(a.usedPercentWeightedSum))
	progress := 0.0
	if totalPercentFloat > 0 {
		progress = (a.usedPercentWeightedSum / totalPercentFloat) * 100
	}
	return &codexUsageWindowTotals{
		AuthFiles:           a.count,
		UsedPercentSum:      usedPercentSum,
		TotalPercent:        totalPercent,
		RemainingPercentSum: totalPercent - usedPercentSum,
		AverageUsedPercent:  int(math.Round(a.usedPercentWeightedSum / a.weightSum)),
		ProgressPercent:     math.Round(progress*100) / 100,
		MinResetAfterSecond: a.minResetAfter,
		MinResetAt:          a.minResetAt,
	}
}

type codexRateLimitAccumulator struct {
	hasAny          bool
	allowedAny      bool
	limitReachedAll bool
	primaryWindow   codexWindowAccumulator
	secondaryWindow codexWindowAccumulator
}

func (a *codexRateLimitAccumulator) add(rate *codexUsageRateLimit, weight float64) {
	if rate == nil {
		return
	}
	if !a.hasAny {
		a.limitReachedAll = true
	}
	a.hasAny = true
	a.allowedAny = a.allowedAny || rate.Allowed
	if !rate.LimitReached {
		a.limitReachedAll = false
	}
	a.primaryWindow.add(rate.PrimaryWindow, weight)
	a.secondaryWindow.add(rate.SecondaryWindow, weight)
}

func (a *codexRateLimitAccumulator) averageRateLimit() *codexUsageRateLimit {
	if a == nil || !a.hasAny {
		return nil
	}
	return &codexUsageRateLimit{
		Allowed:         a.allowedAny,
		LimitReached:    a.limitReachedAll,
		PrimaryWindow:   a.primaryWindow.averageWindow(),
		SecondaryWindow: a.secondaryWindow.averageWindow(),
	}
}

type codexAdditionalAccumulator struct {
	limitName      string
	meteredFeature string
	rate           codexRateLimitAccumulator
}

func defaultCodexUsagePayload() codexUsagePayload {
	return codexUsagePayload{PlanType: "guest"}
}

// refreshCodexUsageFromCacheTTL updates codex usage cache on demand.
// It never polls upstream unless a specific auth file cache TTL has expired.
func (h *Handler) refreshCodexUsageFromCacheTTL(ctx context.Context) {
	if h == nil {
		return
	}
	h.codexUsagePollMu.Lock()
	defer h.codexUsagePollMu.Unlock()

	now := time.Now().UTC()
	manager := h.authManager
	if manager == nil {
		return
	}

	selectedAuthID := strings.TrimSpace(manager.SelectedAuthID("codex"))
	h.codexUsageMu.RLock()
	selectedChanged := strings.TrimSpace(h.codexUsageSelected) != selectedAuthID
	h.codexUsageMu.RUnlock()

	previous := h.codexUsageByAuthSnapshot()
	current := make(map[string]codexAuthUsageStatus, len(previous))
	for key, value := range previous {
		current[key] = value
	}

	auths := manager.List()
	codexAuths := make(map[string]*coreauth.Auth)
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		codexAuths[strings.TrimSpace(auth.ID)] = auth
	}
	changed := false
	for authID := range current {
		if _, ok := codexAuths[authID]; !ok {
			delete(current, authID)
			changed = true
		}
	}

	if len(codexAuths) == 0 {
		if changed || selectedChanged {
			h.updateCodexUsageState(current, selectedAuthID, now, true)
		}
		return
	}

	authIDs := make([]string, 0, len(codexAuths))
	for authID := range codexAuths {
		authIDs = append(authIDs, authID)
	}
	sort.Strings(authIDs)

	for _, authID := range authIDs {
		auth := codexAuths[authID]
		status, ok := current[authID]
		if !ok {
			status = codexAuthUsageStatus{
				AuthID: authID,
			}
		}
		status.AuthID = authID
		status.FileName = strings.TrimSpace(auth.FileName)
		status.Email = authEmail(auth)
		status.AccountID = extractCodexAccountID(auth)
		if status.Status == "" {
			status.Status = "skipped"
		}

		shouldPoll := status.LastPolledAt.IsZero() || now.Sub(status.LastPolledAt) >= codexUsagePollInterval
		if !shouldPoll {
			current[authID] = status
			continue
		}

		changed = true
		token := extractCodexAccessToken(auth)
		status.LastPolledAt = now
		if token == "" {
			status.Status = "error"
			status.Error = "missing access_token"
			status.Usage = nil
			status.HasUsage = false
			status.LastSuccessAt = nil
			current[authID] = status
			continue
		}

		pollCtx := ctx
		var cancel context.CancelFunc
		if pollCtx == nil {
			pollCtx = context.Background()
		}
		pollCtx, cancel = context.WithTimeout(pollCtx, codexUsageRequestTimeout)
		payload, baseURL, pathStyle, err := h.fetchCodexUsagePayload(pollCtx, auth, token, status.AccountID)
		cancel()
		status.BaseURL = baseURL
		status.PathStyle = pathStyle
		if err != nil {
			status.Status = "error"
			status.Error = err.Error()
			if httpErr, ok := err.(*codexUsageHTTPError); ok && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
				// Credential is no longer valid; clear stale usage cache for this auth.
				status.Usage = nil
				status.HasUsage = false
				status.LastSuccessAt = nil
			}
			current[authID] = status
			log.WithError(err).Debugf("codex usage poll failed for auth %s", status.AuthID)
			continue
		}

		status.Status = "ok"
		status.Error = ""
		copied := payload
		status.Usage = &copied
		status.HasUsage = true
		lastSuccess := now
		status.LastSuccessAt = &lastSuccess
		current[authID] = status
	}

	if changed || selectedChanged {
		h.updateCodexUsageState(current, selectedAuthID, now, true)
	}
}

func (h *Handler) updateCodexUsageState(current map[string]codexAuthUsageStatus, selectedAuthID string, now time.Time, persist bool) {
	if h == nil {
		return
	}
	compatPayload, totalSummary, withUsage := aggregateCodexUsage(current)
	authErrors := 0
	authList := make([]codexAuthUsageStatus, 0, len(current))
	for _, item := range current {
		if item.Status == "error" {
			authErrors++
		}
		authList = append(authList, cloneCodexAuthUsageStatus(item))
	}
	sort.Slice(authList, func(i, j int) bool {
		left := strings.TrimSpace(authList[i].FileName)
		right := strings.TrimSpace(authList[j].FileName)
		if left == right {
			return authList[i].AuthID < authList[j].AuthID
		}
		if left == "" {
			return false
		}
		if right == "" {
			return true
		}
		return left < right
	})

	summary := codexUsageSummaryResponse{
		UpdatedAt:           now,
		PollIntervalSeconds: int(codexUsagePollInterval / time.Second),
		AuthFilesTotal:      len(current),
		AuthFilesWithUsage:  withUsage,
		AuthFilesWithErrors: authErrors,
		SelectedAuthID:      selectedAuthID,
		CompatPayload:       compatPayload,
		Total:               totalSummary,
		AuthFiles:           authList,
	}

	h.codexUsageMu.Lock()
	h.codexUsageByAuth = current
	h.codexUsageCompat = compatPayload
	h.codexUsageSummary = summary
	h.codexUsageHasData = withUsage > 0
	h.codexUsageSelected = selectedAuthID
	h.codexUsageMu.Unlock()
	if persist {
		h.persistCodexUsageState()
	}
}

func aggregateCodexUsage(authStatuses map[string]codexAuthUsageStatus) (codexUsagePayload, codexUsageTotalSummary, int) {
	compat := defaultCodexUsagePayload()
	var totals codexUsageTotalSummary

	planCounts := map[string]int{}
	mainRate := codexRateLimitAccumulator{}
	additional := map[string]*codexAdditionalAccumulator{}
	withUsage := 0

	hasCreditsPayload := false
	hasCreditsAny := false
	unlimitedAny := false
	balanceSum := 0.0
	balanceCount := 0

	for _, status := range authStatuses {
		if status.Usage == nil {
			continue
		}
		withUsage++
		usage := status.Usage
		plan := strings.TrimSpace(usage.PlanType)
		if plan != "" {
			planCounts[plan]++
		}
		weight := codexPlanWeight(plan)
		mainRate.add(usage.RateLimit, weight)

		if usage.Credits != nil {
			hasCreditsPayload = true
			hasCreditsAny = hasCreditsAny || usage.Credits.HasCredits
			unlimitedAny = unlimitedAny || usage.Credits.Unlimited
			if usage.Credits.Balance != nil {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(*usage.Credits.Balance), 64); err == nil {
					balanceSum += parsed
					balanceCount++
				}
			}
		}

		for i := range usage.AdditionalRateLimits {
			item := usage.AdditionalRateLimits[i]
			key := strings.TrimSpace(item.LimitName) + "|" + strings.TrimSpace(item.MeteredFeature)
			acc, ok := additional[key]
			if !ok {
				acc = &codexAdditionalAccumulator{
					limitName:      strings.TrimSpace(item.LimitName),
					meteredFeature: strings.TrimSpace(item.MeteredFeature),
				}
				additional[key] = acc
			}
			acc.rate.add(item.RateLimit, weight)
		}
	}

	if withUsage == 0 {
		return compat, totals, 0
	}

	compat.PlanType = dominantPlanType(planCounts)
	compat.RateLimit = mainRate.averageRateLimit()
	totals.PrimaryWindow = mainRate.primaryWindow.totals()
	totals.SecondaryWindow = mainRate.secondaryWindow.totals()

	if hasCreditsPayload {
		credits := &codexUsageCredits{
			HasCredits: hasCreditsAny,
			Unlimited:  unlimitedAny,
		}
		if balanceCount > 0 {
			balance := strconv.FormatFloat(balanceSum, 'f', -1, 64)
			credits.Balance = &balance
		}
		compat.Credits = credits
	}

	if len(additional) > 0 {
		keys := make([]string, 0, len(additional))
		for key := range additional {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		compat.AdditionalRateLimits = make([]codexUsageAdditionalRateLimit, 0, len(keys))
		totals.AdditionalRateLimits = make([]codexAdditionalRateLimitTotals, 0, len(keys))
		for _, key := range keys {
			acc := additional[key]
			compat.AdditionalRateLimits = append(compat.AdditionalRateLimits, codexUsageAdditionalRateLimit{
				LimitName:      acc.limitName,
				MeteredFeature: acc.meteredFeature,
				RateLimit:      acc.rate.averageRateLimit(),
			})
			totals.AdditionalRateLimits = append(totals.AdditionalRateLimits, codexAdditionalRateLimitTotals{
				LimitName:       acc.limitName,
				MeteredFeature:  acc.meteredFeature,
				PrimaryWindow:   acc.rate.primaryWindow.totals(),
				SecondaryWindow: acc.rate.secondaryWindow.totals(),
			})
		}
	}

	return compat, totals, withUsage
}

func dominantPlanType(planCounts map[string]int) string {
	if len(planCounts) == 0 {
		return "guest"
	}
	type pair struct {
		plan  string
		count int
	}
	plans := make([]pair, 0, len(planCounts))
	for plan, count := range planCounts {
		plans = append(plans, pair{plan: plan, count: count})
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].count == plans[j].count {
			return plans[i].plan < plans[j].plan
		}
		return plans[i].count > plans[j].count
	})
	return plans[0].plan
}

func codexPlanWeight(planType string) float64 {
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "free":
		return codexFreePlanWeight
	default:
		return 1.0
	}
}

func (h *Handler) codexUsageStateFilePath() string {
	if h == nil {
		return ""
	}
	baseDir := ""
	if cfgPath := strings.TrimSpace(h.configFilePath); cfgPath != "" {
		baseDir = filepath.Dir(cfgPath)
	} else if h.cfg != nil {
		baseDir = strings.TrimSpace(h.cfg.AuthDir)
	}
	if baseDir == "" {
		return ""
	}
	return filepath.Join(baseDir, codexUsageStateFileName)
}

func (h *Handler) loadCodexUsageState() {
	if h == nil {
		return
	}
	path := h.codexUsageStateFilePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state codexUsagePersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		log.WithError(err).Debugf("failed to load codex usage cache from %s", path)
		return
	}
	if state.ByAuth == nil {
		state.ByAuth = make(map[string]codexAuthUsageStatus)
	}
	compat := cloneCodexUsagePayload(&state.CompatPayload)
	summary := state.Summary
	summary.CompatPayload = cloneCodexUsagePayload(&summary.CompatPayload)
	if summary.PollIntervalSeconds <= 0 {
		summary.PollIntervalSeconds = int(codexUsagePollInterval / time.Second)
	}
	if summary.SelectedAuthID == "" {
		summary.SelectedAuthID = strings.TrimSpace(state.SelectedAuthID)
	}
	if len(summary.AuthFiles) == 0 && len(state.ByAuth) > 0 {
		authList := make([]codexAuthUsageStatus, 0, len(state.ByAuth))
		for _, item := range state.ByAuth {
			authList = append(authList, cloneCodexAuthUsageStatus(item))
		}
		sort.Slice(authList, func(i, j int) bool {
			return authList[i].AuthID < authList[j].AuthID
		})
		summary.AuthFiles = authList
	}

	h.codexUsageMu.Lock()
	h.codexUsageByAuth = make(map[string]codexAuthUsageStatus, len(state.ByAuth))
	for key, value := range state.ByAuth {
		h.codexUsageByAuth[key] = cloneCodexAuthUsageStatus(value)
	}
	h.codexUsageCompat = compat
	h.codexUsageSummary = summary
	h.codexUsageHasData = state.HasData
	h.codexUsageSelected = strings.TrimSpace(summary.SelectedAuthID)
	h.codexUsageMu.Unlock()
}

func (h *Handler) persistCodexUsageState() {
	if h == nil {
		return
	}
	path := h.codexUsageStateFilePath()
	if path == "" {
		return
	}
	compat, summary, hasData := h.codexUsageSnapshot()
	byAuth := h.codexUsageByAuthSnapshot()
	state := codexUsagePersistentState{
		UpdatedAt:      summary.UpdatedAt,
		SelectedAuthID: strings.TrimSpace(summary.SelectedAuthID),
		ByAuth:         byAuth,
		CompatPayload:  compat,
		Summary:        summary,
		HasData:        hasData,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		log.WithError(err).Debug("failed to marshal codex usage cache")
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.WithError(err).Debugf("failed to create codex usage cache directory %s", dir)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		log.WithError(err).Debugf("failed to write codex usage cache temp file %s", tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.WithError(err).Debugf("failed to commit codex usage cache file %s", path)
		_ = os.Remove(tmp)
	}
}

func (h *Handler) fetchCodexUsagePayload(ctx context.Context, auth *coreauth.Auth, token, accountID string) (codexUsagePayload, string, string, error) {
	payload := defaultCodexUsagePayload()
	baseURL, pathStyle := resolveCodexUsageBaseURL(auth)
	usageURL := buildCodexUsageURL(baseURL, pathStyle)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return payload, baseURL, pathStyle, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-cli")
	if strings.TrimSpace(accountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", strings.TrimSpace(accountID))
	}

	httpClient := &http.Client{
		Timeout:   codexUsageRequestTimeout,
		Transport: h.apiCallTransport(auth),
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return payload, baseURL, pathStyle, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return payload, baseURL, pathStyle, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 240 {
			preview = preview[:240]
		}
		return payload, baseURL, pathStyle, &codexUsageHTTPError{
			StatusCode: resp.StatusCode,
			Preview:    preview,
		}
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return payload, baseURL, pathStyle, fmt.Errorf("decode usage payload: %w", err)
	}
	if strings.TrimSpace(payload.PlanType) == "" {
		payload.PlanType = "guest"
	}
	return payload, baseURL, pathStyle, nil
}

func resolveCodexUsageBaseURL(auth *coreauth.Auth) (string, string) {
	raw := ""
	if auth != nil && auth.Attributes != nil {
		raw = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if raw == "" && auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			raw = strings.TrimSpace(v)
		}
	}
	if raw == "" {
		raw = codexUsageDefaultBaseURL
	}
	return normalizeCodexUsageBaseURL(raw)
}

func normalizeCodexUsageBaseURL(base string) (string, string) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = codexUsageDefaultBaseURL
	}
	base = strings.TrimRight(base, "/")

	if (strings.HasPrefix(base, "https://chatgpt.com") || strings.HasPrefix(base, "https://chat.openai.com")) && !strings.Contains(base, "/backend-api") {
		base = base + "/backend-api"
	}

	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/codex") {
		base = strings.TrimSuffix(base, "/codex")
	}

	if strings.Contains(base, "/backend-api") {
		return base, "wham"
	}
	return base, "api"
}

func buildCodexUsageURL(baseURL, pathStyle string) string {
	switch pathStyle {
	case "wham":
		return strings.TrimRight(baseURL, "/") + "/wham/usage"
	default:
		return strings.TrimRight(baseURL, "/") + "/api/codex/usage"
	}
}

func extractCodexAccessToken(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if token := metadataString(auth.Metadata, "access_token", "accessToken"); token != "" {
		return token
	}
	if raw, ok := auth.Metadata["token"]; ok {
		switch typed := raw.(type) {
		case map[string]any:
			return metadataString(typed, "access_token", "accessToken")
		case map[string]string:
			if token := strings.TrimSpace(typed["access_token"]); token != "" {
				return token
			}
			if token := strings.TrimSpace(typed["accessToken"]); token != "" {
				return token
			}
		}
	}
	return ""
}

func metadataString(metadata map[string]any, keys ...string) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range keys {
		if raw, ok := metadata[key]; ok {
			if text, ok := raw.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func extractCodexAccountID(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if accountID := metadataString(auth.Metadata, "account_id", "chatgpt_account_id", "chatgptAccountId"); accountID != "" {
		return accountID
	}
	idToken := metadataString(auth.Metadata, "id_token")
	if idToken == "" {
		return ""
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.GetAccountID())
}

func cloneCodexUsagePayload(payload *codexUsagePayload) codexUsagePayload {
	if payload == nil {
		return defaultCodexUsagePayload()
	}
	cloned := codexUsagePayload{
		PlanType: strings.TrimSpace(payload.PlanType),
	}
	if cloned.PlanType == "" {
		cloned.PlanType = "guest"
	}
	if payload.RateLimit != nil {
		clonedRate := *payload.RateLimit
		clonedRate.PrimaryWindow = cloneCodexUsageWindow(payload.RateLimit.PrimaryWindow)
		clonedRate.SecondaryWindow = cloneCodexUsageWindow(payload.RateLimit.SecondaryWindow)
		cloned.RateLimit = &clonedRate
	}
	if payload.Credits != nil {
		clonedCredits := *payload.Credits
		if payload.Credits.Balance != nil {
			b := *payload.Credits.Balance
			clonedCredits.Balance = &b
		}
		if len(payload.Credits.ApproxLocalMessages) > 0 {
			clonedCredits.ApproxLocalMessages = append([]any(nil), payload.Credits.ApproxLocalMessages...)
		}
		if len(payload.Credits.ApproxCloudMessages) > 0 {
			clonedCredits.ApproxCloudMessages = append([]any(nil), payload.Credits.ApproxCloudMessages...)
		}
		cloned.Credits = &clonedCredits
	}
	if len(payload.AdditionalRateLimits) > 0 {
		cloned.AdditionalRateLimits = make([]codexUsageAdditionalRateLimit, 0, len(payload.AdditionalRateLimits))
		for i := range payload.AdditionalRateLimits {
			item := payload.AdditionalRateLimits[i]
			copiedItem := codexUsageAdditionalRateLimit{
				LimitName:      item.LimitName,
				MeteredFeature: item.MeteredFeature,
			}
			if item.RateLimit != nil {
				clonedRate := *item.RateLimit
				clonedRate.PrimaryWindow = cloneCodexUsageWindow(item.RateLimit.PrimaryWindow)
				clonedRate.SecondaryWindow = cloneCodexUsageWindow(item.RateLimit.SecondaryWindow)
				copiedItem.RateLimit = &clonedRate
			}
			cloned.AdditionalRateLimits = append(cloned.AdditionalRateLimits, copiedItem)
		}
	}
	return cloned
}

func cloneCodexUsageWindow(window *codexUsageWindow) *codexUsageWindow {
	if window == nil {
		return nil
	}
	cloned := *window
	return &cloned
}

func cloneTimePointer(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	copied := *ts
	return &copied
}

func cloneCodexAuthUsageStatus(input codexAuthUsageStatus) codexAuthUsageStatus {
	out := input
	out.LastSuccessAt = cloneTimePointer(input.LastSuccessAt)
	if input.Usage != nil {
		cloned := cloneCodexUsagePayload(input.Usage)
		out.Usage = &cloned
	}
	return out
}

func (h *Handler) codexUsageByAuthSnapshot() map[string]codexAuthUsageStatus {
	if h == nil {
		return nil
	}
	h.codexUsageMu.RLock()
	defer h.codexUsageMu.RUnlock()
	out := make(map[string]codexAuthUsageStatus, len(h.codexUsageByAuth))
	for key, value := range h.codexUsageByAuth {
		out[key] = cloneCodexAuthUsageStatus(value)
	}
	return out
}

func (h *Handler) codexUsageSnapshot() (codexUsagePayload, codexUsageSummaryResponse, bool) {
	if h == nil {
		empty := defaultCodexUsagePayload()
		return empty, codexUsageSummaryResponse{
			PollIntervalSeconds: int(codexUsagePollInterval / time.Second),
			CompatPayload:       empty,
		}, false
	}
	h.codexUsageMu.RLock()
	defer h.codexUsageMu.RUnlock()

	compat := cloneCodexUsagePayload(&h.codexUsageCompat)
	summary := h.codexUsageSummary
	summary.CompatPayload = cloneCodexUsagePayload(&h.codexUsageSummary.CompatPayload)
	if len(h.codexUsageSummary.AuthFiles) > 0 {
		summary.AuthFiles = make([]codexAuthUsageStatus, 0, len(h.codexUsageSummary.AuthFiles))
		for i := range h.codexUsageSummary.AuthFiles {
			summary.AuthFiles = append(summary.AuthFiles, cloneCodexAuthUsageStatus(h.codexUsageSummary.AuthFiles[i]))
		}
	}
	return compat, summary, h.codexUsageHasData
}

func (h *Handler) GetCodexUsageCompat(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusOK, defaultCodexUsagePayload())
		return
	}
	h.refreshCodexUsageFromCacheTTL(c.Request.Context())
	compat, _, _ := h.codexUsageSnapshot()
	c.JSON(http.StatusOK, compat)
}

func (h *Handler) GetCodexUsageSummary(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "handler not initialized"})
		return
	}
	h.refreshCodexUsageFromCacheTTL(c.Request.Context())
	_, summary, _ := h.codexUsageSnapshot()
	c.JSON(http.StatusOK, summary)
}
