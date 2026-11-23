package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/resetcredits"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type desktopUsageResponse struct {
	Strategy    string `json:"strategy"`
	Credentials []struct {
		ID            string     `json:"id"`
		AuthIndex     string     `json:"auth_index"`
		Name          string     `json:"name"`
		Provider      string     `json:"provider"`
		Note          string     `json:"note"`
		Disabled      bool       `json:"disabled"`
		CanRefresh    *bool      `json:"can_refresh"`
		NextPick      bool       `json:"next_pick"`
		ObservedAt    *time.Time `json:"observed_at"`
		CooldownUntil *time.Time `json:"cooldown_until"`
		Weight        int64      `json:"weight"`
		APIKey        *struct {
			Masked  string `json:"masked"`
			Success int64  `json:"success"`
			Failed  int64  `json:"failed"`
		} `json:"api_key"`
		Plan *struct {
			Label      string   `json:"label"`
			Multiplier *float64 `json:"multiplier"`
		} `json:"plan"`
		Windows []struct {
			Key         string     `json:"key"`
			Label       string     `json:"label"`
			UsedPercent *float64   `json:"used_percent"`
			ResetsAt    *time.Time `json:"resets_at"`
			Status      *string    `json:"status"`
			Duration    *int64     `json:"duration_seconds"`
			Scoped      bool       `json:"scoped"`
			Cadence     *string    `json:"reset_cadence"`
		} `json:"windows"`
	} `json:"credentials"`
	Errors map[string]string `json:"errors"`
}

func claudeUsageSignals(weekly, fiveHour time.Time) map[string]string {
	return map[string]string{
		"anthropic-ratelimit-unified-7d-utilization": "0.4",
		"anthropic-ratelimit-unified-7d-reset":       strconv.FormatInt(weekly.Unix(), 10),
		"anthropic-ratelimit-unified-7d-status":      "allowed",
		"anthropic-ratelimit-unified-5h-utilization": "0.1",
		"anthropic-ratelimit-unified-5h-reset":       strconv.FormatInt(fiveHour.Unix(), 10),
	}
}

func newDesktopUsageHandler(t *testing.T, strategy string) *Handler {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now()
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.Routing.Strategy = strategy
	manager.SetConfig(cfg)
	for _, auth := range []*coreauth.Auth{
		{ID: "late", Index: "idx-late", Provider: "claude", Attributes: map[string]string{"runtime_only": "true", "note": "Work", "weight": "3"},
			Metadata: map[string]any{"rate_limit_tier": "default_claude_max_5x", "organization_type": "claude_max"},
			Quota:    coreauth.QuotaState{ObservedAt: now, Signals: claudeUsageSignals(now.Add(72*time.Hour), now.Add(3*time.Hour))}},
		{ID: "soon", Index: "idx-soon", Provider: "claude", Attributes: map[string]string{"runtime_only": "true"},
			Quota: coreauth.QuotaState{ObservedAt: now, Signals: claudeUsageSignals(now.Add(24*time.Hour), now.Add(2*time.Hour))}},
		{ID: "codex-a", Index: "idx-codex", Provider: "codex", Attributes: map[string]string{"runtime_only": "true"}},
	} {
		registerAuthForLookupTest(t, manager, auth)
	}
	return NewHandlerWithoutConfigFilePath(cfg, manager)
}

func serveDesktop(t *testing.T, handler gin.HandlerFunc, method, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v8/management/usage", strings.NewReader(body))
	ctx.Request.RemoteAddr = remoteAddr
	handler(ctx)
	return rec
}

func decodeDesktopUsage(t *testing.T, rec *httptest.ResponseRecorder) desktopUsageResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload desktopUsageResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	return payload
}

func TestGetUsageWindowsShapeAndNextPick(t *testing.T) {
	h := newDesktopUsageHandler(t, "sr")
	payload := decodeDesktopUsage(t, serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", "127.0.0.1:1"))
	if payload.Strategy != coreauth.SoonestResetStrategy {
		t.Fatalf("strategy = %q", payload.Strategy)
	}
	if len(payload.Credentials) != 3 {
		t.Fatalf("credentials = %+v", payload.Credentials)
	}
	picks := map[string]bool{}
	for _, credential := range payload.Credentials {
		picks[credential.ID] = credential.NextPick
		if credential.Name != credential.ID || credential.CooldownUntil != nil {
			t.Fatalf("unexpected identity/cooldown: %+v", credential)
		}
		switch credential.ID {
		case "late":
			if credential.Plan == nil || credential.Plan.Label != "Max 5x" || credential.Plan.Multiplier == nil || *credential.Plan.Multiplier != 5 {
				t.Fatalf("plan = %+v", credential.Plan)
			}
			if credential.Note != "Work" || credential.Weight != 3 {
				t.Fatalf("note/weight = %q/%d", credential.Note, credential.Weight)
			}
		case "soon":
			if len(credential.Windows) != 2 || credential.ObservedAt == nil {
				t.Fatalf("windows = %+v", credential.Windows)
			}
			fiveHour, weekly := credential.Windows[0], credential.Windows[1]
			if fiveHour.Key != "five_hour" || weekly.Key != "seven_day" || weekly.Label != "Weekly" {
				t.Fatalf("window order/keys = %+v", credential.Windows)
			}
			if weekly.UsedPercent == nil || *weekly.UsedPercent != 40 || weekly.ResetsAt == nil || weekly.Status == nil || *weekly.Status != "allowed" {
				t.Fatalf("weekly = %+v", weekly)
			}
			if fiveHour.Duration == nil || *fiveHour.Duration != 5*3600 || weekly.Duration == nil || *weekly.Duration != 7*24*3600 || fiveHour.Scoped || weekly.Scoped {
				t.Fatalf("duration/scope = %+v", credential.Windows)
			}
			if credential.Weight != 1 || credential.Plan != nil {
				t.Fatalf("default weight = %d", credential.Weight)
			}
			if fiveHour.Cadence == nil || *fiveHour.Cadence != "on_use" || weekly.Cadence == nil || *weekly.Cadence != "fixed" {
				t.Fatalf("cadence = %+v", credential.Windows)
			}
			if fiveHour.Status != nil {
				t.Fatalf("missing status should be null: %+v", fiveHour)
			}
		case "codex-a":
			if credential.ObservedAt != nil || len(credential.Windows) != 0 {
				t.Fatalf("codex = %+v", credential)
			}
		}
	}
	if !picks["soon"] || picks["late"] {
		t.Fatalf("next_pick = %v", picks)
	}
	if !picks["codex-a"] {
		t.Fatalf("sole available codex credential should be next pick: %v", picks)
	}
}

func TestGetUsageWindowsRotatingStrategyHasNoNextPick(t *testing.T) {
	h := newDesktopUsageHandler(t, "round-robin")
	payload := decodeDesktopUsage(t, serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", "127.0.0.1:1"))
	for _, credential := range payload.Credentials {
		if credential.NextPick {
			t.Fatalf("round-robin has no deterministic pick: %+v", credential)
		}
	}
}

func TestRefreshUsageWindowsValidation(t *testing.T) {
	h := newDesktopUsageHandler(t, "soonest-reset")
	if rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"missing"}`, "127.0.0.1:1"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown index status = %d", rec.Code)
	}
	if rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"idx-codex"}`, "127.0.0.1:1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("Codex without a login status = %d", rec.Code)
	}
	if rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{`, "127.0.0.1:1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", rec.Code)
	}
	// No credential carries an access token, so refresh-all makes no lookups.
	payload := decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, "", "127.0.0.1:1"))
	if len(payload.Credentials) != 3 || len(payload.Errors) != 0 {
		t.Fatalf("refresh-all payload = %+v", payload)
	}
}

type desktopTestUsageLookup struct {
	singleID string
	failures map[string]error
}

func (lookup *desktopTestUsageLookup) Eligible(auth *coreauth.Auth) bool {
	return auth.Provider == "codex"
}

func (lookup *desktopTestUsageLookup) Refresh(_ context.Context, auth *coreauth.Auth) error {
	lookup.singleID = auth.ID
	return lookup.failures[auth.ID]
}

func (lookup *desktopTestUsageLookup) RefreshAll(context.Context) map[string]error {
	return lookup.failures
}

func TestDesktopUsageRegisteredLookups(t *testing.T) {
	h := newDesktopUsageHandler(t, "soonest-reset")
	registerAuthForLookupTest(t, h.authManager, &coreauth.Auth{
		ID: "codex-key", Index: "idx-codex-key", Provider: "codex", Attributes: map[string]string{"api_key": "test-value"},
	})
	first := &desktopTestUsageLookup{failures: map[string]error{"codex-a": errors.New("first lookup failed")}}
	second := &desktopTestUsageLookup{failures: map[string]error{"another-credential": errors.New("second lookup failed")}}
	h.RegisterUsageLookup(first, h.authManager)
	h.RegisterUsageLookup(second, h.authManager)

	payload := decodeDesktopUsage(t, serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", "127.0.0.1:1"))
	if len(payload.Credentials) != 4 {
		t.Fatalf("credentials = %+v", payload.Credentials)
	}
	for _, credential := range payload.Credentials {
		if want := credential.ID == "codex-a"; credential.CanRefresh == nil || *credential.CanRefresh != want {
			t.Errorf("%s can_refresh = %v, want %t", credential.ID, credential.CanRefresh, want)
		}
	}

	rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"idx-codex-key"}`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest || first.singleID != "" || second.singleID != "" {
		t.Fatalf("API key refresh status = %d, dispatched to %q, %q", rec.Code, first.singleID, second.singleID)
	}

	payload = decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"idx-codex"}`, "127.0.0.1:1"))
	if first.singleID != "codex-a" || second.singleID != "" || payload.Errors["codex-a"] != "first lookup failed" {
		t.Fatalf("single refresh = %q, %q, errors %v", first.singleID, second.singleID, payload.Errors)
	}

	payload = decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, "", "127.0.0.1:1"))
	if len(payload.Errors) != 2 || payload.Errors["codex-a"] != "first lookup failed" || payload.Errors["another-credential"] != "second lookup failed" {
		t.Fatalf("refresh-all errors = %v", payload.Errors)
	}
}

func TestDesktopUsageLookupsFollowManagerAtDispatch(t *testing.T) {
	h := newDesktopUsageHandler(t, "")
	original := h.authManager
	replacement := coreauth.NewManager(nil, nil, nil)
	auth, _ := original.GetByID("codex-a")
	registerAuthForLookupTest(t, replacement, auth)

	lookup := &desktopTestUsageLookup{failures: map[string]error{auth.ID: errors.New("registered lookup called")}}
	h.RegisterUsageLookup(lookup, original)
	before, beforeLookups := h.usageLookupsSnapshot()
	h.SetAuthManager(replacement)
	after, afterLookups := h.usageLookupsSnapshot()

	for _, snapshot := range []struct {
		got, want *coreauth.Manager
		lookups   []UsageLookup
		count     int
	}{
		{before, original, beforeLookups, 3},
		{after, replacement, afterLookups, 2},
	} {
		if snapshot.got != snapshot.want || len(snapshot.lookups) != snapshot.count {
			t.Fatalf("wrong manager or lookup count in snapshot: %+v", snapshot)
		}
		claude, claudeOK := snapshot.lookups[0].(claudeUsageLookup)
		codex, codexOK := snapshot.lookups[1].(codexUsageLookup)
		if !claudeOK || !codexOK || claude.manager != snapshot.want || codex.manager != snapshot.want {
			t.Fatalf("built-ins lost their snapshot manager or order: %v", snapshot.lookups)
		}
	}

	const remoteAddr = "127.0.0.1:1"
	const refreshBody = `{"auth_index":"idx-codex"}`
	get := func() desktopUsageResponse {
		return decodeDesktopUsage(t, serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", remoteAddr))
	}
	checkCanRefresh := func(want bool) {
		t.Helper()
		for _, credential := range get().Credentials {
			if credential.ID == auth.ID && credential.CanRefresh != nil && *credential.CanRefresh == want {
				return
			}
		}
		t.Fatalf("%s can_refresh did not match %t", auth.ID, want)
	}
	checkCanRefresh(false)
	rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, refreshBody, remoteAddr)
	if rec.Code != http.StatusBadRequest || lookup.singleID != "" {
		t.Fatalf("mismatched refresh: status=%d, dispatched to %q", rec.Code, lookup.singleID)
	}
	payload := decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, "", remoteAddr))
	if len(payload.Errors) != 0 {
		t.Fatalf("mismatched bulk refresh dispatched: %v", payload.Errors)
	}

	h.SetAuthManager(original)
	checkCanRefresh(true)
	payload = decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, refreshBody, remoteAddr))
	if lookup.singleID != auth.ID || payload.Errors[auth.ID] != lookup.failures[auth.ID].Error() {
		t.Fatalf("restored single refresh: dispatched to %q, errors=%v", lookup.singleID, payload.Errors)
	}
	payload = decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, "", remoteAddr))
	if payload.Errors[auth.ID] != lookup.failures[auth.ID].Error() {
		t.Fatalf("restored bulk refresh errors = %v", payload.Errors)
	}
}

func TestShutdownServerLoopbackOnly(t *testing.T) {
	calls := 0
	original := requestServerShutdown
	requestServerShutdown = func() { calls++ }
	t.Cleanup(func() { requestServerShutdown = original })
	h := newDesktopUsageHandler(t, "")

	if rec := serveDesktop(t, h.ShutdownServer, http.MethodPost, "", "203.0.113.7:4000"); rec.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("remote: status=%d calls=%d", rec.Code, calls)
	}
	for _, addr := range []string{"127.0.0.1:4000", "[::1]:4000"} {
		if rec := serveDesktop(t, h.ShutdownServer, http.MethodPost, "", addr); rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status=%d", addr, rec.Code)
		}
	}
	if calls != 2 {
		t.Fatalf("shutdown calls = %d", calls)
	}
}

func TestDesktopPlanReportsCodexPlanWithoutMultiple(t *testing.T) {
	plan := desktopPlan(&coreauth.Auth{Provider: "codex", Attributes: map[string]string{"plan_type": "plus"}})
	if plan == nil || plan["label"] != "Plus" || plan["tier"] != "plus" || plan["multiplier"] != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if desktopPlan(&coreauth.Auth{Provider: "codex"}) != nil {
		t.Fatal("plan without a plan type")
	}
}

func TestDesktopResetCreditsReportsStoredLookup(t *testing.T) {
	auth := &coreauth.Auth{Provider: "codex", Metadata: map[string]any{
		"reset_credits_available": float64(1), "reset_credits_applicable": float64(0), "reset_credits_checked_at": "2026-10-26T12:00:00Z",
		"reset_credits_expiries": []any{"2026-11-15T08:30:00Z"},
	}}
	got := desktopResetCredits(auth)
	expiries, _ := got["expiries"].([]time.Time)
	if got == nil || got["available"] != 1 || got["applicable"] != 0 || len(expiries) != 1 || !expiries[0].Equal(time.Date(2026, 11, 15, 8, 30, 0, 0, time.UTC)) {
		t.Fatalf("reset credits = %+v", got)
	}
	if desktopResetCredits(&coreauth.Auth{Provider: "codex", Metadata: map[string]any{}}) != nil {
		t.Fatal("reset credits without a lookup")
	}
}

func TestUseResetCreditValidation(t *testing.T) {
	h := newDesktopUsageHandler(t, "soonest-reset")
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"auth_index":"idx-soon","grant_id":null}`, http.StatusBadRequest},
		{`{"auth_index":"idx-soon","grant_id":" "}`, http.StatusBadRequest},
		{`{"auth_index":"idx-soon","grant_id":3}`, http.StatusBadRequest},
		{`{"auth_index":"missing"}`, http.StatusNotFound},
		{`{"auth_index":"idx-soon"}`, http.StatusBadRequest},
		// A Codex credential without an access token is not an eligible login.
		{`{"auth_index":"idx-codex"}`, http.StatusBadRequest},
	} {
		if rec := serveDesktop(t, h.UseResetCredit, http.MethodPost, tc.body, "127.0.0.1:1"); rec.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}

func TestDesktopResetCreditsIncludesGrantScopeAndNullableExpiry(t *testing.T) {
	auth := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{}}
	resetcredits.Store(auth.Metadata, time.Now(), &resetcredits.Credits{Available: 1, Applicable: 1, Grants: []resetcredits.Grant{
		{ID: "fable", Label: "Fable reset", Scope: "fable", Available: 1, Applicable: 1, Clears: []string{"five_hour", "seven_day_overage_included"}},
	}})
	doc := ojson.NewDocString(`{}`)
	if errSet := doc.Set("credits", desktopResetCredits(auth)); errSet != nil {
		t.Fatal(errSet)
	}
	grant := doc.Get("credits.grants.0")
	if grant.Get("id").String() != "fable" || grant.Get("scope").String() != "fable" || grant.Get("expires_at").Type != ojson.Null ||
		grant.Get("applicable").Int() != 1 || len(grant.Get("clears").Array()) != 2 {
		t.Fatalf("payload = %s", doc.String())
	}
}

type failingRefreshExecutor struct {
	refreshRecordExecutor
	err error
}

func (e *failingRefreshExecutor) Refresh(_ context.Context, _ *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshCnt.Add(1)
	return nil, e.err
}

type tokenCapturingUsageLookup struct {
	provider   string
	manager    *coreauth.Manager
	seenTokens map[string]string
	allErr     error
}

func (l *tokenCapturingUsageLookup) Eligible(auth *coreauth.Auth) bool {
	return auth != nil && !auth.Disabled && auth.Provider == l.provider
}

func (l *tokenCapturingUsageLookup) Refresh(_ context.Context, auth *coreauth.Auth) error {
	tok, _ := auth.Metadata["access_token"].(string)
	l.seenTokens[auth.ID] = tok
	return nil
}

func (l *tokenCapturingUsageLookup) RefreshAll(ctx context.Context) map[string]error {
	failures := map[string]error{}
	for _, auth := range l.manager.List() {
		if !l.Eligible(auth) {
			continue
		}
		if l.allErr != nil {
			failures[auth.ID] = l.allErr
			continue
		}
		_ = l.Refresh(ctx, auth)
	}
	return failures
}

func TestRefreshUsageWindowsRenewsExpiredToken(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshRecordExecutor{provider: "custom-oauth"}
	manager.RegisterExecutor(exec)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	expired := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "cred-single", Index: "idx-single", Provider: "custom-oauth",
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"access_token": "stale-1", "refresh_token": "rt-1", "expired": expired},
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "cred-all", Index: "idx-all", Provider: "custom-oauth",
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"access_token": "stale-2", "refresh_token": "rt-2", "expired": expired},
	})
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	lookup := &tokenCapturingUsageLookup{provider: "custom-oauth", manager: manager, seenTokens: map[string]string{}}
	h.RegisterUsageLookup(lookup, manager)

	payload := decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"idx-single"}`, "127.0.0.1:1"))
	if len(payload.Errors) != 0 || lookup.seenTokens["cred-single"] != "refreshed-token" || exec.refreshCnt.Load() != 1 {
		t.Fatalf("single refresh: errors=%v seen=%v refreshes=%d", payload.Errors, lookup.seenTokens, exec.refreshCnt.Load())
	}

	payload = decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, "", "127.0.0.1:1"))
	if len(payload.Errors) != 0 || lookup.seenTokens["cred-all"] != "refreshed-token" || exec.refreshCnt.Load() != 2 {
		t.Fatalf("refresh-all: errors=%v seen=%v refreshes=%d", payload.Errors, lookup.seenTokens, exec.refreshCnt.Load())
	}
}

func TestRefreshUsageWindowsSurfacesRenewalFailure(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	exec := &failingRefreshExecutor{
		refreshRecordExecutor: refreshRecordExecutor{provider: "custom-oauth"},
		err:                   errors.New("invalid_grant: token revoked"),
	}
	manager.RegisterExecutor(exec)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	expired := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "cred-dead", Index: "idx-dead", Provider: "custom-oauth",
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"access_token": "stale", "refresh_token": "rt", "expired": expired},
	})
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	lookup := &tokenCapturingUsageLookup{
		provider:   "custom-oauth",
		manager:    manager,
		seenTokens: map[string]string{},
		allErr:     errors.New("access token expired; waiting for refresh"),
	}
	h.RegisterUsageLookup(lookup, manager)

	payload := decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"idx-dead"}`, "127.0.0.1:1"))
	if !strings.Contains(payload.Errors["cred-dead"], "invalid_grant: token revoked") || len(lookup.seenTokens) != 0 {
		t.Fatalf("single failure: errors=%v seen=%v", payload.Errors, lookup.seenTokens)
	}

	payload = decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, "", "127.0.0.1:1"))
	if !strings.Contains(payload.Errors["cred-dead"], "invalid_grant: token revoked") {
		t.Fatalf("refresh-all failure should preserve renewal error, got errors=%v", payload.Errors)
	}
}
