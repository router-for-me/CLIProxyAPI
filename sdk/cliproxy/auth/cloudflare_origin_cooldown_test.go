package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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

func TestManagerCloudflareChallengeResponseHeader(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	for _, model := range []string{"", "gpt-header-challenge"} {
		for _, status := range []int{http.StatusForbidden, 520} {
			for _, tt := range []struct {
				value string
				want  bool
			}{
				{"challenge", true},
				{" CHALLENGE ", true},
				{"", false},
				{"other", false},
			} {
				t.Run(fmt.Sprintf("model=%s/status=%d/header=%q", model, status, tt.value), func(t *testing.T) {
					manager := NewManager(nil, nil, nil)
					credential := &Auth{ID: t.Name(), Provider: "codex"}
					if _, err := manager.Register(t.Context(), credential); err != nil {
						t.Fatal(err)
					}
					ctx := internallogging.WithResponseHeadersHolder(t.Context())
					headers := make(http.Header)
					if tt.value != "" {
						headers.Set("Cf-Mitigated", tt.value)
					}
					internallogging.SetResponseHeaders(ctx, headers)
					manager.MarkResult(ctx, Result{AuthID: credential.ID, Provider: "codex", Model: model, Error: &Error{HTTPStatus: status, Message: `<html><body>blocked</body></html>`}})
					updated, _ := manager.GetByID(credential.ID)
					quota, message := updated.Quota, updated.StatusMessage
					if model != "" {
						quota, message = updated.ModelStates[model].Quota, updated.ModelStates[model].StatusMessage
					}
					if got := quota.Exceeded && quota.Reason == "cloudflare challenge" && message == "cloudflare challenge"; got != tt.want {
						t.Errorf("challenge = %t, want %t; quota=%+v message=%q", got, tt.want, quota, message)
					}
				})
			}
		}
	}
}

type cloudflareCompactExecutor struct {
	*compactTestExecutor
	headers http.Header
}

func (e *cloudflareCompactExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	internallogging.SetResponseHeaders(ctx, e.headers)
	return e.compactTestExecutor.Execute(ctx, auth, req, opts)
}

func TestManagerCloudflareCompactCooldown(t *testing.T) {
	previousCooling := quotaCooldownDisabled.Load()
	previousTransient := transientErrorCooldownSeconds.Load()
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(previousCooling)
		transientErrorCooldownSeconds.Store(previousTransient)
	})
	for _, tt := range []struct {
		name                string
		status              int
		body                string
		header              string
		seconds             int64
		disabled, challenge bool
		wait                time.Duration
	}{
		{name: "origin 520", status: 520, body: cloudflareOriginPage, wait: 10 * time.Second},
		{name: "origin 502", status: 502, body: cloudflareOriginPage, wait: 10 * time.Second},
		{name: "origin 501", status: 501, body: cloudflareOriginPage, wait: 10 * time.Second},
		{name: "configured origin", status: 520, body: cloudflareOriginPage, seconds: 3, wait: 3 * time.Second},
		{name: "disabled transient", status: 520, body: cloudflareOriginPage, seconds: -1},
		{name: "disabled cooling", status: 520, body: cloudflareOriginPage, disabled: true},
		{name: "header challenge", status: 520, body: "<html>blocked</html>", header: "challenge", challenge: true, wait: 10 * time.Second},
		{name: "body challenge", status: 520, body: "<html>Cloudflare challenge</html>", challenge: true, wait: 10 * time.Second},
		{name: "ordinary compact failure", status: 520, body: "temporary compact failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			quotaCooldownDisabled.Store(tt.disabled)
			transientErrorCooldownSeconds.Store(tt.seconds)
			headers := make(http.Header)
			if tt.header != "" {
				headers.Set("Cf-Mitigated", tt.header)
			}
			executor := &cloudflareCompactExecutor{compactTestExecutor: &compactTestExecutor{compactErr: compactTestStatusError{code: tt.status, msg: tt.body}}, headers: headers}
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			const model = "gpt-cloudflare-compact"
			ids := []string{t.Name() + "/a", t.Name() + "/b"}
			for _, id := range ids {
				credential := &Auth{ID: id, Provider: executor.Identifier(), Status: StatusActive}
				if _, err := manager.Register(t.Context(), credential); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, credential.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			before := time.Now()
			_, err := manager.Execute(t.Context(), []string{executor.Identifier()}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hello"}`)}, cliproxyexecutor.Options{Alt: "responses/compact"})
			after := time.Now()
			if err == nil {
				t.Fatal("expected compact failure")
			}
			if executor.calls != 2 {
				t.Errorf("upstream calls=%d, want failover across both credentials", executor.calls)
			}
			for _, id := range ids {
				updated, _ := manager.GetByID(id)
				state := updated.ModelStates[model]
				if tt.wait == 0 {
					if state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded) {
						t.Fatalf("unexpected cooldown: %+v", state)
					}
					continue
				}
				if state == nil {
					t.Fatal("compact failure bypassed cooldown handling")
				}
				if !state.Unavailable || state.NextRetryAfter.Before(before.Add(tt.wait)) || state.NextRetryAfter.After(after.Add(tt.wait)) {
					t.Errorf("retry=%v unavailable=%t, want %v cooldown", state.NextRetryAfter, state.Unavailable, tt.wait)
				}
				if state.Quota.Exceeded != tt.challenge || (state.Quota.Reason == "cloudflare challenge") != tt.challenge {
					t.Errorf("quota=%+v, want challenge=%t", state.Quota, tt.challenge)
				}
				if state.LastError == nil || state.LastError.HTTPStatus != tt.status || state.LastError.Message != tt.body {
					t.Errorf("original error lost: %+v", state.LastError)
				}
			}
		})
	}
}
