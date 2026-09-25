package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// newAutoDisableTestAuth builds an auth carrying the auto-disable attributes the
// synthesizer stamps for a PG-backed upstream row.
func newAutoDisableTestAuth(id, provider, codesJSON, entryKey string) *Auth {
	attrs := map[string]string{}
	if codesJSON != "" {
		attrs[AttributeAutoDisableCodes] = codesJSON
	}
	if entryKey != "" {
		attrs[AttributeEntryProviderKey] = entryKey
	}
	return &Auth{ID: id, Provider: provider, Attributes: attrs}
}

func registerAutoDisableTestAuth(t *testing.T, m *Manager, auth *Auth) {
	t.Helper()
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
}

// markAutoDisableResult drives a classified failure through MarkResult, the
// conductor path that fires the auto-disable sink.
func markAutoDisableResult(m *Manager, authID, code string, status int) {
	m.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: "openai-compatibility",
		Model:    "gpt-5",
		Success:  false,
		Error:    &Error{Code: code, Message: "boom", HTTPStatus: status},
	})
}

// awaitAutoDisable waits for (or asserts the absence of) a sink event.
func awaitAutoDisable(t *testing.T, got <-chan AutoDisableEvent, expectFired bool) (AutoDisableEvent, bool) {
	t.Helper()
	if expectFired {
		select {
		case ev := <-got:
			return ev, true
		case <-time.After(3 * time.Second):
			t.Fatalf("expected auto-disable event to fire")
		}
	}
	select {
	case ev := <-got:
		return ev, false
	case <-time.After(150 * time.Millisecond):
		return AutoDisableEvent{}, false
	}
}

func TestAutoDisableSinkFiresOnConfiguredCodeMatch(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := make(chan AutoDisableEvent, 4)
	manager.SetAutoDisableSink(func(_ context.Context, ev AutoDisableEvent) {
		got <- ev
	})

	auth := newAutoDisableTestAuth("auth-ad-code", "openai-compatibility",
		`["401","account_suspended"]`, "openai-compatibility-foo:key-7")
	registerAutoDisableTestAuth(t, manager, auth)

	markAutoDisableResult(manager, auth.ID, "account_suspended", http.StatusBadRequest)

	ev, ok := awaitAutoDisable(t, got, true)
	if !ok {
		t.Fatalf("expected sink to fire")
	}
	if ev.Provider != "openai-compatibility" {
		t.Fatalf("expected provider %q, got %q", "openai-compatibility", ev.Provider)
	}
	if ev.EntryID != 7 {
		t.Fatalf("expected entry id 7, got %d", ev.EntryID)
	}
	if ev.Code != "account_suspended" {
		t.Fatalf("expected code %q, got %q", "account_suspended", ev.Code)
	}
	if ev.Message == "" {
		t.Fatalf("expected the error message to be carried, got empty")
	}
}

func TestAutoDisableSinkNotFiredOnUnknownCode(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := make(chan AutoDisableEvent, 4)
	manager.SetAutoDisableSink(func(_ context.Context, ev AutoDisableEvent) {
		got <- ev
	})

	auth := newAutoDisableTestAuth("auth-ad-nomatch", "openai-compatibility",
		`["401","account_suspended"]`, "openai-compatibility-foo:key-7")
	registerAutoDisableTestAuth(t, manager, auth)

	markAutoDisableResult(manager, auth.ID, "other", http.StatusBadRequest)

	awaitAutoDisable(t, got, false)
}

func TestAutoDisableSinkNumericStatusMatch(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := make(chan AutoDisableEvent, 4)
	manager.SetAutoDisableSink(func(_ context.Context, ev AutoDisableEvent) {
		got <- ev
	})

	auth := newAutoDisableTestAuth("auth-ad-numeric", "openai-compatibility",
		`["401"]`, "openai-compatibility-foo:key-42")
	registerAutoDisableTestAuth(t, manager, auth)

	// err.Code is unrelated to the list; the numeric entry matches the HTTP status.
	markAutoDisableResult(manager, auth.ID, "some_transport_detail", http.StatusUnauthorized)

	ev, ok := awaitAutoDisable(t, got, true)
	if !ok {
		t.Fatalf("expected sink to fire via numeric status match")
	}
	if ev.Code != "401" {
		t.Fatalf("expected the configured numeric code %q, got %q", "401", ev.Code)
	}
	if ev.EntryID != 42 {
		t.Fatalf("expected entry id 42, got %d", ev.EntryID)
	}
}

func TestAutoDisableSinkNumericStatusMatchWhenCodeAbsent(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := make(chan AutoDisableEvent, 4)
	manager.SetAutoDisableSink(func(_ context.Context, ev AutoDisableEvent) {
		got <- ev
	})

	auth := newAutoDisableTestAuth("auth-ad-nocode", "openai-compatibility",
		`["403"]`, "openai-compatibility-foo:key-9")
	registerAutoDisableTestAuth(t, manager, auth)

	markAutoDisableResult(manager, auth.ID, "", http.StatusForbidden)

	awaitAutoDisable(t, got, true)
}

func TestAutoDisableSinkSkipsRequestScopedError(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := make(chan AutoDisableEvent, 4)
	manager.SetAutoDisableSink(func(_ context.Context, ev AutoDisableEvent) {
		got <- ev
	})

	auth := newAutoDisableTestAuth("auth-ad-scoped", "openai-compatibility",
		`["401","account_suspended"]`, "openai-compatibility-foo:key-7")
	registerAutoDisableTestAuth(t, manager, auth)

	markAutoDisableResult(manager, auth.ID, requestScopedErrorCode, http.StatusBadRequest)

	awaitAutoDisable(t, got, false)
}

func TestAutoDisableSinkSkipsConnectionLifecycleError(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := make(chan AutoDisableEvent, 4)
	manager.SetAutoDisableSink(func(_ context.Context, ev AutoDisableEvent) {
		got <- ev
	})

	auth := newAutoDisableTestAuth("auth-ad-lifecycle", "openai-compatibility",
		`["401"]`, "openai-compatibility-foo:key-7")
	registerAutoDisableTestAuth(t, manager, auth)

	markAutoDisableResult(manager, auth.ID, connectionLifecycleErrorCode, http.StatusInternalServerError)

	awaitAutoDisable(t, got, false)
}

func TestAutoDisableSinkNilIsNoop(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetAutoDisableSink(nil) // detach is a no-op

	auth := newAutoDisableTestAuth("auth-ad-nil", "openai-compatibility",
		`["401"]`, "openai-compatibility-foo:key-7")
	registerAutoDisableTestAuth(t, manager, auth)

	markAutoDisableResult(manager, auth.ID, "some_code", http.StatusUnauthorized)
	// Without a sink attached the conductor must never panic or block.
}

func TestAutoDisableSinkPanicRecovered(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetAutoDisableSink(func(_ context.Context, _ AutoDisableEvent) {
		panic("sink adapter exploded")
	})

	auth := newAutoDisableTestAuth("auth-ad-panic", "openai-compatibility",
		`["401"]`, "openai-compatibility-foo:key-7")
	registerAutoDisableTestAuth(t, manager, auth)

	// The classification path must survive a panicking sink.
	markAutoDisableResult(manager, auth.ID, "some_code", http.StatusUnauthorized)
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "openai-compatibility",
		Model:    "gpt-5",
		Success:  true,
	})

	// Give the recovered goroutine a beat so a missed recover would surface as a
	// test crash rather than a race.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAutoDisableSinkNotFiredWithoutAttributableEntry(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := make(chan AutoDisableEvent, 4)
	manager.SetAutoDisableSink(func(_ context.Context, ev AutoDisableEvent) {
		got <- ev
	})

	// Legacy YAML entry: providerKey:key-<name> is not attributable to a row.
	auth := newAutoDisableTestAuth("auth-ad-name", "openai-compatibility",
		`["401"]`, "openai-compatibility-foo:key-my-legacy-entry")
	registerAutoDisableTestAuth(t, manager, auth)

	markAutoDisableResult(manager, auth.ID, "some_code", http.StatusUnauthorized)

	awaitAutoDisable(t, got, false)
}

func TestEntryIDFromProviderKey(t *testing.T) {
	cases := []struct {
		name     string
		entryKey string
		want     int64
	}{
		{"simple numeric", "openai-compatibility-foo:key-7", 7},
		{"compound provider key with colons", "claude:42:key-7", 7},
		{"providers with prefix", "openai-compatibility-myprovider:key-99", 99},
		{"numeric zero", "providerKey:key-0", 0},
		{"non-numeric name", "providerKey:key-my-legacy-entry", 0},
		{"empty suffix", "providerKey:key-", 0},
		{"no key prefix", "providerKey", 0},
		{"empty attribute", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Attributes: map[string]string{}}
			if tc.entryKey != "" {
				auth.Attributes[AttributeEntryProviderKey] = tc.entryKey
			}
			if got := entryIDFromProviderKey(auth); got != tc.want {
				t.Fatalf("entryIDFromProviderKey(%q) = %d, want %d", tc.entryKey, got, tc.want)
			}
		})
	}
}

func TestAuthAutoDisableCodeMatch(t *testing.T) {
	cases := []struct {
		name      string
		codesJSON string
		code      string
		status    int
		want      string
	}{
		{"exact code match", `["401","account_suspended"]`, "account_suspended", 400, "account_suspended"},
		{"numeric status match with unrelated code", `["401"]`, "some_transport_detail", 401, "401"},
		{"numeric status match with absent code", `["403"]`, "", 403, "403"},
		{"no match code", `["401","account_suspended"]`, "other", 400, ""},
		{"no numeric status match", `["401"]`, "other", 400, ""},
		{"empty codes attribute", "", "account_suspended", 400, ""},
		{"nil auth", `["401"]`, "401", 400, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var auth *Auth
			if tc.codesJSON != "" {
				auth = &Auth{Attributes: map[string]string{AttributeAutoDisableCodes: tc.codesJSON}}
			}
			var resultErr *Error
			if tc.name != "nil auth" {
				resultErr = &Error{Code: tc.code, HTTPStatus: tc.status}
			}
			if got := authAutoDisableCodeMatch(auth, resultErr); got != tc.want {
				t.Fatalf("authAutoDisableCodeMatch = %q, want %q", got, tc.want)
			}
		})
	}
}
