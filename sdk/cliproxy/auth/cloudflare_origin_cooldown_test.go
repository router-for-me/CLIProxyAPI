package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

const cloudflareOriginPage = `<html><body><h1>Web server is returning an unknown error</h1><p>There is an unknown connection issue between Cloudflare and the origin web server.</p></body></html>`

func TestCloudflareChallengeRequiresExplicitEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, message string
		want          bool
	}{
		{"520 origin page", cloudflareOriginPage, false},
		{"generic Cloudflare HTML", `<html><body>Cloudflare: upstream unavailable</body></html>`, false},
		{"Cloudflare plain text", "Cloudflare: upstream unavailable", false},
		{"unrelated HTML", `<html><title>Just a moment</title><body>upstream unavailable</body></html>`, false},
		{"challenge script", `<html><script src="/cdn-cgi/challenge-platform/scripts/jsd/main.js"></script></html>`, true},
		{"challenge header", "CF-Mitigated: challenge", true},
		{"explicit challenge message", "upstream returned a Cloudflare challenge", true},
		{"Cloudflare challenge title", `<html><title>Just a moment...</title><body>Cloudflare</body></html>`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCloudflareChallengeErrorMessage(tt.message); got != tt.want {
				t.Errorf("challenge = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestManagerCloudflareOriginUsesTransientCooldown(t *testing.T) {
	previousCooling := quotaCooldownDisabled.Load()
	previousTransient := transientErrorCooldownSeconds.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(previousCooling)
		transientErrorCooldownSeconds.Store(previousTransient)
	})
	for _, model := range []string{"", "gpt-cloudflare-origin"} {
		for _, tt := range []struct {
			name           string
			status         int
			seconds        int64
			disableCooling bool
			wantWait       time.Duration
		}{
			{name: "520 default", status: 520, wantWait: 10 * time.Second},
			{name: "502 default", status: http.StatusBadGateway, wantWait: 10 * time.Second},
			{name: "524 default", status: 524, wantWait: 10 * time.Second},
			{name: "configured shorter", status: 520, seconds: 3, wantWait: 3 * time.Second},
			{name: "configured longer", status: 520, seconds: 30, wantWait: 30 * time.Second},
			{name: "transient cooling disabled", status: 520, seconds: -1},
			{name: "all cooling disabled", status: 520, disableCooling: true},
		} {
			t.Run(fmt.Sprintf("model=%s/%s", model, tt.name), func(t *testing.T) {
				transientErrorCooldownSeconds.Store(tt.seconds)
				quotaCooldownDisabled.Store(tt.disableCooling)
				manager := NewManager(nil, nil, nil)
				credential := &Auth{ID: t.Name(), Provider: "codex"}
				if _, err := manager.Register(context.Background(), credential); err != nil {
					t.Fatal(err)
				}
				before := time.Now()
				for range 2 {
					manager.MarkResult(context.Background(), Result{AuthID: credential.ID, Provider: "codex", Model: model, Error: &Error{HTTPStatus: tt.status, Message: cloudflareOriginPage}})
				}
				after := time.Now()
				updated, _ := manager.GetByID(credential.ID)
				quota, next, unavailable := updated.Quota, updated.NextRetryAfter, updated.Unavailable
				lastError, message := updated.LastError, updated.StatusMessage
				if model != "" {
					state := updated.ModelStates[model]
					if state == nil {
						t.Fatal("transient model state was not recorded")
					}
					quota, next, unavailable = state.Quota, state.NextRetryAfter, state.Unavailable
					lastError, message = state.LastError, state.StatusMessage
				}
				if quota.Exceeded || quota.Reason != "" || quota.BackoffLevel != 0 || !quota.NextRecoverAt.IsZero() {
					t.Errorf("origin failure recorded as quota/challenge: %+v", quota)
				}
				if lastError == nil || lastError.HTTPStatus != tt.status || lastError.Message != cloudflareOriginPage || message != cloudflareOriginPage {
					t.Errorf("original diagnosis was lost: error=%+v message=%q", lastError, message)
				}
				if tt.wantWait == 0 {
					if !next.IsZero() || unavailable {
						t.Errorf("disabled cooling: next=%v unavailable=%t", next, unavailable)
					}
				} else if !unavailable || next.Before(before.Add(tt.wantWait)) || next.After(after.Add(tt.wantWait)) {
					t.Errorf("next=%v unavailable=%t, want %s transient cooldown", next, unavailable, tt.wantWait)
				}
			})
		}
	}
}

func TestCloudflareOriginRetainsLongerExistingCooldown(t *testing.T) {
	previous := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previous) })
	now := time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)
	deadline := now.Add(5 * time.Minute)
	credential := &Auth{NextRetryAfter: deadline}
	applyAuthFailureState(credential, &Error{HTTPStatus: 520, Message: cloudflareOriginPage}, nil, now, false)
	if !credential.NextRetryAfter.Equal(deadline) {
		t.Fatalf("existing cooldown shortened: %v", credential.NextRetryAfter)
	}
}
