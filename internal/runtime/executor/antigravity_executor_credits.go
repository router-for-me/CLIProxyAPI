package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/singleflight"
)

type antigravity429Category string

type antigravityCreditsFailureState struct {
	PermanentlyDisabled      bool
	ExplicitBalanceExhausted bool
}

type antigravity429DecisionKind string

const (
	antigravity429Unknown                         antigravity429Category     = "unknown"
	antigravity429RateLimited                     antigravity429Category     = "rate_limited"
	antigravity429QuotaExhausted                  antigravity429Category     = "quota_exhausted"
	antigravity429SoftRateLimit                   antigravity429Category     = "soft_rate_limit"
	antigravity429DecisionSoftRetry               antigravity429DecisionKind = "soft_retry"
	antigravity429DecisionInstantRetrySameAuth    antigravity429DecisionKind = "instant_retry_same_auth"
	antigravity429DecisionShortCooldownSwitchAuth antigravity429DecisionKind = "short_cooldown_switch_auth"
	antigravity429DecisionFullQuotaExhausted      antigravity429DecisionKind = "full_quota_exhausted"
)

type antigravity429Decision struct {
	kind       antigravity429DecisionKind
	retryAfter *time.Duration
	reason     string
}

var (
	randSource                        = rand.New(rand.NewSource(time.Now().UnixNano()))
	randSourceMutex                   sync.Mutex
	antigravityCreditsFailureByAuth   sync.Map
	antigravityShortCooldownByAuth    sync.Map
	antigravityCreditsBalanceByAuth   sync.Map // auth.ID → antigravityCreditsBalance
	antigravityCreditsHintRefreshByID sync.Map // auth.ID → *antigravityCreditsHintRefreshState
	antigravityRefreshGroup           singleflight.Group
	antigravityQuotaExhaustedKeywords = []string{
		"quota_exhausted",
		"quota exhausted",
	}
)

type antigravityKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	KVDel(ctx context.Context, keys ...string) (int64, error)
}

var currentAntigravityKVClient = func() (antigravityKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

type antigravityCreditsBalance struct {
	CreditAmount    float64
	MinCreditAmount float64
	PaidTierID      string
	Known           bool
}

type antigravityCreditsHintRefreshState struct {
	mu          sync.Mutex
	lastAttempt time.Time
}

type antigravityTokenRefreshData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func antigravityAuthHasCredits(auth *cliproxyauth.Auth) bool {
	ok, err := antigravityAuthHasCreditsRequired(context.Background(), auth)
	if err != nil {
		log.Errorf("antigravity executor: home kv credits check error: %v", err)
		return false
	}
	return ok
}

func antigravityAuthHasCreditsRequired(ctx context.Context, auth *cliproxyauth.Auth) (bool, error) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return false, nil
	}
	authID := strings.TrimSpace(auth.ID)
	if hint, ok, errHint := cliproxyauth.GetAntigravityCreditsHintRequired(ctx, authID); errHint != nil {
		return false, errHint
	} else if ok && hint.Known {
		return hint.Available, nil
	}

	client, homeMode, errClient := currentAntigravityKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		raw, found, errBalance := client.KVGet(ctx, antigravityCreditsBalanceKey(authID))
		if errBalance != nil {
			return false, errBalance
		}
		if !found {
			return true, nil
		}
		var homeBalance antigravityCreditsBalance
		if errUnmarshal := json.Unmarshal(raw, &homeBalance); errUnmarshal != nil {
			return false, errUnmarshal
		}
		return antigravityCreditsBalanceAvailable(authID, homeBalance), nil
	}

	val, ok := antigravityCreditsBalanceByAuth.Load(authID)
	if !ok {
		return true, nil // optimistic: assume credits available when balance unknown
	}
	bal, valid := val.(antigravityCreditsBalance)
	if !valid {
		antigravityCreditsBalanceByAuth.Delete(authID)
		return false, nil
	}
	return antigravityCreditsBalanceAvailable(authID, bal), nil
}

func antigravityCreditsBalanceAvailable(authID string, bal antigravityCreditsBalance) bool {
	if !bal.Known {
		return false
	}
	available := bal.CreditAmount >= bal.MinCreditAmount
	cliproxyauth.SetAntigravityCreditsHint(strings.TrimSpace(authID), cliproxyauth.AntigravityCreditsHint{
		Known:           true,
		Available:       available,
		CreditAmount:    bal.CreditAmount,
		MinCreditAmount: bal.MinCreditAmount,
		PaidTierID:      bal.PaidTierID,
		UpdatedAt:       time.Now(),
	})
	return available
}

// parseMetaFloat extracts a float64 from auth.Metadata (handles string and numeric types).
func parseMetaFloat(metadata map[string]any, key string) (float64, bool) {
	v, ok := metadata[key]
	if !ok {
		return 0, false
	}
	switch typed := v.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		if f, err := typed.Float64(); err == nil {
			return f, true
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}
func injectEnabledCreditTypes(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	if !gjson.ValidBytes(payload) {
		return nil
	}
	updated, err := sjson.SetRawBytes(payload, "enabledCreditTypes", []byte(`["GOOGLE_ONE_AI"]`))
	if err != nil {
		return nil
	}
	return updated
}

func classifyAntigravity429(body []byte) antigravity429Category {
	switch decideAntigravity429(body).kind {
	case antigravity429DecisionInstantRetrySameAuth, antigravity429DecisionShortCooldownSwitchAuth:
		return antigravity429RateLimited
	case antigravity429DecisionFullQuotaExhausted:
		return antigravity429QuotaExhausted
	case antigravity429DecisionSoftRetry:
		return antigravity429SoftRateLimit
	default:
		return antigravity429Unknown
	}
}

func decideAntigravity429(body []byte) antigravity429Decision {
	decision := antigravity429Decision{kind: antigravity429DecisionSoftRetry}
	if len(body) == 0 {
		return decision
	}

	if retryAfter, parseErr := helps.ParseRetryDelay(body); parseErr == nil && retryAfter != nil {
		decision.retryAfter = retryAfter
	}

	status := strings.TrimSpace(gjson.GetBytes(body, "error.status").String())
	if !strings.EqualFold(status, "RESOURCE_EXHAUSTED") {
		return decision
	}

	details := gjson.GetBytes(body, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
				continue
			}
			reason := strings.TrimSpace(detail.Get("reason").String())
			decision.reason = reason
			switch {
			case strings.EqualFold(reason, "QUOTA_EXHAUSTED"):
				decision.kind = antigravity429DecisionFullQuotaExhausted
				return decision
			case strings.EqualFold(reason, "RATE_LIMIT_EXCEEDED"):
				if decision.retryAfter == nil {
					decision.kind = antigravity429DecisionSoftRetry
					return decision
				}
				switch {
				case *decision.retryAfter < antigravityInstantRetryThreshold:
					decision.kind = antigravity429DecisionInstantRetrySameAuth
				case *decision.retryAfter < antigravityShortQuotaCooldownThreshold:
					decision.kind = antigravity429DecisionShortCooldownSwitchAuth
				default:
					decision.kind = antigravity429DecisionFullQuotaExhausted
				}
				return decision
			}
		}
	}

	lowerBody := strings.ToLower(string(body))
	for _, keyword := range antigravityQuotaExhaustedKeywords {
		if strings.Contains(lowerBody, keyword) {
			decision.kind = antigravity429DecisionFullQuotaExhausted
			decision.reason = "quota_exhausted"
			return decision
		}
	}

	decision.kind = antigravity429DecisionSoftRetry
	return decision
}

func antigravityCreditsRetryEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.QuotaExceeded.AntigravityCredits
}

func clearAntigravityCreditsFailureState(auth *cliproxyauth.Auth) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	antigravityCreditsFailureByAuth.Delete(strings.TrimSpace(auth.ID))
}
func markAntigravityCreditsPermanentlyDisabled(auth *cliproxyauth.Auth) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	state := antigravityCreditsFailureState{
		PermanentlyDisabled:      true,
		ExplicitBalanceExhausted: true,
	}
	antigravityCreditsFailureByAuth.Store(authID, state)
	bal := antigravityCreditsBalance{
		CreditAmount:    0,
		MinCreditAmount: 1,
		Known:           true,
	}
	storeAntigravityCreditsBalanceBestEffort(authID, bal)
	cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
		Known:           true,
		Available:       false,
		CreditAmount:    0,
		MinCreditAmount: 1,
		UpdatedAt:       time.Now(),
	})
}

func clearAntigravityCreditsPermanentlyDisabled(auth *cliproxyauth.Auth) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	antigravityCreditsFailureByAuth.Delete(strings.TrimSpace(auth.ID))
}

func antigravityHasExplicitCreditsBalanceExhaustedReason(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	details := gjson.GetBytes(body, "error.details")
	if !details.Exists() || !details.IsArray() {
		return false
	}
	for _, detail := range details.Array() {
		if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
			continue
		}
		reason := strings.TrimSpace(detail.Get("reason").String())
		if strings.EqualFold(reason, "INSUFFICIENT_G1_CREDITS_BALANCE") {
			return true
		}
	}
	return false
}

func newAntigravityStatusErr(statusCode int, body []byte) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests {
		if retryAfter, parseErr := helps.ParseRetryDelay(body); parseErr == nil && retryAfter != nil {
			err.retryAfter = retryAfter
		}
	}
	return err
}
func (e *AntigravityExecutor) maybeRefreshAntigravityCreditsHint(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) {
	if e == nil || auth == nil || !antigravityCreditsRetryEnabled(e.cfg) {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHint(authID); ok && hint.Known {
		return
	}
	if strings.TrimSpace(accessToken) == "" {
		accessToken = metaStringValue(auth.Metadata, "access_token")
	}
	if strings.TrimSpace(accessToken) == "" {
		return
	}

	if client, homeMode, errClient := currentAntigravityKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("antigravity executor: home kv best-effort refresh lock failed prefix=cpa:antigravity:*: %v", errClient)
			return
		}
		written, errSetNX := client.KVSetNX(context.Background(), antigravityCreditsRefreshLockKey(authID), []byte("1"), antigravityCreditsHintRefreshInterval)
		if errSetNX != nil {
			log.Errorf("antigravity executor: home kv best-effort refresh lock failed prefix=cpa:antigravity:*: %v", errSetNX)
			return
		}
		if !written {
			return
		}
		refreshCtx := context.Background()
		if ctx != nil {
			if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
				refreshCtx = context.WithValue(refreshCtx, "cliproxy.roundtripper", rt)
			}
		}
		refreshCtx, cancel := context.WithTimeout(refreshCtx, antigravityCreditsHintRefreshTimeout)
		authCopy := auth.Clone()
		go func(auth *cliproxyauth.Auth, token string) {
			defer cancel()
			e.updateAntigravityCreditsBalance(refreshCtx, auth, token)
		}(authCopy, accessToken)
		return
	}

	state := &antigravityCreditsHintRefreshState{}
	if existing, loaded := antigravityCreditsHintRefreshByID.LoadOrStore(authID, state); loaded {
		if cast, ok := existing.(*antigravityCreditsHintRefreshState); ok && cast != nil {
			state = cast
		} else {
			antigravityCreditsHintRefreshByID.Delete(authID)
			antigravityCreditsHintRefreshByID.Store(authID, state)
		}
	}

	now := time.Now()
	if !state.mu.TryLock() {
		return
	}
	if !state.lastAttempt.IsZero() && now.Sub(state.lastAttempt) < antigravityCreditsHintRefreshInterval {
		state.mu.Unlock()
		return
	}
	state.lastAttempt = now

	refreshCtx := context.Background()
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			refreshCtx = context.WithValue(refreshCtx, "cliproxy.roundtripper", rt)
		}
	}
	refreshCtx, cancel := context.WithTimeout(refreshCtx, antigravityCreditsHintRefreshTimeout)
	authCopy := auth.Clone()

	go func(state *antigravityCreditsHintRefreshState, auth *cliproxyauth.Auth, token string) {
		defer cancel()
		defer state.mu.Unlock()
		e.updateAntigravityCreditsBalance(refreshCtx, auth, token)
	}(state, authCopy, accessToken)
}

func (e *AntigravityExecutor) updateAntigravityCreditsBalance(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	token := strings.TrimSpace(accessToken)
	if token == "" {
		token = metaStringValue(auth.Metadata, "access_token")
	}
	if token == "" {
		return
	}

	userAgent := resolveUserAgent(auth)
	loadReqBody, errMarshal := json.Marshal(map[string]any{
		"metadata": map[string]string{
			"ideType": "ANTIGRAVITY",
		},
	})
	if errMarshal != nil {
		log.Debugf("antigravity executor: marshal loadCodeAssist request error: %v", errMarshal)
		return
	}
	baseURL := antigravityLoadCodeAssistBaseURL(auth)
	endpointURL := strings.TrimSuffix(baseURL, "/") + "/v1internal:loadCodeAssist"
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(loadReqBody))
	if errReq != nil {
		log.Debugf("antigravity executor: create loadCodeAssist request error: %v", errReq)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		log.Debugf("antigravity executor: loadCodeAssist request error: %v", errDo)
		return
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close loadCodeAssist response body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil || httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("antigravity executor: loadCodeAssist returned status %d, err=%v", httpResp.StatusCode, errRead)
		return
	}

	authID := strings.TrimSpace(auth.ID)
	paidTierID := strings.TrimSpace(gjson.GetBytes(bodyBytes, "paidTier.id").String())

	credits := gjson.GetBytes(bodyBytes, "paidTier.availableCredits")
	if !credits.IsArray() {
		cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
			Known:      true,
			Available:  false,
			PaidTierID: paidTierID,
			UpdatedAt:  time.Now(),
		})
		return
	}
	for _, credit := range credits.Array() {
		if !strings.EqualFold(credit.Get("creditType").String(), "GOOGLE_ONE_AI") {
			continue
		}
		creditAmount, errCA := strconv.ParseFloat(strings.TrimSpace(credit.Get("creditAmount").String()), 64)
		if errCA != nil {
			continue
		}
		minAmount, errMA := strconv.ParseFloat(strings.TrimSpace(credit.Get("minimumCreditAmountForUsage").String()), 64)
		if errMA != nil {
			continue
		}
		bal := antigravityCreditsBalance{
			CreditAmount:    creditAmount,
			MinCreditAmount: minAmount,
			PaidTierID:      paidTierID,
			Known:           true,
		}
		storeAntigravityCreditsBalanceBestEffort(authID, bal)
		cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
			Known:           true,
			Available:       creditAmount >= minAmount,
			CreditAmount:    creditAmount,
			MinCreditAmount: minAmount,
			PaidTierID:      paidTierID,
			UpdatedAt:       time.Now(),
		})
		if creditAmount >= minAmount {
			clearAntigravityCreditsPermanentlyDisabled(auth)
		}
		return
	}
}
func antigravityRetryAttempts(auth *cliproxyauth.Auth, cfg *config.Config) int {
	retry := 0
	if cfg != nil {
		retry = cfg.RequestRetry
	}
	if auth != nil {
		if override, ok := auth.RequestRetryOverride(); ok {
			retry = override
		}
	}
	if retry < 0 {
		retry = 0
	}
	attempts := retry + 1
	if attempts < 1 {
		return 1
	}
	return attempts
}

func antigravityShouldRetryNoCapacity(statusCode int, body []byte) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	if len(body) == 0 {
		return false
	}
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "no capacity available")
}

func antigravityShouldRetryTransientResourceExhausted429(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	if len(body) == 0 {
		return false
	}
	if classifyAntigravity429(body) != antigravity429Unknown {
		return false
	}
	status := strings.TrimSpace(gjson.GetBytes(body, "error.status").String())
	if !strings.EqualFold(status, "RESOURCE_EXHAUSTED") {
		return false
	}
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "resource has been exhausted")
}

func antigravityShouldRetrySoftRateLimit(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	return decideAntigravity429(body).kind == antigravity429DecisionSoftRetry
}

func antigravityShouldBypassShortCooldown(ctx context.Context, cfg *config.Config) bool {
	return cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(cfg)
}

func antigravitySoftRateLimitDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := time.Duration(attempt+1) * 500 * time.Millisecond
	if base > 3*time.Second {
		base = 3 * time.Second
	}
	return base
}

func antigravityShortCooldownKey(auth *cliproxyauth.Auth, modelName string) string {
	if auth == nil {
		return ""
	}
	authID := strings.TrimSpace(auth.ID)
	modelName = strings.TrimSpace(modelName)
	if authID == "" || modelName == "" {
		return ""
	}
	return authID + "|" + modelName + "|sc"
}

func antigravityCreditsBalanceKey(authID string) string {
	return "cpa:antigravity:credits-balance:" + strings.TrimSpace(authID)
}

func antigravityCreditsRefreshLockKey(authID string) string {
	return "cpa:antigravity:credits-refresh-lock:" + strings.TrimSpace(authID)
}

func antigravityShortCooldownKVKey(auth *cliproxyauth.Auth, modelName string) string {
	if auth == nil {
		return ""
	}
	authID := strings.TrimSpace(auth.ID)
	modelName = strings.TrimSpace(modelName)
	if authID == "" || modelName == "" {
		return ""
	}
	return "cpa:antigravity:short-cooldown:" + authID + ":" + homekv.HashKeyPart(modelName)
}

func antigravityIsInShortCooldown(auth *cliproxyauth.Auth, modelName string, now time.Time) (bool, time.Duration) {
	inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, modelName, now)
	if errCooldown != nil {
		log.Errorf("antigravity executor: home kv cooldown read error: %v", errCooldown)
		return false, 0
	}
	return inCooldown, remaining
}

func antigravityIsInShortCooldownRequired(ctx context.Context, auth *cliproxyauth.Auth, modelName string, now time.Time) (bool, time.Duration, error) {
	kvKey := antigravityShortCooldownKVKey(auth, modelName)
	client, homeMode, errClient := currentAntigravityKVClient()
	if homeMode {
		if errClient != nil {
			return false, 0, errClient
		}
		if kvKey == "" {
			return false, 0, nil
		}
		raw, found, errGet := client.KVGet(ctx, kvKey)
		if errGet != nil || !found {
			return false, 0, errGet
		}
		untilNano, errParse := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if errParse != nil {
			return false, 0, errParse
		}
		remaining := time.Unix(0, untilNano).Sub(now)
		if remaining <= 0 {
			if _, errDel := client.KVDel(ctx, kvKey); errDel != nil {
				return false, 0, errDel
			}
			return false, 0, nil
		}
		return true, remaining, nil
	}

	key := antigravityShortCooldownKey(auth, modelName)
	if key == "" {
		return false, 0, nil
	}
	value, ok := antigravityShortCooldownByAuth.Load(key)
	if !ok {
		return false, 0, nil
	}
	until, ok := value.(time.Time)
	if !ok || until.IsZero() {
		antigravityShortCooldownByAuth.Delete(key)
		return false, 0, nil
	}
	remaining := until.Sub(now)
	if remaining <= 0 {
		antigravityShortCooldownByAuth.Delete(key)
		return false, 0, nil
	}
	return true, remaining, nil
}

func markAntigravityShortCooldown(auth *cliproxyauth.Auth, modelName string, now time.Time, duration time.Duration) {
	if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, modelName, now, duration); errMark != nil {
		log.Errorf("antigravity executor: home kv cooldown write error: %v", errMark)
	}
}

func markAntigravityShortCooldownRequired(ctx context.Context, auth *cliproxyauth.Auth, modelName string, now time.Time, duration time.Duration) error {
	kvKey := antigravityShortCooldownKVKey(auth, modelName)
	client, homeMode, errClient := currentAntigravityKVClient()
	if homeMode {
		if errClient != nil {
			return errClient
		}
		if kvKey == "" || duration <= 0 {
			return nil
		}
		until := now.Add(duration)
		written, errSet := client.KVSet(ctx, kvKey, []byte(strconv.FormatInt(until.UnixNano(), 10)), homekv.KVSetOptions{EX: duration + 5*time.Second})
		if errSet != nil {
			return errSet
		}
		if !written {
			return fmt.Errorf("home kv store unavailable")
		}
		return nil
	}

	key := antigravityShortCooldownKey(auth, modelName)
	if key == "" {
		return nil
	}
	antigravityShortCooldownByAuth.Store(key, now.Add(duration))
	return nil
}

func storeAntigravityCreditsBalanceBestEffort(authID string, bal antigravityCreditsBalance) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if client, homeMode, errClient := currentAntigravityKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("antigravity executor: home kv best-effort credits balance set failed prefix=cpa:antigravity:*: %v", errClient)
			return
		}
		raw, errMarshal := json.Marshal(bal)
		if errMarshal != nil {
			log.Errorf("antigravity executor: home kv best-effort credits balance set failed prefix=cpa:antigravity:*: %v", errMarshal)
			return
		}
		if _, errSet := client.KVSet(context.Background(), antigravityCreditsBalanceKey(authID), raw, homekv.KVSetOptions{EX: 30 * time.Minute}); errSet != nil {
			log.Errorf("antigravity executor: home kv best-effort credits balance set failed prefix=cpa:antigravity:*: %v", errSet)
		}
		return
	}
	antigravityCreditsBalanceByAuth.Store(authID, bal)
}

func homeKVUnavailableStatusErr(cause error) statusErr {
	if cause == nil {
		return statusErr{code: http.StatusServiceUnavailable, msg: "home kv store unavailable"}
	}
	return statusErr{code: http.StatusServiceUnavailable, msg: fmt.Sprintf("home kv store unavailable: %v", cause)}
}

func antigravityNoCapacityRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt+1) * 250 * time.Millisecond
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	return delay
}

func antigravityTransient429RetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt+1) * 100 * time.Millisecond
	if delay > 500*time.Millisecond {
		delay = 500 * time.Millisecond
	}
	return delay
}

func antigravityInstantRetryDelay(wait time.Duration) time.Duration {
	if wait <= 0 {
		return 0
	}
	return wait + 800*time.Millisecond
}

func antigravityWait(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
