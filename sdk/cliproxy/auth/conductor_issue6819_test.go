package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue6819OuterOnlyRefreshExecutor struct {
	id          string
	nextOuter   string
	refreshSIWC bool
	nextSIWC    string
	refreshErr  error
}

func (e *issue6819OuterOnlyRefreshExecutor) Identifier() string { return e.id }

func (e *issue6819OuterOnlyRefreshExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *issue6819OuterOnlyRefreshExecutor) ExecuteStream(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *issue6819OuterOnlyRefreshExecutor) CountTokens(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *issue6819OuterOnlyRefreshExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *issue6819OuterOnlyRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	before := auth.Clone()
	updated := auth.Clone()
	if e.nextOuter != "" {
		updated.Metadata["access_token"] = e.nextOuter
	}
	if e.refreshSIWC && e.nextSIWC != "" {
		siwcMap, _ := updated.Metadata["siwc"].(map[string]any)
		siwcMap["access_token"] = e.nextSIWC
		siwcMap["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	}
	updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(before.Metadata, updated.Metadata, e.nextOuter != "", e.refreshSIWC)
	return updated, nil
}

func TestIssue6819_CodexSIWCStatusEvaluation(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	// 1. use_siwc not enabled -> off
	offAuth := &Auth{
		ID:       "codex-off.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-valid",
			"refresh_token": "outer-ref",
			"use_siwc":      false,
			"siwc": map[string]any{
				"access_token": "siwc-tok",
				"expires_at":   now.Add(time.Hour).Format(time.RFC3339),
			},
		},
	}
	if got := offAuth.CodexSIWCStatus(now); got != SIWCStatusOff {
		t.Fatalf("CodexSIWCStatus(use_siwc=false) = %q, want %q", got, SIWCStatusOff)
	}

	// 2. use_siwc enabled, access_token missing -> invalid
	missingTokAuth := &Auth{
		ID:       "codex-missing.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":     "codex",
			"use_siwc": true,
			"siwc": map[string]any{
				"expires_at": now.Add(time.Hour).Format(time.RFC3339),
			},
		},
	}
	if got := missingTokAuth.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus(missing access_token) = %q, want %q", got, SIWCStatusInvalid)
	}

	// 3. use_siwc enabled, within 3m SIWCExpiryMargin window:
	// "expired" is already in the past (now - 1m), but real "expires_at" is in the future (now + 2m) -> valid!
	marginWindowAuth := &Auth{
		ID:       "codex-margin.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":     "codex",
			"use_siwc": true,
			"siwc": map[string]any{
				"access_token": "siwc-tok-margin",
				"expired":      now.Add(-1 * time.Minute).Format(time.RFC3339),
				"expires_at":   now.Add(2 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	if got := marginWindowAuth.CodexSIWCStatus(now); got != SIWCStatusValid {
		t.Fatalf("CodexSIWCStatus(before real expires_at, after margin expired) = %q, want %q", got, SIWCStatusValid)
	}
	// Once real expires_at is reached -> invalid
	if got := marginWindowAuth.CodexSIWCStatus(now.Add(2 * time.Minute)); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus(at real expires_at) = %q, want %q", got, SIWCStatusInvalid)
	}

	// 4. Existing JWT without expires_at: reads exp from JWT, ignoring margin-deducted expired
	jwtRealExp := now.Add(2 * time.Minute)
	jwtToken := makeIssue6742JWT(jwtRealExp)
	jwtAuth := &Auth{
		ID:       "codex-jwt.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":     "codex",
			"use_siwc": true,
			"siwc": map[string]any{
				"access_token": jwtToken,
				"expired":      now.Add(-1 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	if got := jwtAuth.CodexSIWCStatus(now); got != SIWCStatusValid {
		t.Fatalf("CodexSIWCStatus(existing JWT before exp) = %q, want %q", got, SIWCStatusValid)
	}
	if got := jwtAuth.CodexSIWCStatus(jwtRealExp); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus(existing JWT at exp) = %q, want %q", got, SIWCStatusInvalid)
	}

	// 5. Non-JWT token with only pre-discounted "expired" and no "expires_at" -> invalid
	onlyExpiredAuth := &Auth{
		ID:       "codex-only-expired.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":     "codex",
			"use_siwc": true,
			"siwc": map[string]any{
				"access_token": "opaque-no-expires-at",
				"expired":      now.Add(10 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	if got := onlyExpiredAuth.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus(only margin-discounted expired without real expiry) = %q, want %q", got, SIWCStatusInvalid)
	}
}

func TestIssue6819_OuterRefreshDoesNotClearSIWCRejectedAccessToken(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	siwcToken := "siwc-rejected-token-1"
	auth := &Auth{
		ID:       "codex-issue6819.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-token-1",
			"refresh_token": "outer-refresh-1",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  siwcToken,
				"refresh_token": "siwc-refresh-1",
				"client_id":     "oaiapp_1",
				"expires_at":    now.Add(30 * time.Minute).Format(time.RFC3339),
				"expired":       now.Add(27 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	exec := &issue6819OuterOnlyRefreshExecutor{
		id:          "codex",
		nextOuter:   "outer-token-2",
		refreshSIWC: false,
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(exec)
	if _, errReg := m.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	m.markRejectedAccessToken(auth.ID, siwcToken)
	beforeRefresh, _ := m.GetByID(auth.ID)
	if beforeRefresh.RejectedAccessToken != siwcToken {
		t.Fatalf("precondition RejectedAccessToken = %q, want %q", beforeRefresh.RejectedAccessToken, siwcToken)
	}
	if got := beforeRefresh.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after 401 = %q, want %q", got, SIWCStatusInvalid)
	}

	// 1. Outer Codex OAuth refreshes alone while SIWC was not refreshed.
	refreshed, errRefresh := m.ForceRefreshAuth(context.Background(), auth.ID)
	if errRefresh != nil {
		t.Fatalf("ForceRefreshAuth error: %v", errRefresh)
	}
	if refreshed.RejectedAccessToken != siwcToken {
		t.Fatalf("RejectedAccessToken after outer-only refresh = %q, want %q preserved", refreshed.RejectedAccessToken, siwcToken)
	}
	if got := refreshed.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after outer-only refresh = %q, want %q", got, SIWCStatusInvalid)
	}

	// 2. Outer Codex OAuth updated via Manager.Update (e.g. re-login preserving SIWC).
	relogin := refreshed.Clone()
	relogin.RejectedAccessToken = ""
	relogin.Metadata["access_token"] = "outer-token-3"
	relogin.Metadata["refresh_token"] = "outer-refresh-3"
	updatedOuter, errUpdate := m.Update(context.Background(), relogin)
	if errUpdate != nil {
		t.Fatalf("Update error: %v", errUpdate)
	}
	if updatedOuter.RejectedAccessToken != siwcToken {
		t.Fatalf("RejectedAccessToken after outer-only Update = %q, want %q preserved", updatedOuter.RejectedAccessToken, siwcToken)
	}
	if got := updatedOuter.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after outer-only Update = %q, want %q", got, SIWCStatusInvalid)
	}

	// 3. A refresh that returns the exact same rejected SIWC access_token must not clear the rejection.
	exec.refreshSIWC = true
	exec.nextSIWC = siwcToken
	sameTokenRefresh, errSameRefresh := m.ForceRefreshAuth(context.Background(), auth.ID)
	if errSameRefresh != nil {
		t.Fatalf("ForceRefreshAuth (unchanged SIWC token) error: %v", errSameRefresh)
	}
	if sameTokenRefresh.RejectedAccessToken != siwcToken {
		t.Fatalf("RejectedAccessToken after refresh returning same SIWC token = %q, want %q", sameTokenRefresh.RejectedAccessToken, siwcToken)
	}
	if got := sameTokenRefresh.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after refresh returning same SIWC token = %q, want %q", got, SIWCStatusInvalid)
	}

	// 4. Concurrent dual-partition refresh started on an older SIWC token A:
	// Outer partition still matches and applies, while SIWC partition conflicts with current rejected token B and is skipped.
	staleBase := sameTokenRefresh.Clone()
	staleBase.RejectedAccessToken = ""
	staleBase.Metadata["access_token"] = "outer-token-3"
	staleBase.Metadata["refresh_token"] = "outer-refresh-3"
	staleBase.Metadata["siwc"] = map[string]any{
		"access_token":  "siwc-older-token-A",
		"refresh_token": "siwc-refresh-A",
		"client_id":     "oaiapp_1",
		"expires_at":    now.Add(30 * time.Minute).Format(time.RFC3339),
		"expired":       now.Add(27 * time.Minute).Format(time.RFC3339),
	}
	// Ensure current live auth has outer-token-3 (matching staleBase's outer) and rejected siwcToken (conflicting with staleBase's SIWC).
	liveBeforeStaleMerge := sameTokenRefresh.Clone()
	liveBeforeStaleMerge.Metadata["access_token"] = "outer-token-3"
	liveBeforeStaleMerge.Metadata["refresh_token"] = "outer-refresh-3"
	if _, errSetLive := m.Update(context.Background(), liveBeforeStaleMerge); errSetLive != nil {
		t.Fatalf("Update liveBeforeStaleMerge error: %v", errSetLive)
	}
	m.markRejectedAccessToken(auth.ID, siwcToken)

	staleUpdated := staleBase.Clone()
	staleUpdated.Metadata["access_token"] = "outer-token-4"
	staleUpdated.Metadata["refresh_token"] = "outer-refresh-4"
	staleUpdated.Metadata["siwc"] = map[string]any{
		"access_token":  "siwc-stale-refreshed-A2",
		"refresh_token": "siwc-refresh-A2",
		"client_id":     "oaiapp_1",
		"expires_at":    now.Add(time.Hour).Format(time.RFC3339),
		"expired":       now.Add(57 * time.Minute).Format(time.RFC3339),
	}
	staleUpdated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(staleBase.Metadata, staleUpdated.Metadata, true, true)

	afterStaleMerge, errStaleMerge := m.UpdateRefreshedAuth(context.Background(), staleBase, staleUpdated)
	if errStaleMerge != nil {
		t.Fatalf("UpdateRefreshedAuth (stale SIWC partition) error: %v", errStaleMerge)
	}
	if gotOuter := authOuterAccessToken(afterStaleMerge); gotOuter != "outer-token-4" {
		t.Fatalf("outer token after partial patch = %q, want outer-token-4", gotOuter)
	}
	if gotSIWC := authSIWCTokenField(afterStaleMerge, "access_token"); gotSIWC != siwcToken {
		t.Fatalf("SIWC token after skipped conflicting SIWC patch = %q, want %q", gotSIWC, siwcToken)
	}
	if afterStaleMerge.RejectedAccessToken != siwcToken {
		t.Fatalf("RejectedAccessToken after skipped conflicting SIWC patch = %q, want %q preserved", afterStaleMerge.RejectedAccessToken, siwcToken)
	}
	if got := afterStaleMerge.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after skipped conflicting SIWC patch = %q, want %q", got, SIWCStatusInvalid)
	}

	// 5. Replacing the SIWC token via SIWC refresh clears the old rejection and restores valid status.
	exec.refreshSIWC = true
	exec.nextSIWC = "siwc-fresh-token-2"
	refreshedSIWC, errRefreshSIWC := m.ForceRefreshAuth(context.Background(), auth.ID)
	if errRefreshSIWC != nil {
		t.Fatalf("ForceRefreshAuth (SIWC) error: %v", errRefreshSIWC)
	}
	if refreshedSIWC.RejectedAccessToken != "" {
		t.Fatalf("RejectedAccessToken after SIWC token replacement = %q, want empty", refreshedSIWC.RejectedAccessToken)
	}
	if got := refreshedSIWC.CodexSIWCStatus(now); got != SIWCStatusValid {
		t.Fatalf("CodexSIWCStatus after SIWC token replacement = %q, want %q", got, SIWCStatusValid)
	}
}

func TestIssue6819_FailedSIWCRefreshAndNonAuthErrorsPreserveValidityUntilRealExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	realExp := now.Add(2 * time.Minute)
	siwcToken := "siwc-unexpired-token"
	auth := &Auth{
		ID:       "codex-issue6819-nonauth.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-token",
			"refresh_token": "outer-refresh",
			"expired":       now.Add(5 * 24 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  siwcToken,
				"refresh_token": "siwc-refresh-revoked",
				"client_id":     "oaiapp_1",
				"expires_at":    realExp.Format(time.RFC3339),
				"expired":       now.Add(-1 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	exec := &issue6819OuterOnlyRefreshExecutor{
		id:         "codex",
		refreshErr: errors.New("OpenAI OAuth token request failed (401): invalid_grant"),
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(exec)
	if _, errReg := m.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	// 1. 429 and 503 request errors must not mark the SIWC credential as invalid.
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		m.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: "codex",
			Model:    "gpt-5.4",
			Success:  false,
			Error:    &Error{HTTPStatus: status, Message: "upstream transient error"},
		})
		current, _ := m.GetByID(auth.ID)
		if got := current.CodexSIWCStatus(now); got != SIWCStatusValid {
			t.Fatalf("CodexSIWCStatus after HTTP %d = %q, want %q", status, got, SIWCStatusValid)
		}
	}

	// 2. Refresh failure (even with invalid_grant on refresh_token) must not extend expiration
	// and must keep status "valid" while the access token itself is unexpired and unrejected.
	_, errRefresh := m.ForceRefreshAuth(context.Background(), auth.ID)
	if errRefresh == nil {
		t.Fatal("expected ForceRefreshAuth to fail")
	}
	afterFailedRefresh, _ := m.GetByID(auth.ID)
	gotExp, okExp := afterFailedRefresh.SIWCRealExpirationTime()
	if !okExp || !gotExp.Equal(realExp) {
		t.Fatalf("SIWCRealExpirationTime after failed refresh = (%v, %v), want (%v, true)", gotExp, okExp, realExp)
	}
	if got := afterFailedRefresh.CodexSIWCStatus(now); got != SIWCStatusValid {
		t.Fatalf("CodexSIWCStatus before real expiry after failed refresh = %q, want %q", got, SIWCStatusValid)
	}
	// Once real expiration is reached, status becomes "invalid".
	if got := afterFailedRefresh.CodexSIWCStatus(realExp); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus at real expiry = %q, want %q", got, SIWCStatusInvalid)
	}

	// 3. When a subsequent refresh succeeds and obtains a new token, status is re-evaluated as "valid".
	exec.refreshErr = nil
	exec.refreshSIWC = true
	exec.nextSIWC = "siwc-recovered-token"
	recovered, errRecover := m.ForceRefreshAuth(context.Background(), auth.ID)
	if errRecover != nil {
		t.Fatalf("ForceRefreshAuth recovery error: %v", errRecover)
	}
	if got := recovered.CodexSIWCStatus(realExp.Add(time.Minute)); got != SIWCStatusValid {
		t.Fatalf("CodexSIWCStatus after successful token refresh = %q, want %q", got, SIWCStatusValid)
	}
}

type issue6819StreamExecutor struct {
	issue6819OuterOnlyRefreshExecutor
	chunks chan cliproxyexecutor.StreamChunk
}

func (e *issue6819StreamExecutor) ExecuteStream(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}

func TestIssue6819_InStream401RecordedAfterConcurrentOuterRefreshAndAcrossUseSIWCToggle(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	siwcToken := "siwc-stream-token-A"
	auth := &Auth{
		ID:       "codex-issue6819-stream.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-token-1",
			"refresh_token": "outer-refresh-1",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  siwcToken,
				"refresh_token": "siwc-refresh-A",
				"client_id":     "oaiapp_1",
				"expires_at":    now.Add(30 * time.Minute).Format(time.RFC3339),
				"expired":       now.Add(27 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok\n\n")}
	exec := &issue6819StreamExecutor{
		issue6819OuterOnlyRefreshExecutor: issue6819OuterOnlyRefreshExecutor{
			id:          "codex",
			nextOuter:   "outer-token-2",
			refreshSIWC: false,
		},
		chunks: chunks,
	}
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(exec)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errReg := m.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	// 1. Start a streaming SIWC request using token A (captured at CredentialVersion 1).
	streamRes, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream error: %v", errStream)
	}

	// 2. While the stream is open, outer Codex OAuth refreshes alone (advancing CredentialVersion to 2), leaving SIWC token A unchanged.
	if _, errOuterRefresh := m.ForceRefreshAuth(context.Background(), auth.ID); errOuterRefresh != nil {
		t.Fatalf("ForceRefreshAuth (outer) error: %v", errOuterRefresh)
	}

	// 3. Deliver in-stream 401 on the SIWC stream and drain the stream channel.
	chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "401 unauthorized"}}
	close(chunks)
	for range streamRes.Chunks {
	}

	afterStream401, _ := m.GetByID(auth.ID)
	if got := afterStream401.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after in-stream 401 following outer refresh = %q, want %q", got, SIWCStatusInvalid)
	}

	// 4. Disable use_siwc -> status is "off"; then outer token hits 401 and refreshes; then re-enable use_siwc with the same token A -> still "invalid"!
	disabledSIWC := afterStream401.Clone()
	disabledSIWC.Metadata["use_siwc"] = false
	afterToggleOff, errToggleOff := m.Update(context.Background(), disabledSIWC)
	if errToggleOff != nil {
		t.Fatalf("Update (use_siwc=false) error: %v", errToggleOff)
	}
	if got := afterToggleOff.CodexSIWCStatus(now); got != SIWCStatusOff {
		t.Fatalf("CodexSIWCStatus when use_siwc=false = %q, want %q", got, SIWCStatusOff)
	}

	// Outer token hits 401 while use_siwc=false, then outer refreshes to outer-token-3.
	m.markRejectedAccessToken(auth.ID, "outer-token-2")
	exec.nextOuter = "outer-token-3"
	if _, errOuterRef2 := m.ForceRefreshAuth(context.Background(), auth.ID); errOuterRef2 != nil {
		t.Fatalf("ForceRefreshAuth (outer recovery) error: %v", errOuterRef2)
	}

	// Re-enable use_siwc while SIWC token is still the rejected token A.
	reEnabled := afterToggleOff.Clone()
	currentAfterOuter, _ := m.GetByID(auth.ID)
	reEnabled = currentAfterOuter.Clone()
	reEnabled.Metadata["use_siwc"] = true
	afterToggleOn, errToggleOn := m.Update(context.Background(), reEnabled)
	if errToggleOn != nil {
		t.Fatalf("Update (use_siwc=true) error: %v", errToggleOn)
	}
	if got := afterToggleOn.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after re-enabling use_siwc with still-rejected token A = %q, want %q", got, SIWCStatusInvalid)
	}
}

type issue6819Retry401Executor struct {
	issue6819OuterOnlyRefreshExecutor
	onRetry func()
}

func (e *issue6819Retry401Executor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if authAccessToken(auth) == "siwc-retry-token-B" && e.onRetry != nil {
		e.onRetry()
	}
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "401 unauthorized"}
}

func TestIssue6819_ExecuteRetry401WithConcurrentOuterRefreshAndSameTokenRebind(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := t.TempDir()
	path := dir + "/codex-retry-bind.json"
	metadata := map[string]any{
		"type":          "codex",
		"access_token":  "outer-token-1",
		"refresh_token": "outer-refresh-1",
		"account_id":    "acct-1",
		"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
		"use_siwc":      true,
		"siwc": map[string]any{
			"access_token":  "siwc-init-token-A",
			"refresh_token": "siwc-refresh-A",
			"client_id":     "oaiapp_1",
			"expires_at":    now.Add(30 * time.Minute).Format(time.RFC3339),
			"expired":       now.Add(27 * time.Minute).Format(time.RFC3339),
		},
	}
	if errWrite := codex.WriteJSONFileAtomic(path, metadata); errWrite != nil {
		t.Fatalf("WriteJSONFileAtomic: %v", errWrite)
	}

	m := NewManager(nil, &FillFirstSelector{}, nil)
	exec := &issue6819Retry401Executor{
		issue6819OuterOnlyRefreshExecutor: issue6819OuterOnlyRefreshExecutor{
			id:          "codex",
			nextOuter:   "outer-token-2",
			refreshSIWC: true,
			nextSIWC:    "siwc-retry-token-B",
		},
	}
	exec.onRetry = func() {
		// While retry with token B is in flight, an outer-only refresh advances CredentialVersion.
		exec.refreshSIWC = false
		exec.nextOuter = "outer-token-3"
		_, _ = m.ForceRefreshAuth(context.Background(), "codex-retry-bind.json")
	}
	m.RegisterExecutor(exec)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("codex-retry-bind.json", "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	t.Cleanup(func() { reg.UnregisterClient("codex-retry-bind.json") })

	registered, errReg := m.Register(context.Background(), &Auth{
		ID:         "codex-retry-bind.json",
		FileName:   "codex-retry-bind.json",
		Provider:   "codex",
		Status:     StatusActive,
		Metadata:   metadata,
		Attributes: map[string]string{"path": path, AttributeSource: path, AttributeSourceBackend: AuthSourceFile},
	})
	if errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	// 1. Execute hits 401 on token A, refreshes to token B, outer refreshes concurrently during retry, and retry hits 401 on token B.
	_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected Execute to fail with 401")
	}
	afterRetry401, _ := m.GetByID(registered.ID)
	if got := afterRetry401.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after retry 401 on token B with concurrent outer refresh = %q, want %q", got, SIWCStatusInvalid)
	}

	// 2. Re-binding with the exact same rejected SIWC token B must keep status "invalid";
	// re-binding with a different token C updates status to "valid".
	diskWithB := afterRetry401.Metadata
	if errWrite := codex.WriteJSONFileAtomic(path, diskWithB); errWrite != nil {
		t.Fatalf("WriteJSONFileAtomic(diskWithB): %v", errWrite)
	}
	m.SetConfig(&internalconfig.Config{AuthDir: dir})
	reboundSame, errBindSame := m.BindCodexSIWC(context.Background(), registered.ID, registered.RegistrationEpoch, "acct-1", &codex.CodexSIWCTokenData{
		AccessToken:  "siwc-retry-token-B",
		RefreshToken: "siwc-refresh-B2",
		ClientID:     "oaiapp_1",
		Expire:       now.Add(27 * time.Minute).Format(time.RFC3339),
		ExpiresAt:    now.Add(30 * time.Minute).Format(time.RFC3339),
	})
	if errBindSame != nil {
		t.Fatalf("BindCodexSIWC (same token B) error: %v", errBindSame)
	}
	if got := reboundSame.CodexSIWCStatus(now); got != SIWCStatusInvalid {
		t.Fatalf("CodexSIWCStatus after re-binding identical rejected token B = %q, want %q", got, SIWCStatusInvalid)
	}

	reboundNew, errBindNew := m.BindCodexSIWC(context.Background(), registered.ID, registered.RegistrationEpoch, "acct-1", &codex.CodexSIWCTokenData{
		AccessToken:  "siwc-fresh-token-C",
		RefreshToken: "siwc-refresh-C",
		ClientID:     "oaiapp_1",
		Expire:       now.Add(27 * time.Minute).Format(time.RFC3339),
		ExpiresAt:    now.Add(30 * time.Minute).Format(time.RFC3339),
	})
	if errBindNew != nil {
		t.Fatalf("BindCodexSIWC (new token C) error: %v", errBindNew)
	}
	if got := reboundNew.CodexSIWCStatus(now); got != SIWCStatusValid {
		t.Fatalf("CodexSIWCStatus after re-binding new token C = %q, want %q", got, SIWCStatusValid)
	}
}

type codexClientOuter401Executor struct {
	issue6819OuterOnlyRefreshExecutor
}

func (e *codexClientOuter401Executor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if authOuterAccessToken(auth) == "outer-token-2" {
		return cliproxyexecutor.Response{Payload: []byte(`{"id":"ok"}`)}, nil
	}
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "401 unauthorized"}
}

func TestCodexClient401OnSIWCAuthRejectsAndRefreshesOuterTokenOnly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	auth := &Auth{
		ID:       "codex-client-outer-401.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-token-1",
			"refresh_token": "outer-refresh-1",
			"account_id":    "acct-1",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-valid-token",
				"refresh_token": "siwc-refresh-1",
				"client_id":     "oaiapp_1",
				"expires_at":    now.Add(30 * time.Minute).Format(time.RFC3339),
				"expired":       now.Add(27 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	m := NewManager(nil, &FillFirstSelector{}, nil)
	exec := &codexClientOuter401Executor{
		issue6819OuterOnlyRefreshExecutor: issue6819OuterOnlyRefreshExecutor{
			id:          "codex",
			nextOuter:   "outer-token-2",
			refreshSIWC: false,
		},
	}
	m.RegisterExecutor(exec)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errReg := m.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	resp, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{
		Headers: http.Header{"User-Agent": []string{"codex-tui/0.154.0"}},
	})
	if errExec != nil {
		t.Fatalf("Execute with Codex client after outer 401 refresh error: %v", errExec)
	}
	if string(resp.Payload) != `{"id":"ok"}` {
		t.Fatalf("Execute payload = %s, want {\"id\":\"ok\"}", resp.Payload)
	}
	after, _ := m.GetByID(auth.ID)
	if got := after.CodexSIWCStatus(now); got != SIWCStatusValid {
		t.Fatalf("CodexSIWCStatus after Codex client outer 401 = %q, want %q", got, SIWCStatusValid)
	}
	if after.RejectedSIWCAccessToken != "" {
		t.Fatalf("RejectedSIWCAccessToken = %q, want empty", after.RejectedSIWCAccessToken)
	}

	// Simulate SIWC token rotation bumping CredentialVersion while outer token remains "outer-token-2".
	// A result from a Codex client whose User-Agent was only in gin.Context (not Result.Options.Headers)
	// and which used "outer-token-2" at the previous CredentialVersion must NOT be treated as stale.
	prevVersion := after.CredentialVersion
	siwcRotated := after.Clone()
	siwcRotated.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-rotated-token-v2"
	if _, errUpd := m.Update(context.Background(), siwcRotated); errUpd != nil {
		t.Fatalf("Update siwcRotated: %v", errUpd)
	}
	currentAfterSIWCRotate, _ := m.GetByID(auth.ID)
	if isStaleExecutionResult(Result{
		AuthID:            auth.ID,
		Provider:          "codex",
		Model:             "gpt-5.4",
		Success:           false,
		AccessToken:       "outer-token-2",
		CredentialVersion: prevVersion,
		RegistrationEpoch: currentAfterSIWCRotate.RegistrationEpoch,
	}, currentAfterSIWCRotate) {
		t.Fatal("expected Codex client result with current outer token not to be treated as stale when only SIWC token rotated")
	}
}
