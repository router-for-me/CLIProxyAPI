// Package codexreset redeems explicitly opted-in, expiring Codex reset credits.
package codexreset

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const baseURL = "https://chatgpt.com/backend-api/wham/"
const interval = 5 * time.Minute

type statusError struct {
	code    int
	retryAt int64
}

func (e *statusError) Error() string { return fmt.Sprintf("reset request returned HTTP %d", e.code) }

// State contains no credentials. The persistent KV store owns policy and journal.
type State struct {
	AccountKey          string `json:"account_key"`
	Enabled             bool   `json:"enabled"`
	BeforeExpirySeconds int    `json:"before_expiry_seconds"`
	NextCheckAt         int64  `json:"next_check_at"`
	LastCheckedAt       int64  `json:"last_checked_at"`
	LastResetAt         int64  `json:"last_reset_at"`
	Result              string `json:"result"`
	CreditID            string `json:"credit_id,omitempty"`
	RedeemedCreditID    string `json:"redeemed_credit_id,omitempty"`
	RequestID           string `json:"request_id,omitempty"`
	Pending             bool   `json:"pending"`
	Failures            int    `json:"failures"`
}

// AccountKey binds consent to both the immutable user and workspace, not a filename.
func AccountKey(auth *coreauth.Auth) string {
	if auth == nil || auth.Provider != "codex" {
		return ""
	}
	for name := range auth.Attributes {
		if strings.HasPrefix(name, "header:") {
			header := strings.TrimSpace(strings.TrimPrefix(name, "header:"))
			if strings.EqualFold(header, "Authorization") || strings.EqualFold(header, "ChatGPT-Account-Id") || strings.EqualFold(header, "Host") {
				return ""
			}
		}
	}
	idToken, _ := auth.Metadata["id_token"].(string)
	claims, errParse := codexauth.ParseJWTToken(idToken)
	if errParse != nil || strings.TrimSpace(claims.Sub) == "" || strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID) == "" {
		return ""
	}
	accountID, _ := auth.Metadata["account_id"].(string)
	if accountID != claims.CodexAuthInfo.ChatgptAccountID {
		return ""
	}
	return fmt.Sprintf("codex-reset:%x", sha256.Sum256([]byte(claims.Sub+"\x00"+accountID)))
}

func Read(ctx context.Context, store kvstore.KVClient, key string) (State, []byte, bool, error) {
	state := State{AccountKey: key, BeforeExpirySeconds: 3600, Result: "disabled"}
	raw, exists, errRead := store.KVGet(ctx, key)
	if errRead == nil && exists {
		value := ojson.GetBytes(raw, "state")
		state = State{
			AccountKey: value.Get("account_key").String(), Enabled: value.Get("enabled").Bool(),
			BeforeExpirySeconds: int(value.Get("before_expiry_seconds").Int()),
			LastResetAt:         value.Get("last_reset_at").Int(),
			NextCheckAt:         value.Get("next_check_at").Int(), LastCheckedAt: value.Get("last_checked_at").Int(),
			Result: value.Get("result").String(), CreditID: value.Get("credit_id").String(),
			RedeemedCreditID: value.Get("redeemed_credit_id").String(),
			RequestID:        value.Get("request_id").String(), Pending: value.Get("pending").Bool(),
			Failures: int(value.Get("failures").Int()),
		}
		if !ojson.ValidBytes(raw) || state.AccountKey != key {
			errRead = fmt.Errorf("reset policy identity mismatch")
		}
	}
	return state, raw, exists, errRead
}

func Save(ctx context.Context, store kvstore.KVClient, state State, previous []byte, exists bool) (bool, error) {
	doc := ojson.NewDocString("{}")
	if errSet := doc.Set("state", state); errSet != nil {
		return false, errSet
	}
	return store.KVCompareAndSwap(ctx, state.AccountKey, previous, exists, doc.Bytes(), 0)
}

type Manager interface {
	List() []*coreauth.Auth
	GetByID(string) (*coreauth.Auth, bool)
	HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error)
	ResetQuota(context.Context, string) (*coreauth.Auth, []string, error)
}

type Worker struct {
	Manager Manager
	Store   kvstore.KVClient
	Now     func() time.Time
}

// Run is tied to the service lifecycle and never falls back to volatile storage.
func Run(ctx context.Context, manager Manager, standalone func() bool) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if standalone() {
			if store, enabled, errStore := kvstore.CurrentKVClient(); errStore == nil && enabled && store != nil {
				worker := Worker{Manager: manager, Store: store, Now: time.Now}
				worker.Check(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) Check(ctx context.Context) {
	for _, auth := range w.Manager.List() {
		if ctx.Err() != nil {
			return
		}
		key := AccountKey(auth)
		if key == "" || auth.Disabled {
			continue
		}
		state, raw, exists, errRead := Read(ctx, w.Store, key)
		now := w.Now()
		if errRead != nil || !exists || (!state.Enabled && state.Result != "cooldown_pending") || state.Pending || now.Unix() < state.NextCheckAt || state.BeforeExpirySeconds < 60 || state.BeforeExpirySeconds > 86400 {
			continue
		}
		// Claim the probe too: aliases and multiple workers must not poll/spend together.
		state.LastCheckedAt, state.NextCheckAt = now.Unix(), now.Add(interval).Unix()
		claimed, errClaim := Save(ctx, w.Store, state, raw, exists)
		if errClaim != nil || !claimed {
			continue
		}
		if state.Result == "cooldown_pending" {
			w.clearCooldown(ctx, &state)
			continue
		}
		w.checkAccount(ctx, state)
	}
}

func (w *Worker) checkAccount(ctx context.Context, state State) {
	var auth *coreauth.Auth
	var usage, credits ojson.Result
	var errRead error
	for _, candidate := range w.Manager.List() {
		if ctx.Err() != nil {
			return
		}
		current, ok := w.Manager.GetByID(candidate.ID)
		if !ok || current.Disabled || AccountKey(current) != state.AccountKey {
			continue
		}
		usage, errRead = w.request(ctx, current, "usage", "")
		if errRead == nil {
			credits, errRead = w.request(ctx, current, "rate-limit-reset-credits", "")
		}
		if errRead == nil {
			auth = current
			break
		}
		// Retry only read-only authentication failures; never bypass account-wide limits.
		upstream, ok := errRead.(*statusError)
		if !ok || upstream.code != http.StatusUnauthorized {
			break
		}
		state.NextCheckAt = max(state.NextCheckAt, upstream.retryAt)
	}
	if errRead != nil {
		w.readFailed(ctx, state, errRead)
		return
	}
	if auth == nil {
		w.finish(ctx, state, "identity_changed")
		return
	}
	now := w.Now()
	creditID := selectCredit(usage, credits, now, time.Duration(state.BeforeExpirySeconds)*time.Second)
	age := now.Unix() - state.LastCheckedAt
	if creditID == "" || creditID == state.RedeemedCreditID || age < 0 || age > 60 {
		w.finish(ctx, state, "not_eligible")
		return
	}
	current, ok := w.Manager.GetByID(auth.ID)
	if !ok || current.Disabled || AccountKey(current) != state.AccountKey {
		w.finish(ctx, state, "identity_changed")
		return
	}
	latest, raw, exists, errRead := Read(ctx, w.Store, state.AccountKey)
	if errRead != nil || !exists || !latest.Enabled || latest.Pending || latest.BeforeExpirySeconds != state.BeforeExpirySeconds || latest.LastCheckedAt != state.LastCheckedAt {
		return
	}
	latest.Pending, latest.Result, latest.CreditID, latest.RequestID = true, "unknown", creditID, uuid.NewString()
	claimed, errClaim := Save(ctx, w.Store, latest, raw, true)
	if errClaim != nil || !claimed {
		return
	}
	// Persist before POST. A crash or ambiguous response blocks automation, never retries spending.
	body := ojson.NewDocString("{}")
	if errSet := body.Set("redeem_request_id", latest.RequestID); errSet != nil {
		return
	}
	if errSet := body.Set("credit_id", creditID); errSet != nil {
		return
	}
	answer, errConsume := w.request(ctx, current, "rate-limit-reset-credits/consume", body.String())
	if errConsume != nil {
		return
	}
	switch answer.Get("code").String() {
	case "reset", "already_redeemed":
		if w.finish(ctx, latest, "cooldown_pending") {
			w.clearCooldown(ctx, &latest)
		}
	case "no_credit", "nothing_to_reset":
		w.finish(ctx, latest, answer.Get("code").String())
	}
}

func (w *Worker) clearCooldown(ctx context.Context, state *State) {
	matched, clearedAll := false, true
	// The journal is account-wide, but routing cooldowns belong to each credential.
	for _, auth := range w.Manager.List() {
		if AccountKey(auth) != state.AccountKey {
			continue
		}
		current, exists := w.Manager.GetByID(auth.ID)
		if !exists || AccountKey(current) != state.AccountKey {
			continue
		}
		matched = true
		if _, _, errReset := w.Manager.ResetQuota(ctx, auth.ID); errReset != nil {
			clearedAll = false
		}
	}
	if matched && clearedAll {
		w.finish(ctx, *state, "reset")
	}
}

func (w *Worker) readFailed(ctx context.Context, state State, errRead error) {
	if upstream, ok := errRead.(*statusError); ok {
		state.NextCheckAt = max(state.NextCheckAt, upstream.retryAt)
	}
	w.finish(ctx, state, "read_failed")
}

func (w *Worker) finish(ctx context.Context, operation State, result string) bool {
	for attempt := 0; attempt < 3; attempt++ {
		state, raw, exists, errRead := Read(ctx, w.Store, operation.AccountKey)
		if errRead != nil || !exists || state.RequestID != operation.RequestID {
			return false
		}
		state.Result, state.Pending = result, false
		if result == "cooldown_pending" {
			state.RedeemedCreditID = operation.CreditID
			state.LastResetAt = w.Now().Unix()
		}
		state.Failures = 0
		if result == "read_failed" {
			state.Failures = min(max(operation.Failures, 0)+1, 4)
			delay := interval * time.Duration(1<<min(state.Failures, 4))
			state.NextCheckAt = max(operation.NextCheckAt, w.Now().Add(min(delay, time.Hour)).Unix())
		}
		if saved, errSave := Save(ctx, w.Store, state, raw, true); errSave != nil {
			return false
		} else if saved {
			return true
		}
	}
	return false
}

func (w *Worker) request(ctx context.Context, auth *coreauth.Auth, path, body string) (ojson.Result, error) {
	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}
	req, errRequest := http.NewRequestWithContext(ctx, method, baseURL+path, strings.NewReader(body))
	if errRequest != nil {
		return ojson.Result{}, errRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "codex-1")
	req.Header.Set("Cache-Control", "no-cache")
	accountID, _ := auth.Metadata["account_id"].(string)
	req.Header.Set("ChatGPT-Account-Id", accountID)
	// Quota/reset operations always use the original ChatGPT grant, never the SIWC partition.
	ctx = coreauth.WithCodexClientRequest(ctx, true)
	response, errHTTP := w.Manager.HttpRequest(ctx, auth, req)
	if errHTTP != nil {
		return ojson.Result{}, fmt.Errorf("reset request failed")
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			// Transport error text may contain credentials; keep the diagnostic fixed.
			log.Error("codex reset: failed to close upstream response body")
		}
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retry := int64(0)
		value := response.Header.Get("Retry-After")
		if seconds, errParse := strconv.ParseInt(value, 10, 32); errParse == nil && seconds >= 0 {
			retry = w.Now().Add(time.Duration(seconds) * time.Second).Unix()
		} else if at, errDate := http.ParseTime(value); errDate == nil {
			retry = at.Unix()
		}
		return ojson.Result{}, &statusError{code: response.StatusCode, retryAt: retry}
	}
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if errRead != nil || len(raw) > 1024*1024 || !ojson.ValidBytes(raw) {
		return ojson.Result{}, fmt.Errorf("invalid reset response")
	}
	return ojson.ParseBytes(raw), nil
}

func selectCredit(usage, credits ojson.Result, now time.Time, lead time.Duration) string {
	available := credits.Get("available_count")
	if available.Type != ojson.Number || available.Int() <= 0 || available.Float() != float64(available.Int()) {
		return ""
	}
	// Official payloads expose availability; newer optional applicability must never be ignored.
	for _, count := range []ojson.Result{usage.Get("rate_limit_reset_credits.available_count"), usage.Get("rate_limit_reset_credits.applicable_available_count"), credits.Get("applicable_available_count")} {
		if count.Exists() && (count.Type != ojson.Number || count.Int() <= 0 || count.Float() != float64(count.Int())) {
			return ""
		}
	}
	eligible := false
	for _, name := range []string{"primary_window", "secondary_window"} {
		window := usage.Get("rate_limit." + name)
		used := window.Get("used_percent")
		reset := window.Get("reset_at")
		duration := window.Get("limit_window_seconds")
		covered := duration.Type == ojson.Number && (duration.Float() == 18000 || duration.Float() == 604800)
		if covered && used.Type == ojson.Number && used.Float() > 20 && used.Float() <= 100 && reset.Type == ojson.Number && reset.Int() > now.Unix() {
			eligible = true
		}
	}
	if !eligible {
		return ""
	}
	var selected string
	var earliest time.Time
	for _, credit := range credits.Get("credits").Array() {
		expires, errParse := time.Parse(time.RFC3339Nano, credit.Get("expires_at").String())
		id := strings.TrimSpace(credit.Get("id").String())
		if errParse != nil || credit.Get("id").Type != ojson.String || id == "" || credit.Get("reset_type").String() != "codex_rate_limits" || credit.Get("status").String() != "available" || !expires.After(now) || expires.After(now.Add(lead)) {
			continue
		}
		if selected == "" || expires.Before(earliest) {
			selected, earliest = id, expires
		}
	}
	return selected
}
