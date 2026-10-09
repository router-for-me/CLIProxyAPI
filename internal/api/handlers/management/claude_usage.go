package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const claudeOAuthUsageURL = "https://api.anthropic.com/api/oauth/usage"

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudeExtraUsage struct {
	IsEnabled         *bool    `json:"is_enabled"`
	MonthlyLimit      *float64 `json:"monthly_limit"`
	UsedCredits       *float64 `json:"used_credits"`
	Utilization       *float64 `json:"utilization"`
	Currency          *string  `json:"currency"`
	DisabledReason    *string  `json:"disabled_reason"`
	UserDisabled      *bool    `json:"user_disabled"`
	SpendLimitReached *bool    `json:"spend_limit_reached"`
}

type claudeDollarWindow struct {
	LimitDollars     *float64 `json:"limit_dollars"`
	UsedDollars      *float64 `json:"used_dollars"`
	RemainingDollars *float64 `json:"remaining_dollars"`
	Utilization      *float64 `json:"utilization"`
	ResetsAt         *string  `json:"resets_at"`
}

// These observations never enter Auth.Quota, which belongs to request routing.
type claudeUsageSnapshot struct {
	ObservedAt    time.Time                      `json:"observed_at"`
	FiveHour      *claudeUsageWindow             `json:"five_hour"`
	SevenDay      *claudeUsageWindow             `json:"seven_day"`
	ExtraUsage    *claudeExtraUsage              `json:"extra_usage"`
	DollarWindows map[string]*claudeDollarWindow `json:"dollar_windows"`
}

type claudeUsageCacheEntry struct {
	epoch    uint64
	identity claudeUsageIdentity
	snapshot *claudeUsageSnapshot
	body     string
	stale    bool
}

type claudeUsageIdentity struct {
	account      string
	organization string
	email        string
	version      uint64
}

func claudeUsageIdentityFor(auth *coreauth.Auth) claudeUsageIdentity {
	metadataString := func(key string) string {
		value, _ := auth.Metadata[key].(string)
		return strings.TrimSpace(value)
	}
	identity := claudeUsageIdentity{
		account: metadataString("account_uuid"), organization: metadataString("organization_uuid"),
	}
	if identity.account == "" {
		identity.email = strings.ToLower(metadataString("email"))
		if identity.email == "" {
			// Without an account identity, never carry a balance across a secret replacement.
			identity.version = auth.CredentialVersion
		}
	}
	return identity
}

func isClaudeOAuthUsageCall(method string, target *url.URL, auth *coreauth.Auth) bool {
	return method == http.MethodGet && target != nil && target.String() == claudeOAuthUsageURL &&
		isClaudeOAuthCredential(auth)
}

func isClaudeOAuthCredential(auth *coreauth.Auth) bool {
	return auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") &&
		auth.AuthKind() == coreauth.AuthKindOAuth
}

func parseClaudeUsage(data []byte) (*claudeUsageSnapshot, error) {
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(data, &fields); errDecode != nil || fields == nil {
		return nil, errors.New("invalid Claude usage response")
	}
	snapshot := &claudeUsageSnapshot{DollarWindows: make(map[string]*claudeDollarWindow)}
	recognized := false
	for _, plan := range []struct {
		key string
		dst **claudeUsageWindow
	}{{"five_hour", &snapshot.FiveHour}, {"seven_day", &snapshot.SevenDay}} {
		raw, exists := fields[plan.key]
		recognized = recognized || exists
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var window claudeUsageWindow
		if !hasUsageFields(raw, "utilization", "resets_at") || json.Unmarshal(raw, &window) != nil {
			return nil, errors.New("invalid Claude plan window")
		}
		*plan.dst = &window
	}
	if raw, exists := fields["extra_usage"]; exists {
		recognized = true
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			var extra claudeExtraUsage
			if !hasUsageFields(raw, "is_enabled") || json.Unmarshal(raw, &extra) != nil {
				return nil, errors.New("invalid Claude extra usage")
			}
			snapshot.ExtraUsage = &extra
		}
	}
	for key, raw := range fields {
		if key == "five_hour" || key == "seven_day" || key == "extra_usage" {
			continue
		}
		if !hasUsageFields(raw, "limit_dollars", "used_dollars", "remaining_dollars", "utilization", "resets_at") {
			if hasUsageFields(raw, "utilization", "resets_at") {
				var window claudeUsageWindow
				if json.Unmarshal(raw, &window) != nil {
					return nil, errors.New("invalid Claude plan window")
				}
				recognized = true
			}
			continue
		}
		var window claudeDollarWindow
		if errDecode := json.Unmarshal(raw, &window); errDecode != nil {
			return nil, errors.New("invalid Claude dollar window")
		}
		snapshot.DollarWindows[key] = &window
	}
	// Keep the raw response compatible with newer scoped plan-window payloads.
	if raw, exists := fields["limits"]; exists {
		var limits []json.RawMessage
		if json.Unmarshal(raw, &limits) != nil {
			return nil, errors.New("invalid Claude scoped windows")
		}
		for _, limit := range limits {
			recognized = recognized || hasUsageFields(limit, "kind", "percent", "resets_at")
		}
	}
	if !recognized && len(snapshot.DollarWindows) == 0 {
		return nil, errors.New("Claude usage response contains no usage windows")
	}
	return snapshot, nil
}

func hasUsageFields(raw json.RawMessage, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, key := range keys {
		if _, exists := fields[key]; !exists {
			return false
		}
	}
	return true
}

func (h *Handler) cachedClaudeUsage(auth *coreauth.Auth) *claudeUsageCacheEntry {
	h.claudeUsageMu.Lock()
	defer h.claudeUsageMu.Unlock()
	entry := h.cachedClaudeUsageLocked(auth)
	if entry == nil {
		return nil
	}
	copyEntry := *entry
	return &copyEntry
}

func (h *Handler) cachedClaudeUsageLocked(auth *coreauth.Auth) *claudeUsageCacheEntry {
	entry := h.claudeUsage[auth.ID]
	if entry == nil {
		return nil
	}
	current, exists := h.authManager.GetByID(auth.ID)
	if !exists || !isClaudeOAuthCredential(current) || entry.epoch != current.RegistrationEpoch ||
		entry.identity != claudeUsageIdentityFor(current) {
		delete(h.claudeUsage, auth.ID)
		return nil
	}
	if current.RegistrationEpoch != auth.RegistrationEpoch ||
		claudeUsageIdentityFor(current) != claudeUsageIdentityFor(auth) {
		return nil
	}
	return entry
}

func (h *Handler) addClaudeUsageObservation(c *gin.Context, auth *coreauth.Auth, entry gin.H) {
	if !c.GetBool(ConfigV8ContextKey) || !isClaudeOAuthCredential(auth) {
		return
	}
	if cached := h.cachedClaudeUsage(auth); cached != nil {
		entry["claude_usage"] = cached.snapshot
		entry["claude_usage_stale"] = cached.stale
	}
}

func (h *Handler) claudeUsageFailure(c *gin.Context, auth *coreauth.Auth, status int, message string) {
	response := apiCallResponse{
		StatusCode: status,
		Header:     map[string][]string{},
		Body:       `{"error":"` + message + `"}`,
		Error:      message,
	}
	h.claudeUsageMu.Lock()
	if cached := h.cachedClaudeUsageLocked(auth); cached != nil {
		cached.stale = true
		response.Body = cached.body
		response.ClaudeUsage = cached.snapshot
		response.Stale = true
	}
	h.claudeUsageMu.Unlock()
	c.JSON(http.StatusOK, response)
}

func (h *Handler) callClaudeOAuthUsage(c *gin.Context, auth *coreauth.Auth, proxyURL string) {
	refreshed, rotated, errRefresh := h.authManager.RefreshAuthForObservation(c.Request.Context(), auth)
	if errRefresh != nil {
		// Do not log upstream errors: they may contain credential material.
		h.claudeUsageFailure(c, auth, http.StatusBadGateway, "Claude usage credential refresh failed")
		return
	}
	if !isClaudeOAuthCredential(refreshed) {
		h.claudeUsageFailure(c, auth, http.StatusConflict, "Claude usage credential changed")
		return
	}
	if rotated {
		// A refresh can resolve missing account identity or rotate an unidentified
		// credential. Carry the snapshot only across our own verified rotation.
		h.claudeUsageMu.Lock()
		current, exists := h.authManager.GetByID(auth.ID)
		cached := h.claudeUsage[auth.ID]
		if exists && isClaudeOAuthCredential(current) && cached != nil &&
			current.RegistrationEpoch == refreshed.RegistrationEpoch &&
			current.CredentialVersion == refreshed.CredentialVersion &&
			claudeUsageIdentityFor(current) == claudeUsageIdentityFor(refreshed) &&
			cached.epoch == auth.RegistrationEpoch && cached.identity == claudeUsageIdentityFor(auth) {
			cached.identity = claudeUsageIdentityFor(refreshed)
		}
		h.claudeUsageMu.Unlock()
	}
	auth = refreshed
	token, _ := auth.Metadata["access_token"].(string)
	req, errRequest := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, claudeOAuthUsageURL, nil)
	if errRequest != nil {
		h.claudeUsageFailure(c, auth, http.StatusBadGateway, "Claude usage request failed")
		return
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	client := &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: h.apiCallTransport(auth, proxyURL),
		// Do not forward this OAuth credential to a redirect target.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		h.claudeUsageFailure(c, auth, http.StatusBadGateway, "Claude usage request failed")
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warn("Claude usage response body close failed")
		}
	}()
	data, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		h.claudeUsageFailure(c, auth, http.StatusBadGateway, "Claude usage response read failed")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.claudeUsageFailure(c, auth, resp.StatusCode, "Claude usage upstream request failed")
		return
	}
	snapshot, errParse := parseClaudeUsage(data)
	if errParse != nil {
		h.claudeUsageFailure(c, auth, http.StatusBadGateway, "Claude usage response is invalid")
		return
	}
	snapshot.ObservedAt = time.Now().UTC()
	h.claudeUsageMu.Lock()
	current, exists := h.authManager.GetByID(auth.ID)
	if !exists || !isClaudeOAuthCredential(current) || current.RegistrationEpoch != auth.RegistrationEpoch ||
		current.CredentialVersion != auth.CredentialVersion || claudeUsageIdentityFor(current) != claudeUsageIdentityFor(auth) {
		h.claudeUsageMu.Unlock()
		h.claudeUsageFailure(c, auth, http.StatusConflict, "Claude usage credential changed")
		return
	}
	if h.claudeUsage == nil {
		h.claudeUsage = make(map[string]*claudeUsageCacheEntry)
	}
	h.claudeUsage[auth.ID] = &claudeUsageCacheEntry{
		epoch: auth.RegistrationEpoch, identity: claudeUsageIdentityFor(auth),
		snapshot: snapshot, body: string(data),
	}
	h.claudeUsageMu.Unlock()
	c.JSON(http.StatusOK, apiCallResponse{
		StatusCode: resp.StatusCode, Header: resp.Header, Body: string(data), ClaudeUsage: snapshot,
	})
}
