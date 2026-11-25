package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

type antigravity429Category string

type antigravityCreditsCacheScope struct {
	authID string
	scope  string
}

type antigravityCreditsScopeState struct {
	mu                sync.Mutex
	registrationEpoch uint64
	createdAt         time.Time
	updatedAt         time.Time
	scope             string
}

type antigravityCreditsFailureState struct {
	PermanentlyDisabled      bool
	ExplicitBalanceExhausted bool
}

type antigravity429DecisionKind string

const (
	antigravityLoadCodeAssistMaxResponseBytes = 1 << 20

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
	randSource                          = rand.New(rand.NewSource(time.Now().UnixNano()))
	randSourceMutex                     sync.Mutex
	antigravityCreditsFailureByAuth     sync.Map
	antigravityShortCooldownByAuth      sync.Map
	antigravityCreditsBalanceByAuth     sync.Map // credential-scoped auth key → antigravityCreditsBalance
	antigravityCreditsCurrentScope      sync.Map
	antigravityCreditsHintRefreshByID   sync.Map // auth.ID → *antigravityCreditsHintRefreshState
	antigravityCreditsRefreshRegistries sync.Map // auth.ID → *antigravityCreditsRefreshRegistry
	antigravityRefreshGroup             singleflight.Group
	antigravityQuotaExhaustedKeywords   = []string{
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

type antigravityCreditsRefreshRegistry struct {
	mu           sync.Mutex
	currentEpoch uint64
}

type antigravityCreditsHintRefreshState struct {
	mu                sync.Mutex
	registrationEpoch uint64
	createdAt         time.Time
	updatedAt         time.Time
	scope             string
	lastAttempt       time.Time
	task              *antigravityCreditsRefreshTask
}

type antigravityCreditsRefreshTask struct {
	state     *antigravityCreditsHintRefreshState
	ctx       context.Context
	lifecycle context.Context
	cancel    context.CancelFunc
	done      chan struct{}
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
	if hint, ok, errHint := cliproxyauth.GetAntigravityCreditsHintForAuthRequired(ctx, auth); errHint != nil {
		return false, errHint
	} else if ok && hint.Known {
		return hint.Available, nil
	}

	client, homeMode, errClient := currentAntigravityKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		raw, found, errBalance := client.KVGet(ctx, antigravityCreditsBalanceKey(authID, cliproxyauth.AntigravityCreditsScope(auth)))
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
		return antigravityCreditsBalanceAvailable(auth, homeBalance), nil
	}

	key, unlock, current := lockAntigravityCreditsScope(auth)
	if !current {
		return true, nil
	}
	defer unlock()
	val, ok := antigravityCreditsBalanceByAuth.Load(key)
	if !ok {
		return true, nil // optimistic: assume credits available when balance unknown
	}
	bal, valid := val.(antigravityCreditsBalance)
	if !valid {
		antigravityCreditsBalanceByAuth.Delete(key)
		return false, nil
	}
	return antigravityCreditsBalanceAvailable(auth, bal), nil
}

func antigravityCreditsAmountsAvailable(creditAmount, minAmount float64) bool {
	if math.IsNaN(creditAmount) || math.IsInf(creditAmount, 0) || math.IsNaN(minAmount) || math.IsInf(minAmount, 0) || minAmount < 0 {
		return false
	}
	return creditAmount > 0 && creditAmount >= minAmount
}

func antigravityCreditsBalanceAvailable(auth *cliproxyauth.Auth, bal antigravityCreditsBalance) bool {
	if auth == nil || !bal.Known {
		return false
	}
	available := antigravityCreditsAmountsAvailable(bal.CreditAmount, bal.MinCreditAmount)
	cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
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
	if !ojson.ValidBytes(payload) {
		return nil
	}
	doc := ojson.NewDoc(payload)
	if !injectEnabledCreditTypesDoc(doc) {
		return nil
	}
	return doc.Bytes()
}

func injectEnabledCreditTypesDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	return doc.SetRawBytes("enabledCreditTypes", []byte(`["GOOGLE_ONE_AI"]`)) == nil
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
	return decideAntigravity429WithHeaders(body, nil)
}

func decideAntigravity429WithHeaders(body []byte, headers http.Header) antigravity429Decision {
	decision := antigravity429Decision{kind: antigravity429DecisionSoftRetry}
	if retryAfter, parseErr := helps.ParseRetryDelayWithHeaders(headers, body, time.Now()); parseErr == nil && retryAfter != nil {
		decision.retryAfter = retryAfter
	}
	if len(body) == 0 {
		return decision
	}

	status := strings.TrimSpace(ojson.GetBytesNoCopy(body, "error.status").String())
	if !strings.EqualFold(status, "RESOURCE_EXHAUSTED") {
		return decision
	}

	details := ojson.GetBytesNoCopy(body, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
				continue
			}
			reason := strings.Clone(strings.TrimSpace(detail.Get("reason").String()))
			decision.reason = reason
			switch {
			case strings.EqualFold(reason, "QUOTA_EXHAUSTED"), strings.EqualFold(reason, "INSUFFICIENT_G1_CREDITS_BALANCE"):
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

func isAntigravityAuthDisabled(auth *cliproxyauth.Auth) bool {
	return auth == nil || auth.Disabled || auth.Status == cliproxyauth.StatusDisabled
}

func clearAntigravityCreditsFailureState(auth *cliproxyauth.Auth) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	key, unlock, current := lockAntigravityCreditsScope(auth)
	if !current {
		return
	}
	defer unlock()
	_, hadFailure := antigravityCreditsFailureByAuth.LoadAndDelete(key)
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth); ok && hint.Known && !hint.Available {
		if hadFailure || !cliproxyauth.IsAntigravityCreditsHintFresh(hint, time.Now()) {
			cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
		}
	}
}
func markAntigravityCreditsPermanentlyDisabled(auth *cliproxyauth.Auth) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" || antigravityCoolingDisabled(auth, nil) {
		return
	}
	key, unlock, current := lockAntigravityCreditsScope(auth)
	if !current {
		return
	}
	state := antigravityCreditsFailureState{
		PermanentlyDisabled:      true,
		ExplicitBalanceExhausted: true,
	}
	antigravityCreditsFailureByAuth.Store(key, state)
	unlock()
	bal := antigravityCreditsBalance{
		CreditAmount:    0,
		MinCreditAmount: 1,
		Known:           true,
	}
	storeAntigravityCreditsBalanceBestEffort(auth, bal)
	cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
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
	key, unlock, current := lockAntigravityCreditsScope(auth)
	if current {
		defer unlock()
		antigravityCreditsFailureByAuth.Delete(key)
	}
}

func antigravityHasExplicitCreditsBalanceExhaustedReason(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	details := ojson.GetBytesNoCopy(body, "error.details")
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

// antigravityTOSViolation captures the parsed state of a 403 PERMISSION_DENIED
// carrying an ErrorInfo reason=TOS_VIOLATION (issue #7515). This is a
// terminal, service-scoped credential suspension — the OAuth token and model
// catalog keep working while inference is refused — so the credential must be
// deactivated rather than cooled, and sibling credentials in the same pool
// should not be hammered during an automated sweep.
type antigravityTOSViolation struct {
	AppealURL     string
	AppealURLText string
}

// parseAntigravityTOSViolation extracts the TOS_VIOLATION signature from a
// Google RPC error body. It returns nil for any other shape, including
// transient 429 quota errors and non-TOS 403s.
func parseAntigravityTOSViolation(body []byte) *antigravityTOSViolation {
	if len(body) == 0 {
		return nil
	}
	code := int(ojson.GetBytesNoCopy(body, "error.code").Int())
	status := strings.TrimSpace(ojson.GetBytesNoCopy(body, "error.status").String())
	isHTTPForbidden := code == http.StatusForbidden && strings.EqualFold(status, "PERMISSION_DENIED")
	isGRPCPermissionDenied := code == 7 && (status == "" || strings.EqualFold(status, "PERMISSION_DENIED"))
	if !isHTTPForbidden && !isGRPCPermissionDenied {
		return nil
	}
	details := ojson.GetBytesNoCopy(body, "error.details")
	if !details.Exists() || !details.IsArray() {
		return nil
	}
	for _, detail := range details.Array() {
		if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
			continue
		}
		reason := strings.TrimSpace(detail.Get("reason").String())
		if !strings.EqualFold(reason, "TOS_VIOLATION") {
			continue
		}
		meta := detail.Get("metadata")
		return &antigravityTOSViolation{
			AppealURL:     strings.Clone(strings.TrimSpace(meta.Get("appeal_url").String())),
			AppealURLText: strings.Clone(strings.TrimSpace(meta.Get("appeal_url_link_text").String())),
		}
	}
	return nil
}

func newAntigravityStatusErr(statusCode int, body []byte) statusErr {
	return newAntigravityStatusErrWithHeaders(statusCode, nil, body)
}

func newAntigravityStatusErrWithHeaders(statusCode int, headers http.Header, body []byte) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable {
		if retryAfter, parseErr := helps.ParseRetryDelayWithHeaders(headers, body, time.Now()); parseErr == nil && retryAfter != nil {
			err.retryAfter = retryAfter
		}
	} else if statusCode == http.StatusUnauthorized {
		err.credentialScoped = true
	} else if statusCode == http.StatusForbidden {
		if violation := parseAntigravityTOSViolation(body); violation != nil {
			err.tosViolation = true
			err.credentialScoped = true
			err.tosAppealURL = violation.AppealURL
			err.tosAppealURLText = violation.AppealURLText
		} else if cliproxyauth.IsAntigravityValidationRequiredError(err) {
			err.credentialScoped = true
		}
	}
	return err
}

// applyAntigravity429State performs the executor-side bookkeeping for an
// Antigravity 429 response (either HTTP status 429 or a mid-stream SSE 429
// error frame): closing idle transports, recording short model cooldowns, and
// permanently disabling credits on explicit balance exhaustion.
func (e *AntigravityExecutor) applyAntigravity429State(ctx context.Context, auth *cliproxyauth.Auth, baseModel string, useCredits bool, headers http.Header, bodyBytes []byte) error {
	return e.applyAntigravity429StateWithEvictAll(ctx, auth, baseModel, useCredits, headers, bodyBytes, false)
}

func (e *AntigravityExecutor) applyAntigravity429StateWithEvictAll(ctx context.Context, auth *cliproxyauth.Auth, baseModel string, useCredits bool, headers http.Header, bodyBytes []byte, evictAll bool) error {
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	decision := decideAntigravity429WithHeaders(bodyBytes, headers)
	switch decision.kind {
	case antigravity429DecisionShortCooldownSwitchAuth:
		closeAntigravityAuthIdleTransports(auth)
		if decision.retryAfter != nil && *decision.retryAfter > 0 && !antigravityCoolingDisabled(auth, cfg) {
			if errMarkCooldown := markAntigravityShortCooldownRequired(ctx, auth, baseModel, time.Now(), *decision.retryAfter); errMarkCooldown != nil {
				return homeKVUnavailableStatusErr(errMarkCooldown)
			}
			log.Debugf("antigravity executor: short quota cooldown (%s) for model %s recorded", *decision.retryAfter, baseModel)
		}
	case antigravity429DecisionFullQuotaExhausted:
		closeAntigravityAuthIdleTransports(auth)
		if useCredits && antigravityHasExplicitCreditsBalanceExhaustedReason(bodyBytes) && !antigravityCoolingDisabled(auth, cfg) {
			markAntigravityCreditsPermanentlyDisabled(auth)
		}
	default:
		if evictAll {
			closeAntigravityAuthIdleTransports(auth)
		}
	}
	return nil
}

// applyAntigravityTOSState performs the executor-side bookkeeping for a terminal
// TOS_VIOLATION: it arms the optional pool-wide protective pause and logs the
// appeal URL for the operator. The durable credential deactivation and the
// appeal metadata on the auth record are carried by the returned statusErr
// (tos_violation code + Error.Metadata) through the conductor, since the
// executor only sees a cloned auth. It is a no-op for non-TOS errors.
func (e *AntigravityExecutor) applyAntigravityTOSState(cfg *config.Config, auth *cliproxyauth.Auth, body []byte) {
	violation := parseAntigravityTOSViolation(body)
	if violation == nil || auth == nil {
		return
	}
	markAntigravityTOSPause(cfg, auth)
	if violation.AppealURL != "" {
		log.Warnf("antigravity executor: credential %s suspended for TOS_VIOLATION; submit appeal: %s (%s)", auth.ID, redactTOSAppealURL(violation.AppealURL), violation.AppealURLText)
	} else {
		log.Warnf("antigravity executor: credential %s suspended for TOS_VIOLATION", auth.ID)
	}
}

// applyAntigravityForbiddenState performs executor-side bookkeeping for an
// Antigravity 403 response: it evicts idle pooled transports so flagged
// keep-alive sockets are not reused, and arms the optional pool-wide pause on
// terminal TOS_VIOLATION.
func (e *AntigravityExecutor) applyAntigravityForbiddenState(cfg *config.Config, auth *cliproxyauth.Auth, body []byte) {
	closeAntigravityAuthIdleTransports(auth)
	if parseAntigravityTOSViolation(body) != nil {
		e.applyAntigravityTOSState(cfg, auth, body)
	}
}

func antigravityInBandErrorStatusCode(errorResult ojson.Result) int {
	statusCode := int(errorResult.Get("code").Int())
	if statusCode < http.StatusBadRequest || statusCode > 599 {
		return http.StatusBadGateway
	}
	return statusCode
}

// applyAntigravityUpstreamError performs shared executor-side bookkeeping for
// an Antigravity upstream error (either an HTTP non-2xx response or an HTTP 2xx
// in-band JSON error frame): 429 cooldown/credits handling, invalid-signature
// replay invalidation, 403 TOS/pool eviction, and 401 idle transport eviction.
func (e *AntigravityExecutor) applyAntigravityUpstreamError(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	upstreamModel string,
	useCredits bool,
	replayScope antigravityReasoningReplayScope,
	statusCode int,
	headers http.Header,
	body []byte,
) error {
	if statusCode == http.StatusTooManyRequests {
		if err429 := e.applyAntigravity429State(ctx, auth, upstreamModel, useCredits, headers, body); err429 != nil {
			return err429
		}
	}
	if errClear := clearAntigravityReasoningReplayOnInvalidSignature(ctx, replayScope, statusCode, body); errClear != nil {
		// Report the upstream failure rather than the cleanup failure.
		logAntigravityReasoningReplayDegraded(replayScope, "invalidate", errClear)
	}
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	switch statusCode {
	case http.StatusForbidden:
		e.applyAntigravityForbiddenState(cfg, auth, body)
	case http.StatusUnauthorized:
		closeAntigravityAuthIdleTransports(auth)
	}
	return newAntigravityStatusErrWithHeaders(statusCode, headers, body)
}
func (e *AntigravityExecutor) maybeRefreshAntigravityCreditsHint(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) {
	if e == nil || isAntigravityAuthDisabled(auth) || !antigravityCreditsRetryEnabled(e.cfg) || antigravityCoolingDisabled(auth, e.cfg) {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth); ok && cliproxyauth.IsAntigravityCreditsHintFresh(hint, time.Now()) {
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
		written, errSetNX := client.KVSetNX(context.Background(), antigravityCreditsRefreshLockKey(authID, cliproxyauth.AntigravityCreditsScope(auth)), []byte("1"), antigravityCreditsHintRefreshInterval)
		if errSetNX != nil {
			log.Errorf("antigravity executor: home kv best-effort refresh lock failed prefix=cpa:antigravity:*: %v", errSetNX)
			return
		}
		if !written {
			return
		}
		e.queueAntigravityCreditsRefresh(ctx, auth, accessToken, antigravityCreditsHintRefreshTimeout)
		return
	}

	e.queueAntigravityCreditsRefresh(ctx, auth, accessToken, antigravityCreditsHintRefreshTimeout)
}

func (e *AntigravityExecutor) queueAntigravityCreditsRefresh(ctx context.Context, auth *cliproxyauth.Auth, accessToken string, timeout time.Duration) {
	if e == nil || isAntigravityAuthDisabled(auth) || !antigravityCreditsRetryEnabled(e.cfg) || (ctx != nil && ctx.Err() != nil) {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" || strings.TrimSpace(accessToken) == "" {
		return
	}
	refreshScope := cliproxyauth.AntigravityCreditsScope(auth)
	refreshKey := authID
	if refreshScope != "" {
		refreshKey += "|" + refreshScope
	}
	registryValue, _ := antigravityCreditsRefreshRegistries.LoadOrStore(authID, &antigravityCreditsRefreshRegistry{})
	registry, okRegistry := registryValue.(*antigravityCreditsRefreshRegistry)
	if !okRegistry || registry == nil {
		antigravityCreditsRefreshRegistries.Delete(authID)
		return
	}
	registry.mu.Lock()
	if auth.RegistrationEpoch < registry.currentEpoch {
		registry.mu.Unlock()
		return
	}
	registry.currentEpoch = auth.RegistrationEpoch
	state := &antigravityCreditsHintRefreshState{
		registrationEpoch: auth.RegistrationEpoch,
		createdAt:         auth.CreatedAt,
		updatedAt:         auth.UpdatedAt,
		scope:             refreshScope,
	}
	antigravityCreditsHintRefreshByID.Range(func(key, value any) bool {
		text, okKey := key.(string)
		if !okKey || (text != authID && !strings.HasPrefix(text, authID+"|")) || text == refreshKey {
			return true
		}
		oldState, okState := value.(*antigravityCreditsHintRefreshState)
		if !okState || oldState == nil {
			return true
		}
		oldState.mu.Lock()
		if auth.RegistrationEpoch > oldState.registrationEpoch {
			if oldState.task != nil {
				oldState.task.cancel()
			}
			antigravityCreditsHintRefreshByID.CompareAndDelete(key, value)
			oldState.registrationEpoch = auth.RegistrationEpoch
		}
		oldState.mu.Unlock()
		return true
	})
	if existing, loaded := antigravityCreditsHintRefreshByID.LoadOrStore(refreshKey, state); loaded {
		if cast, ok := existing.(*antigravityCreditsHintRefreshState); ok && cast != nil {
			state = cast
		} else {
			antigravityCreditsHintRefreshByID.Delete(refreshKey)
			antigravityCreditsHintRefreshByID.Store(refreshKey, state)
		}
	}
	registry.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	state.mu.Lock()
	scope := cliproxyauth.AntigravityCreditsScope(auth)
	if ctx.Err() != nil {
		state.mu.Unlock()
		return
	}
	oldTask := state.task
	if state.scope == "" {
		state.registrationEpoch = auth.RegistrationEpoch
		state.createdAt = auth.CreatedAt
		state.updatedAt = auth.UpdatedAt
		state.scope = scope
	} else if state.scope != scope {
		state.mu.Unlock()
		return
	} else if oldTask != nil {
		if oldTask.ctx.Err() == nil {
			state.mu.Unlock()
			return
		}
		if oldTask.lifecycle.Err() != nil {
			state.lastAttempt = time.Time{}
		}
		state.task = nil
	}
	if !state.lastAttempt.IsZero() && now.Sub(state.lastAttempt) < antigravityCreditsHintRefreshInterval {
		state.mu.Unlock()
		if oldTask != nil {
			oldTask.cancel()
		}
		return
	}

	lifecycle := ctx
	var refreshCtx context.Context
	var cancelRefresh context.CancelFunc
	if timeout > 0 {
		// Keep the existing independent warm-hint budget and transport selection
		// while preserving non-cancellation context values and excluding
		// execution-scoped request proxy overrides.
		lifecycle = detachedAntigravityCredentialContext(ctx)
		refreshCtx, cancelRefresh = context.WithTimeout(lifecycle, timeout)
	} else {
		// Optional post-refresh work still follows the caller's lifecycle, but a
		// stalled upstream must not leak the goroutine or wedge state.task, so it
		// runs under the bounded warm-hint budget instead of an unbounded cancel.
		refreshCtx, cancelRefresh = context.WithTimeout(cliproxyexecutor.WithoutRequestProxyURL(lifecycle), antigravityCreditsHintRefreshTimeout)
	}
	task := &antigravityCreditsRefreshTask{
		state:     state,
		ctx:       refreshCtx,
		lifecycle: lifecycle,
		cancel:    cancelRefresh,
		done:      make(chan struct{}),
	}
	state.task = task
	state.lastAttempt = now
	state.mu.Unlock()
	if oldTask != nil {
		oldTask.cancel()
	}
	authCopy := auth.Clone()

	go func() {
		defer close(task.done)
		defer task.cancel()
		defer func() {
			state.mu.Lock()
			if state.task == task {
				state.task = nil
				if task.lifecycle.Err() != nil {
					state.lastAttempt = time.Time{}
				}
			}
			state.mu.Unlock()
		}()
		e.updateAntigravityCreditsBalanceForTask(task.ctx, authCopy, accessToken, task)
	}()
}

func (e *AntigravityExecutor) updateAntigravityCreditsBalance(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) {
	e.updateAntigravityCreditsBalanceForTask(ctx, auth, accessToken, nil)
}

func (e *AntigravityExecutor) updateAntigravityCreditsBalanceForTask(ctx context.Context, auth *cliproxyauth.Auth, accessToken string, task *antigravityCreditsRefreshTask) {
	if e == nil || isAntigravityAuthDisabled(auth) || !antigravityCreditsRetryEnabled(e.cfg) || strings.TrimSpace(auth.ID) == "" {
		return
	}
	token := strings.TrimSpace(accessToken)
	if token == "" {
		token = metaStringValue(auth.Metadata, "access_token")
	}
	if token == "" {
		return
	}

	userAgent := resolveLoadCodeAssistUserAgent(auth)
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
	reqCtx := ctx
	if reqCtx != nil {
		// Credits balance refreshes are credential-scoped background probes;
		// explicitly clearing the session ID prevents custom header expansion
		// from touching a caller's pooled *gin.Context or leaking session IDs.
		reqCtx = util.WithSessionID(reqCtx, "")
	}
	httpReq, errReq := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(loadReqBody))
	if errReq != nil {
		log.Debugf("antigravity executor: create loadCodeAssist request error: %v", errReq)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	if host := resolveHost(baseURL); host != "" {
		httpReq.Host = host
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes, http.Header{})

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "credits_query").Do(httpReq)
	if errDo != nil {
		log.Debugf("antigravity executor: loadCodeAssist request error: %v", errDo)
		return
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close loadCodeAssist response body error: %v", errClose)
		}
	}()

	authID := strings.TrimSpace(auth.ID)
	runIfCurrentTask := func(fn func()) {
		if task != nil {
			state := task.state
			state.mu.Lock()
			defer state.mu.Unlock()
			refreshKey := authID
			if state.scope != "" {
				refreshKey += "|" + state.scope
			}
			current, _ := antigravityCreditsHintRefreshByID.Load(refreshKey)
			if current != state || state.task != task || state.registrationEpoch != auth.RegistrationEpoch || state.scope != cliproxyauth.AntigravityCreditsScope(auth) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		fn()
	}

	bodyBytes, errRead := io.ReadAll(io.LimitReader(httpResp.Body, antigravityLoadCodeAssistMaxResponseBytes+1))
	if errRead != nil || httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		if errRead == nil && httpResp.StatusCode == http.StatusForbidden && len(bodyBytes) <= antigravityLoadCodeAssistMaxResponseBytes {
			runIfCurrentTask(func() {
				e.applyAntigravityTOSState(e.cfg, auth, bodyBytes)
			})
		}
		log.Debugf("antigravity executor: loadCodeAssist returned status %d, err=%v", httpResp.StatusCode, errRead)
		return
	}
	if len(bodyBytes) > antigravityLoadCodeAssistMaxResponseBytes {
		log.Debugf("antigravity executor: loadCodeAssist response exceeded %d bytes", antigravityLoadCodeAssistMaxResponseBytes)
		return
	}
	if !ojson.ValidBytes(bodyBytes) {
		log.Debugf("antigravity executor: loadCodeAssist returned invalid JSON")
		return
	}

	publish := func(balance *antigravityCreditsBalance, hint cliproxyauth.AntigravityCreditsHint) {
		runIfCurrentTask(func() {
			if balance != nil {
				storeAntigravityCreditsBalanceBestEffort(auth, *balance)
			}
			cliproxyauth.SetAntigravityCreditsHintForAuth(auth, hint)
			if balance != nil && hint.Available {
				clearAntigravityCreditsPermanentlyDisabled(auth)
			}
		})
	}
	paidTierID := strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(bodyBytes, "paidTier.id").String()))

	credits := ojson.GetBytesNoCopy(bodyBytes, "paidTier.availableCredits")
	if credits.IsArray() {
		for _, credit := range credits.Array() {
			if !strings.EqualFold(credit.Get("creditType").String(), "GOOGLE_ONE_AI") {
				continue
			}
			creditAmount, errCA := strconv.ParseFloat(strings.TrimSpace(credit.Get("creditAmount").String()), 64)
			if errCA != nil || math.IsNaN(creditAmount) || math.IsInf(creditAmount, 0) {
				continue
			}
			minField := credit.Get("minimumCreditAmountForUsage")
			rawMin := strings.TrimSpace(minField.String())
			minAmount := 0.0
			if minField.Type != ojson.Null && rawMin != "" {
				parsedMin, errMA := strconv.ParseFloat(rawMin, 64)
				if errMA != nil || math.IsNaN(parsedMin) || math.IsInf(parsedMin, 0) || parsedMin < 0 {
					continue
				}
				minAmount = parsedMin
			}
			bal := antigravityCreditsBalance{
				CreditAmount:    creditAmount,
				MinCreditAmount: minAmount,
				PaidTierID:      paidTierID,
				Known:           true,
			}
			publish(&bal, cliproxyauth.AntigravityCreditsHint{
				Known:           true,
				Available:       antigravityCreditsAmountsAvailable(creditAmount, minAmount),
				CreditAmount:    creditAmount,
				MinCreditAmount: minAmount,
				PaidTierID:      paidTierID,
				UpdatedAt:       time.Now(),
			})
			return
		}
	}
	publish(nil, cliproxyauth.AntigravityCreditsHint{
		Known:      true,
		Available:  false,
		PaidTierID: paidTierID,
		UpdatedAt:  time.Now(),
	})
}
func antigravityShouldBypassShortCooldown(ctx context.Context, cfg *config.Config) bool {
	return cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(cfg)
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
	return authID + "|" + cliproxyauth.AntigravityCreditsScope(auth) + "|" + modelName + "|sc"
}

func newAntigravityCreditsCacheKey(authID, scope string) any {
	authID = strings.TrimSpace(authID)
	if scope == "" {
		return authID
	}
	return antigravityCreditsCacheScope{authID: authID, scope: scope}
}

func lockAntigravityCreditsScope(auth *cliproxyauth.Auth) (any, func(), bool) {
	noop := func() {}
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return nil, noop, false
	}
	authID := strings.TrimSpace(auth.ID)
	value, _ := antigravityCreditsCurrentScope.LoadOrStore(authID, &antigravityCreditsScopeState{})
	state, okState := value.(*antigravityCreditsScopeState)
	if !okState || state == nil {
		antigravityCreditsCurrentScope.Delete(authID)
		return lockAntigravityCreditsScope(auth)
	}
	state.mu.Lock()
	scope := cliproxyauth.AntigravityCreditsScope(auth)
	if state.scope != "" && state.scope != scope {
		if state.registrationEpoch > auth.RegistrationEpoch || (state.registrationEpoch == auth.RegistrationEpoch && state.createdAt.After(auth.CreatedAt)) || (state.registrationEpoch == auth.RegistrationEpoch && state.createdAt.Equal(auth.CreatedAt) && state.updatedAt.After(auth.UpdatedAt)) {
			state.mu.Unlock()
			return nil, noop, false
		}
	}
	if state.scope != scope {
		state.registrationEpoch = auth.RegistrationEpoch
		state.createdAt = auth.CreatedAt
		state.updatedAt = auth.UpdatedAt
		state.scope = scope
		for _, cache := range []*sync.Map{&antigravityCreditsFailureByAuth, &antigravityCreditsBalanceByAuth} {
			cache.Range(func(key, _ any) bool {
				if scoped, ok := key.(antigravityCreditsCacheScope); ok && scoped.authID == authID && scoped.scope != scope {
					cache.Delete(key)
				}
				if legacy, ok := key.(string); ok && legacy == authID {
					cache.Delete(key)
				}
				return true
			})
		}
		antigravityShortCooldownByAuth.Range(func(key, _ any) bool {
			if text, ok := key.(string); ok && strings.HasPrefix(text, authID+"|") {
				antigravityShortCooldownByAuth.Delete(key)
			}
			return true
		})
	}
	return newAntigravityCreditsCacheKey(authID, scope), state.mu.Unlock, true
}

func antigravityCreditsBalanceKey(authID string, scope ...string) string {
	key := "cpa:antigravity:credits-balance:" + strings.TrimSpace(authID)
	if len(scope) == 0 || scope[0] == "" {
		return key
	}
	return key + ":" + strings.ReplaceAll(scope[0], ":", "-")
}

func antigravityCreditsRefreshLockKey(authID string, scope ...string) string {
	key := "cpa:antigravity:credits-refresh-lock:" + strings.TrimSpace(authID)
	if len(scope) == 0 || scope[0] == "" {
		return key
	}
	return key + ":" + strings.ReplaceAll(scope[0], ":", "-")
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
	scope := cliproxyauth.AntigravityCreditsScope(auth)
	if scope != "" {
		authID += ":" + strings.ReplaceAll(scope, ":", "-")
	}
	return "cpa:antigravity:short-cooldown:" + authID + ":" + homekv.HashKeyPart(modelName)
}

func antigravityCoolingDisabled(auth *cliproxyauth.Auth, cfg *config.Config) bool {
	return cliproxyauth.QuotaCooldownDisabledForAuthWithConfig(auth, cfg)
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
	if antigravityCoolingDisabled(auth, nil) {
		return false, 0, nil
	}
	_, unlock, current := lockAntigravityCreditsScope(auth)
	if !current {
		return false, 0, nil
	}
	defer unlock()
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
	if antigravityCoolingDisabled(auth, nil) {
		return nil
	}
	_, unlock, current := lockAntigravityCreditsScope(auth)
	if !current {
		return nil
	}
	defer unlock()
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

func storeAntigravityCreditsBalanceBestEffort(auth *cliproxyauth.Auth, bal antigravityCreditsBalance) {
	if auth == nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
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
		if _, errSet := client.KVSet(context.Background(), antigravityCreditsBalanceKey(authID, cliproxyauth.AntigravityCreditsScope(auth)), raw, homekv.KVSetOptions{EX: 30 * time.Minute}); errSet != nil {
			log.Errorf("antigravity executor: home kv best-effort credits balance set failed prefix=cpa:antigravity:*: %v", errSet)
		}
		return
	}
	key, unlock, current := lockAntigravityCreditsScope(auth)
	if current {
		defer unlock()
		antigravityCreditsBalanceByAuth.Store(key, bal)
	}
}

func homeKVUnavailableStatusErr(cause error) statusErr {
	if cause == nil {
		return statusErr{code: http.StatusServiceUnavailable, msg: "home kv store unavailable"}
	}
	return statusErr{code: http.StatusServiceUnavailable, msg: fmt.Sprintf("home kv store unavailable: %v", cause)}
}

// antigravityTOSPauseUntil stores the pool-wide protective pause deadline.
// It mirrors the credits/cooldown dual-track pattern: an in-process map for
// standalone mode and the home KV store (prefix cpa:antigravity:*) so the pause
// is shared when the control plane is active.
var antigravityTOSPauseUntil sync.Map

const antigravityTOSPauseKVKey = "cpa:antigravity:tos-pause:until"

// antigravityTOSPauseWindow resolves the configured protective pause window.
// Defaults to disabled (0) so a TOS_VIOLATION only deactivates the offending
// credential unless a positive Go duration (e.g. "6h") is explicitly configured.
func antigravityTOSPauseWindow(cfg *config.Config) (time.Duration, bool) {
	if cfg == nil {
		return 0, false
	}
	raw := strings.TrimSpace(cfg.Antigravity.TOSPauseWindow)
	if raw == "" {
		return 0, false
	}
	switch strings.ToLower(raw) {
	case "0", "off", "false", "none", "disabled":
		return 0, false
	}
	window, err := time.ParseDuration(raw)
	if err != nil || window <= 0 {
		return 0, false
	}
	return window, true
}

func antigravityTOSPauseKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	_ = auth
	return "pool"
}

// markAntigravityTOSPause records a pool-wide protective pause after the first
// TOS_VIOLATION. Only the TOS path calls this; transient 429 quota errors must
// never write or extend the pause.
func markAntigravityTOSPause(cfg *config.Config, auth *cliproxyauth.Auth) {
	window, enabled := antigravityTOSPauseWindow(cfg)
	if !enabled || window <= 0 {
		return
	}
	until := time.Now().Add(window)
	key := antigravityTOSPauseKey(auth)
	antigravityTOSPauseUntil.Store(key, until)
	if client, homeMode, errClient := currentAntigravityKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("antigravity executor: home kv tos pause write error: %v", errClient)
			return
		}
		payload := []byte(strconv.FormatInt(until.UnixNano(), 10))
		if _, errSet := client.KVSet(context.Background(), antigravityTOSPauseKVKey, payload, homekv.KVSetOptions{EX: window + 5*time.Second}); errSet != nil {
			log.Errorf("antigravity executor: home kv tos pause write error: %v", errSet)
		}
	}
	log.Warnf("antigravity executor: TOS_VIOLATION pause armed for %s (pool-wide until %s)", window, until.Format(time.RFC3339))
}

// newAntigravityTOSPauseStatusErr builds the 503 error returned while the
// Antigravity pool is under a protective TOS_VIOLATION pause. Marking it
// request-scoped ensures the conductor aborts immediately without failing,
// cooling, or rotating across healthy sibling credentials.
func newAntigravityTOSPauseStatusErr() statusErr {
	return statusErr{
		code:          http.StatusServiceUnavailable,
		msg:           "antigravity pool paused after TOS_VIOLATION",
		requestScoped: true,
	}
}

// antigravityInTOSPause reports whether the Antigravity pool is currently under
// a protective TOS pause. The check is provider-scoped (any Antigravity
// credential) and ignores per-credential identity.
func antigravityInTOSPause(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	_, enabled := antigravityTOSPauseWindow(cfg)
	if !enabled {
		return false
	}
	key := antigravityTOSPauseKey(auth)
	if key == "" {
		return false
	}
	now := time.Now()
	if value, ok := antigravityTOSPauseUntil.Load(key); ok {
		if until, ok := value.(time.Time); ok && until.After(now) {
			return true
		}
		// Delete only if it is still the same expired value; a concurrent
		// markAntigravityTOSPause may have already installed a newer deadline.
		antigravityTOSPauseUntil.CompareAndDelete(key, value)
	}
	if client, homeMode, errClient := currentAntigravityKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("antigravity executor: home kv tos pause read error: %v", errClient)
			return false
		}
		raw, found, errGet := client.KVGet(context.Background(), antigravityTOSPauseKVKey)
		if errGet != nil || !found {
			return false
		}
		untilNano, errParse := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if errParse != nil {
			return false
		}
		remaining := time.Unix(0, untilNano).Sub(now)
		// An expired deadline is treated as absent; the KV EX ttl reclaims the
		// key, so no explicit delete is needed (which could race a concurrent
		// markAntigravityTOSPause writing a newer deadline).
		return remaining > 0
	}
	return false
}

// redactTOSAppealURL strips query parameters and fragments from an appeal URL
// before logging, so a signed or tracking URL cannot leak through log output.
// The full URL remains available on the auth record for operator use.
func redactTOSAppealURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}
