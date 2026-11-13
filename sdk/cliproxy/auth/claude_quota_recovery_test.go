package auth

import (
	"context"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestClaudeQuotaRecoveryAfterManualReset(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
	registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-fable-5-2"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	delay := 5 * 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &delay, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
	before, _ := m.GetByID(a.ID)
	if blocked, _, _ := isAuthBlockedForModel(before, "claude-fable-5-2", time.Now()); !blocked {
		t.Fatal("fixture is not blocked")
	}
	if !claudeUsageAvailable([]byte(`{"five_hour":null,"seven_day":{"utilization":78},"iguana_necktie":{"utilization":0}}`)) {
		t.Fatal("restored usage rejected")
	}
	if err := m.restoreClaudeQuota(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-fable-5-2", time.Now()); blocked {
		t.Fatal("manual reset left Fable blocked until the old weekly deadline")
	}
}

func TestClaudeUsageRecoveryRequiresCapacity(t *testing.T) {
	for _, body := range []string{`{"five_hour":null,"seven_day":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":100}]}`, `{"five_hour":null,"seven_day":{"utilization":0},"limits":[{}]}`, `{"five_hour":null,"seven_day":{"utilization":0},"weekly_scoped_shares":[{"limit_index":0}]}`, `{"five_hour":null,"seven_day":{"utilization":0,"locked_reason":"policy"}}`, `{}`, `null`, `{"seven_day":{"utilization":0}}`, `{"five_hour":null,"seven_day":null}`, `{"five_hour":null,"seven_day":{"utilization":100}}`, `{"five_hour":{"utilization":100},"seven_day":{"utilization":0}}`, `{"five_hour":null,"seven_day":{"utilization":0},"iguana_necktie":{"utilization":100}}`, `{"five_hour":null,"seven_day":{"utilization":"0"}}`, `{"five_hour":null,"seven_day":{"utilization":-1}}`, `{"five_hour":null,"seven_day":{"utilization":0},"unknown_scoped_limit":{"utilization":100}}`} {
		if claudeUsageAvailable([]byte(body)) {
			t.Errorf("unsafe recovery for %s", body)
		}
	}
}

func TestClaudeQuotaRecoveryKeepsNewerFailures(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	fail := func() {
		d := 5 * 24 * time.Hour
		m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &d, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
	}
	fail()
	snapshot, _ := m.GetByID(a.ID)
	fail()
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	if !after.Quota.Exceeded {
		t.Fatal("old usage response cleared a newer failure")
	}
}

func TestClaudeQuotaRecoveryKeepsUnrelatedRestrictions(t *testing.T) {
	for _, kind := range []string{"disabled", "replaced", "unauthorized", "model failure", "cancelled", "home"} {
		t.Run(kind, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			m := NewManager(nil, nil, nil)
			a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			delay := 5 * 24 * time.Hour
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &delay, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
			before, _ := m.GetByID(a.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "disabled":
				m.mu.Lock()
				m.auths[a.ID].Disabled = true
				m.mu.Unlock()
			case "replaced":
				if _, err := m.Register(ctx, a); err != nil {
					t.Fatal(err)
				}
			case "unauthorized":
				m.mu.Lock()
				m.auths[a.ID].LastError = &Error{HTTPStatus: 401}
				m.mu.Unlock()
			case "model failure":
				m.mu.Lock()
				m.auths[a.ID].ModelStates["other"] = &ModelState{Unavailable: true, Status: StatusError, LastError: &Error{HTTPStatus: 503}}
				m.mu.Unlock()
			case "cancelled":
				cancel()
			case "home":
				m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			}
			pre, _ := m.GetByID(a.ID)
			_ = m.restoreClaudeQuota(ctx, before)
			post, _ := m.GetByID(a.ID)
			if !reflect.DeepEqual(pre, post) {
				t.Fatal("usage recovery changed an unrelated restriction or replaced credential")
			}
		})
	}
}
