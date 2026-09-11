package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestIsCloudflareChallengeErrorMessage_ExcludesOriginErrors(t *testing.T) {
	origin520 := `<html><head><title>Web server is returning an unknown error</title></head><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1><p>There is an unknown connection issue between Cloudflare and the origin web server.</p><ul><li>Ray ID: a385507da9eeb4c4</li><li>Error reference number: 520</li><li>Cloudflare Location: Los Angeles</li></ul></div></body></html>`
	if isCloudflareChallengeErrorMessage(origin520) {
		t.Fatalf("expected origin 520 error not to be classified as cloudflare challenge")
	}

	minimalOrigin := `<html><body><h1>Web server is returning an unknown error</h1><p>There is an unknown connection issue between Cloudflare and the origin web server.</p></body></html>`
	if isCloudflareChallengeErrorMessage(minimalOrigin) {
		t.Fatalf("expected minimal origin error not to be classified as cloudflare challenge")
	}

	// Legitimate challenge messages must still be recognized.
	challenges := []string{
		"cf-mitigated: challenge",
		`<html><body><script src="/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1"></script></body></html>`,
		"cloudflare challenge required",
		// Isolated "Just a moment..." challenge page test without "cloudflare challenge" phrase
		`<html><head><title>Just a moment...</title></head><body>Checking your browser... cloudflare</body></html>`,
	}
	for _, ch := range challenges {
		if !isCloudflareChallengeErrorMessage(ch) {
			t.Fatalf("expected %q to be classified as cloudflare challenge", ch)
		}
	}
}

func TestManager_MarkResult_Cloudflare520OriginError_NotTreatedAsChallenge(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1><p>There is an unknown connection issue between Cloudflare and the origin web server.</p></div></body></html>`

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	// Quota must not be marked as exceeded or challenged.
	if state.Quota.Exceeded {
		t.Fatalf("expected Quota.Exceeded to be false for 520 origin error")
	}
	if state.Quota.Reason != "" {
		t.Fatalf("expected Quota.Reason to be empty for 520 origin error, got %q", state.Quota.Reason)
	}
	if state.StatusMessage == "cloudflare challenge" {
		t.Fatalf("expected StatusMessage not to be 'cloudflare challenge' for 520 origin error")
	}
	if state.StatusMessage != origin520Msg {
		t.Fatalf("expected StatusMessage to preserve upstream error, got %q", state.StatusMessage)
	}

	// LastError must retain the 520 error.
	if state.LastError == nil || state.LastError.HTTPStatus != 520 {
		t.Fatalf("expected LastError to retain HTTP 520, got %#v", state.LastError)
	}

	// By default, transient errors apply a 1-minute retry window.
	if !state.Unavailable {
		t.Fatalf("expected model to be marked unavailable during transient cooldown")
	}
	diff := time.Until(state.NextRetryAfter)
	if diff < 45*time.Second || diff > 75*time.Second {
		t.Fatalf("expected default transient cooldown of ~60s, got %v", diff)
	}
}

func TestManager_MarkResult_Cloudflare520_CustomTransientCooldown(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-custom-cooldown",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1></div></body></html>`

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	diff := time.Until(state.NextRetryAfter)
	if diff < 3*time.Second || diff > 7*time.Second {
		t.Fatalf("expected custom transient cooldown of ~5s, got %v", diff)
	}
}

func TestManager_MarkResult_Cloudflare520_DisabledTransientCooldown(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-disabled-cooldown",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1></div></body></html>`

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when transient cooldown disabled, got %v", state.NextRetryAfter)
	}
	if state.Unavailable {
		t.Fatalf("expected model not to be unavailable when cooldown is disabled")
	}
}

func TestManager_MarkResult_Cloudflare520_DisableCoolingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-disable-cooling-auth",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1></div></body></html>`

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling is true, got %v", state.NextRetryAfter)
	}
	if state.Unavailable {
		t.Fatalf("expected model not to be unavailable when disable_cooling is true")
	}
}

func TestManager_MarkResult_Cloudflare520_WithRetryAfterHint(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-hint",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1></div></body></html>`
	hint := 15 * time.Second

	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-5.6-sol",
		Success:    false,
		RetryAfter: &hint,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	diff := time.Until(state.NextRetryAfter)
	if diff < 10*time.Second || diff > 20*time.Second {
		t.Fatalf("expected hint-based cooldown of ~15s, got %v", diff)
	}
}

func TestManager_MarkResult_Cloudflare520_DisabledTransientCooldown_IgnoresHint(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-disabled-with-hint",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1></div></body></html>`
	hint := 15 * time.Second

	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-5.6-sol",
		Success:    false,
		RetryAfter: &hint,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when transient cooldown is disabled (-1) despite RetryAfter hint, got %v", state.NextRetryAfter)
	}
	if state.Unavailable {
		t.Fatalf("expected model not to be unavailable when transient cooldown is disabled")
	}
}

func TestManager_MarkResult_AuthLevelCloudflare520_DisabledTransientCooldown_IgnoresHint(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-auth-disabled-with-hint",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1></div></body></html>`
	hint := 15 * time.Second

	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "",
		Success:    false,
		RetryAfter: &hint,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}

	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth NextRetryAfter to be zero when transient cooldown is disabled (-1) despite RetryAfter hint, got %v", updated.NextRetryAfter)
	}
	if updated.Unavailable {
		t.Fatalf("expected auth not to be unavailable when transient cooldown is disabled")
	}
}

func TestManager_MarkResult_HTTP503_TransientCooldown_RespectsDisabledAndHint(t *testing.T) {
	// 1. When disabled (-1), 503 with RetryAfter hint must NOT apply cooldown.
	{
		prevTransient := transientErrorCooldownSeconds.Load()
		transientErrorCooldownSeconds.Store(-1)
		defer transientErrorCooldownSeconds.Store(prevTransient)

		m := NewManager(nil, nil, nil)
		auth := &Auth{
			ID:       "auth-test-503-disabled",
			Provider: "claude",
			Status:   StatusActive,
		}
		m.Register(context.Background(), auth)

		hint := 20 * time.Second
		m.MarkResult(context.Background(), Result{
			AuthID:     auth.ID,
			Provider:   "claude",
			Model:      "claude-3-5-sonnet",
			Success:    false,
			RetryAfter: &hint,
			Error: &Error{
				HTTPStatus: 503,
				Message:    "service unavailable",
			},
		})

		updated, ok := m.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatalf("expected auth to be found")
		}
		state := updated.ModelStates["claude-3-5-sonnet"]
		if state == nil {
			t.Fatalf("expected model state to be present")
		}
		if !state.NextRetryAfter.IsZero() {
			t.Fatalf("expected 503 NextRetryAfter to be zero when transient cooldown is disabled (-1), got %v", state.NextRetryAfter)
		}
		if state.Unavailable {
			t.Fatalf("expected model not to be unavailable when transient cooldown is disabled")
		}
	}

	// 2. When enabled (0 = default), 503 with RetryAfter hint must use hint.
	{
		prevTransient := transientErrorCooldownSeconds.Load()
		transientErrorCooldownSeconds.Store(0)
		defer transientErrorCooldownSeconds.Store(prevTransient)

		m := NewManager(nil, nil, nil)
		auth := &Auth{
			ID:       "auth-test-503-hint",
			Provider: "claude",
			Status:   StatusActive,
		}
		m.Register(context.Background(), auth)

		hint := 20 * time.Second
		m.MarkResult(context.Background(), Result{
			AuthID:     auth.ID,
			Provider:   "claude",
			Model:      "claude-3-5-sonnet",
			Success:    false,
			RetryAfter: &hint,
			Error: &Error{
				HTTPStatus: 503,
				Message:    "service unavailable",
			},
		})

		updated, ok := m.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatalf("expected auth to be found")
		}
		state := updated.ModelStates["claude-3-5-sonnet"]
		if state == nil {
			t.Fatalf("expected model state to be present")
		}
		diff := time.Until(state.NextRetryAfter)
		if diff < 15*time.Second || diff > 25*time.Second {
			t.Fatalf("expected 503 hint cooldown of ~20s, got %v", diff)
		}
		if !state.Unavailable {
			t.Fatalf("expected model to be unavailable during cooldown")
		}
	}
}

func TestManager_MarkResult_AuthLevelCloudflare520_SetsTransientError(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-authlevel",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	origin520Msg := `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1><p>There is an unknown connection issue between Cloudflare and the origin web server.</p></div></body></html>`

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    origin520Msg,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	if updated.Quota.Exceeded {
		t.Fatalf("expected Quota.Exceeded to be false for 520 origin error")
	}
	if updated.Quota.Reason != "" {
		t.Fatalf("expected Quota.Reason to be empty for 520 origin error, got %q", updated.Quota.Reason)
	}
	if updated.StatusMessage != "transient upstream error" {
		t.Fatalf("expected StatusMessage to be 'transient upstream error', got %q", updated.StatusMessage)
	}
	if updated.LastError == nil || updated.LastError.HTTPStatus != 520 {
		t.Fatalf("expected LastError to retain HTTP 520, got %#v", updated.LastError)
	}
	if !updated.Unavailable {
		t.Fatalf("expected auth to be marked unavailable during transient cooldown")
	}
	diff := time.Until(updated.NextRetryAfter)
	if diff < 45*time.Second || diff > 75*time.Second {
		t.Fatalf("expected default transient cooldown of ~60s, got %v", diff)
	}
}

func TestIsCloudflareChallengeResultError_Excludes5xx(t *testing.T) {
	// Status >= 500 origin errors must not be classified as challenges even if containing challenge strings.
	for _, status := range []int{500, 502, 503, 504, 520, 521, 522, 523, 524, 525, 526} {
		err5xx := &Error{
			HTTPStatus: status,
			Message:    "cf-mitigated: challenge but with 5xx status code",
		}
		if isCloudflareChallengeResultError(err5xx) {
			t.Fatalf("expected %d status code to be excluded from cloudflare challenge", status)
		}
	}

	err403 := &Error{
		HTTPStatus: 403,
		Message:    "cf-mitigated: challenge",
	}
	if !isCloudflareChallengeResultError(err403) {
		t.Fatalf("expected 403 challenge to be recognized")
	}
}

func TestIsCloudflareChallengeError(t *testing.T) {
	// 520 error wrapped in standard error interface must not be treated as challenge.
	err520 := &Error{
		HTTPStatus: 520,
		Message:    "cf-error-details cf-error-520: Web server is returning an unknown error",
	}
	if isCloudflareChallengeError(err520) {
		t.Fatalf("expected 520 error not to be cloudflare challenge")
	}

	// 403 challenge error must be recognized.
	err403 := &Error{
		HTTPStatus: http.StatusForbidden,
		Message:    "cf-mitigated: challenge",
	}
	if !isCloudflareChallengeError(err403) {
		t.Fatalf("expected 403 challenge error to be recognized")
	}

	// Plain error without status but with challenge message must be recognized.
	plainChallenge := errors.New("challenge-platform orchestrate script blocked")
	if !isCloudflareChallengeError(plainChallenge) {
		t.Fatalf("expected plain challenge error to be recognized")
	}

	// Plain origin error without challenge markers must not be recognized.
	plainOrigin := errors.New("Web server is returning an unknown error between Cloudflare and origin")
	if isCloudflareChallengeError(plainOrigin) {
		t.Fatalf("expected plain origin error not to be recognized as challenge")
	}
}
