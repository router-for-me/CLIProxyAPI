package auth

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCooldownSnapshotForAuthScopes(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	modelState := func(after time.Duration) *ModelState {
		return &ModelState{Unavailable: true, NextRetryAfter: now.Add(after), Quota: QuotaState{
			Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(after), BackoffLevel: 6,
		}, LastError: &Error{HTTPStatus: 429}}
	}
	for _, tt := range []struct {
		name string
		auth *Auth
		want []string
	}{
		{name: "nil", want: []string{}},
		{name: "empty", auth: &Auth{}, want: []string{}},
		{name: "aggregate is not credential cooldown", auth: &Auth{
			Unavailable: true, NextRetryAfter: now.Add(time.Minute),
			Quota:       QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute)},
			ModelStates: map[string]*ModelState{"b": modelState(time.Minute), "a": modelState(32 * time.Second), "ready": {}},
		}, want: []string{"model:a", "model:b"}},
		{name: "credential quota and longer model coexist", auth: &Auth{
			NextRetryAfter: now.Add(time.Hour),
			Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(20 * time.Second), BackoffLevel: 9},
			ModelStates:    map[string]*ModelState{"a": modelState(time.Minute)},
		}, want: []string{"credential:", "model:a"}},
		{name: "credential fallback", auth: &Auth{
			Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 503},
		}, want: []string{"credential:"}},
		{name: "nil model states still suppress fallback like selector", auth: &Auth{
			Unavailable: true, NextRetryAfter: now.Add(time.Minute), ModelStates: map[string]*ModelState{"a": nil},
		}, want: []string{}},
		{name: "disabled credential retains timer", auth: &Auth{
			Disabled: true, Status: StatusDisabled, Unavailable: true, NextRetryAfter: now.Add(time.Minute),
		}, want: []string{"credential:"}},
		{name: "expired token does not erase timer", auth: &Auth{
			Metadata:    map[string]any{"expired": now.Add(-time.Hour).Format(time.RFC3339)},
			ModelStates: map[string]*ModelState{"a": modelState(time.Minute)},
		}, want: []string{"model:a"}},
		{name: "disabled without timer", auth: &Auth{Disabled: true, ModelStates: map[string]*ModelState{"a": {Status: StatusDisabled}}}, want: []string{}},
		{name: "forced timer survives disable cooling override", auth: &Auth{
			Metadata: map[string]any{"disable_cooling": true}, Unavailable: true, NextRetryAfter: now.Add(time.Minute),
			LastError: &Error{Code: ErrorCodeForceCooldown},
		}, want: []string{"credential:"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := CooldownSnapshotForAuth(tt.auth, now)
			if got == nil {
				t.Fatal("expected non-nil slice")
			}
			keys := make([]string, 0, len(got))
			for _, view := range got {
				keys = append(keys, view.Scope+":"+view.ModelKey)
			}
			if !reflect.DeepEqual(keys, tt.want) {
				t.Fatalf("scopes = %v, want %v", keys, tt.want)
			}
			if tt.name == "credential quota and longer model coexist" {
				if got[0].RemainingSeconds != 20 || got[0].BackoffLevel != nil || got[0].HTTPStatus != 0 {
					t.Fatalf("credential gate did not use its own deadline/diagnostics: %+v", got[0])
				}
			}
		})
	}
}

func TestCooldownSnapshotForAuthTimeBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		state   ModelState
		seconds int64
	}{
		{name: "just before expiry", state: ModelState{Unavailable: true, NextRetryAfter: now.Add(time.Nanosecond)}, seconds: 1},
		{name: "fraction rounds up", state: ModelState{Unavailable: true, NextRetryAfter: now.Add(1500 * time.Millisecond)}, seconds: 2},
		{name: "exact expiry", state: ModelState{Unavailable: true, NextRetryAfter: now}},
		{name: "past expiry", state: ModelState{Unavailable: true, NextRetryAfter: now.Add(-time.Nanosecond)}},
		{name: "historical error and backoff", state: ModelState{Status: StatusError, Quota: QuotaState{BackoffLevel: 6}}},
		{name: "no deadline", state: ModelState{Unavailable: true, Quota: QuotaState{Exceeded: true}}},
		{name: "inactive future timestamp", state: ModelState{NextRetryAfter: now.Add(time.Minute)}},
		{name: "later quota time", state: ModelState{Unavailable: true, NextRetryAfter: now.Add(time.Second), Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(3 * time.Second)}}, seconds: 3},
		{name: "later retry time", state: ModelState{Unavailable: true, NextRetryAfter: now.Add(4 * time.Second), Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Second)}}, seconds: 4},
		{name: "quota only", state: ModelState{Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(5 * time.Second)}}, seconds: 5},
		{name: "expired quota and retry", state: ModelState{Unavailable: true, NextRetryAfter: now, Quota: QuotaState{Exceeded: true, NextRecoverAt: now, BackoffLevel: 6}}},
		{name: "retry hint independent of backoff", state: ModelState{Unavailable: true, NextRetryAfter: now.Add(97 * time.Second), Quota: QuotaState{Exceeded: true, Reason: "quota", BackoffLevel: 2}}, seconds: 97},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := CooldownSnapshotForAuth(&Auth{ModelStates: map[string]*ModelState{"a": &tt.state}}, now)
			if tt.seconds == 0 {
				if len(got) != 0 {
					t.Fatalf("unexpected cooldown: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].RemainingSeconds != tt.seconds {
				t.Fatalf("views = %+v, want remaining %d", got, tt.seconds)
			}
		})
	}
}

func TestCooldownSnapshotForAuthReasons(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		quota   QuotaState
		err     *Error
		message string
		reason  string
		status  int
		backoff bool
	}{
		{name: "quota", quota: QuotaState{Exceeded: true, Reason: "quota", BackoffLevel: 6}, err: &Error{HTTPStatus: 429}, reason: "quota", status: 429, backoff: true},
		{name: "propagated quota hides old error", quota: QuotaState{Exceeded: true, Reason: "credential_quota", BackoffLevel: 6}, err: &Error{HTTPStatus: 401}, reason: "credential_quota"},
		{name: "quota hides unrelated error", quota: QuotaState{Exceeded: true, Reason: "quota"}, err: &Error{HTTPStatus: 503}, reason: "quota", backoff: true},
		{name: "challenge", quota: QuotaState{Exceeded: true, Reason: "cloudflare challenge"}, err: &Error{HTTPStatus: 403, Message: "cf-mitigated: challenge"}, reason: "cloudflare_challenge", status: 403, backoff: true},
		{name: "model unsupported", err: &Error{HTTPStatus: 400, Message: "model not supported"}, reason: "model_not_supported", status: 400},
		{name: "invalid grant", err: &Error{HTTPStatus: 400, Message: "invalid_grant"}, reason: "invalid_grant", status: 400},
		{name: "unauthorized", err: &Error{HTTPStatus: 401}, reason: "unauthorized", status: 401},
		{name: "payment", err: &Error{HTTPStatus: 402}, reason: "payment_required", status: 402},
		{name: "forbidden", err: &Error{HTTPStatus: 403}, reason: "payment_required", status: 403},
		{name: "not found", err: &Error{HTTPStatus: 404}, reason: "not_found", status: 404},
		{name: "gateway not challenge", err: &Error{HTTPStatus: 520, Message: "cloudflare challenge"}, reason: "transient_error", status: 520},
		{name: "shorter active quota does not label longer retry", quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(20 * time.Second), BackoffLevel: 6}, err: &Error{HTTPStatus: 503}, reason: "transient_error", status: 503},
		{name: "longer quota supplies deadline", quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(2 * time.Minute), BackoffLevel: 6}, err: &Error{HTTPStatus: 503}, reason: "quota", backoff: true},
		{name: "shorter propagated quota preserves longer retry reason", quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(20 * time.Second), BackoffLevel: 6}, err: &Error{HTTPStatus: 401}, reason: "unauthorized"},
		{name: "expired quota does not label new failure", quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now, BackoffLevel: 6}, err: &Error{HTTPStatus: 503}, reason: "transient_error", status: 503},
		{name: "known marker", message: "transient upstream error", reason: "transient_error"},
		{name: "unknown is sanitized", quota: QuotaState{Exceeded: true, Reason: "secret-quota"}, err: &Error{Code: "secret-code", Message: "secret-body"}, message: "secret-message", reason: "unknown"},
		{name: "unknown HTTP error", err: &Error{HTTPStatus: 418}, reason: "unknown", status: 418},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := &ModelState{Unavailable: true, NextRetryAfter: now.Add(time.Minute), Quota: tt.quota, LastError: tt.err, StatusMessage: tt.message}
			got := CooldownSnapshotForAuth(&Auth{ModelStates: map[string]*ModelState{"a": state}}, now)
			if len(got) != 1 {
				t.Fatalf("views = %+v", got)
			}
			view := got[0]
			if view.Reason != tt.reason || view.HTTPStatus != tt.status || (view.BackoffLevel != nil) != tt.backoff {
				t.Fatalf("view = %+v, want reason=%s status=%d backoff=%v", view, tt.reason, tt.status, tt.backoff)
			}
			encoded, errMarshal := json.Marshal(view)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if strings.Contains(string(encoded), "secret") {
				t.Fatalf("raw data leaked: %s", encoded)
			}
		})
	}
}

func TestCooldownSnapshotForAuthDeduplicatesWithoutMutation(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.FixedZone("offset", 3600))
	auth := &Auth{
		Metadata: map[string]any{"access_token": "secret-token"},
		ModelStates: map[string]*ModelState{
			" model-a(high) ": {Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 503}},
			"model-a":         {Unavailable: true, NextRetryAfter: now.Add(32 * time.Second), Quota: QuotaState{Exceeded: true, Reason: "quota", BackoffLevel: 6}, LastError: &Error{HTTPStatus: 429}},
			"model-b":         {Unavailable: true, NextRetryAfter: now.Add(time.Minute), Quota: QuotaState{Exceeded: true, Reason: "quota", BackoffLevel: 6}},
			" ":               {Unavailable: true, NextRetryAfter: now.Add(time.Minute)},
			"nil":             nil,
		},
	}
	before := auth.Clone()
	got := CooldownSnapshotForAuth(auth, now)
	if len(got) != 2 || got[0].ModelKey != "model-a" || got[0].Reason != "transient_error" || got[0].HTTPStatus != 503 || got[0].BackoffLevel != nil || got[0].RemainingSeconds != 60 {
		t.Fatalf("deduplicated views = %+v", got)
	}
	if got[0].RetryAt.Location() != time.UTC {
		t.Fatal("retry_at is not UTC")
	}
	for range 20 {
		if again := CooldownSnapshotForAuth(auth, now); !reflect.DeepEqual(again, got) {
			t.Fatal("unstable snapshot")
		}
	}
	*got[1].BackoffLevel = 999
	if !reflect.DeepEqual(auth, before) {
		t.Fatal("projection or returned view mutated auth")
	}
}

func TestCooldownSnapshotForAuthEqualDeadlineIsStable(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	for _, quotaKey := range []string{"a(high)", "a(low)"} {
		t.Run(quotaKey, func(t *testing.T) {
			otherKey := "a(low)"
			if quotaKey == otherKey {
				otherKey = "a(high)"
			}
			auth := &Auth{ModelStates: map[string]*ModelState{
				quotaKey: {Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 429}, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute)}},
				otherKey: {Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 503}},
			}}
			blocked, reason, next := isAuthBlockedForModel(auth, "a", now)
			if !blocked || reason != blockReasonCooldown {
				t.Fatal("precondition: selector must prefer quota on equal deadlines")
			}
			for range 20 {
				got := CooldownSnapshotForAuth(auth, now)
				if len(got) != 1 || got[0].Reason != "quota" || !got[0].RetryAt.Equal(next) || got[0].HTTPStatus != 429 {
					t.Fatalf("unstable equal-deadline selection: %+v", got)
				}
			}
		})
	}
}

func TestCooldownSnapshotForAuthLongerRetryReasonSurvivesQuotaExpiry(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	auth := &Auth{ModelStates: map[string]*ModelState{
		"a": {
			Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &Error{HTTPStatus: 503},
			Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(5 * time.Minute), BackoffLevel: 6},
		},
	}}
	for _, observed := range []time.Time{now, now.Add(5 * time.Minute), now.Add(6 * time.Minute)} {
		got := CooldownSnapshotForAuth(auth, observed)
		if len(got) != 1 || got[0].Reason != "transient_error" || got[0].HTTPStatus != 503 || got[0].BackoffLevel != nil || !got[0].RetryAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("longer retry diagnostics changed at %v: %+v", observed, got)
		}
	}
}
