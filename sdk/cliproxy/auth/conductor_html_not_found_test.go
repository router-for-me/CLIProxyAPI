package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestManager_HTMLNotFoundCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	previousTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(0)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous); transientErrorCooldownSeconds.Store(previousTransient) })
	for _, model := range []string{"", "html-not-found-model"} {
		for _, tc := range []struct {
			name, message string
			want          time.Duration
		}{
			{"html", "<html>\n<head><title>404 Not Found</title></head></html>", time.Minute},
			{"doctype", " \n<!DOCTYPE HTML><html><body>Not Found</body></html>", time.Minute},
			{"plain", "404 page not found", 12 * time.Hour},
			{"model", `{"error":{"code":"model_not_found","message":"model html-not-found-model was not found"}}`, 12 * time.Hour},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				m := NewManager(nil, nil, nil)
				a := &Auth{ID: "html-not-found-auth", Provider: "codex"}
				if _, err := m.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				before := time.Now()
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusNotFound, Message: tc.message}})
				after := time.Now()
				got, _ := m.GetByID(a.ID)
				deadline := got.NextRetryAfter
				if model != "" {
					deadline = got.ModelStates[model].NextRetryAfter
				}
				if deadline.Before(before.Add(tc.want)) || deadline.After(after.Add(tc.want)) {
					t.Fatalf("cooldown = %v, want %v", deadline.Sub(before), tc.want)
				}
			})
		}
	}
}

func TestApplyAuthFailureState_HTMLNotFoundPreservesLongerCooldown(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	want := now.Add(30 * time.Minute)
	a := &Auth{NextRetryAfter: want}
	applyAuthFailureState(a, &Error{HTTPStatus: 404, Message: "<html><body>Not Found</body></html>"}, nil, now, false)
	if !a.NextRetryAfter.Equal(want) {
		t.Fatalf("deadline = %v, want %v", a.NextRetryAfter, want)
	}
}

func TestManager_HTMLNotFoundCooldownSettings(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	previousTransient := transientErrorCooldownSeconds.Load()
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous); transientErrorCooldownSeconds.Store(previousTransient) })
	for _, tc := range []struct {
		name     string
		disabled bool
		seconds  int64
		want     time.Duration
	}{
		{"disabled", true, 0, 0},
		{"transient disabled", false, -1, 0},
		{"custom transient", false, 7, 7 * time.Second},
	} {
		for _, model := range []string{"", "html-not-found-model"} {
			t.Run(tc.name+"/"+model, func(t *testing.T) {
				quotaCooldownDisabled.Store(tc.disabled)
				transientErrorCooldownSeconds.Store(tc.seconds)
				m := NewManager(nil, nil, nil)
				a := &Auth{ID: "html-settings-auth", Provider: "codex"}
				if _, err := m.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				before := time.Now()
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 404, Message: "<html><body>Not Found</body></html>"}})
				after := time.Now()
				got, _ := m.GetByID(a.ID)
				deadline, unavailable := got.NextRetryAfter, got.Unavailable
				if model != "" {
					deadline, unavailable = got.ModelStates[model].NextRetryAfter, got.ModelStates[model].Unavailable
				}
				if tc.want == 0 {
					if !deadline.IsZero() || unavailable {
						t.Fatalf("disabled cooldown: deadline=%v unavailable=%v", deadline, unavailable)
					}
				} else if !unavailable || deadline.Before(before.Add(tc.want)) || deadline.After(after.Add(tc.want)) {
					t.Fatalf("cooldown=%v unavailable=%v, want %v", deadline.Sub(before), unavailable, tc.want)
				}
			})
		}
	}
}
